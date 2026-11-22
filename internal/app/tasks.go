package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type taskUpdate struct {
	ID, Description, Status string
}

func taskTool() tool {
	return tool{Type: "function", Function: toolFunction{
		Name:        "task",
		Description: "Delegate an independent, bounded investigation to a subagent. Multiple task calls in one response run concurrently (up to 4). Each subagent has its own conversation and tools, shares the workspace and must return a concise result. Do not delegate work that depends on another task's output. Subagents cannot spawn further subagents.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"description": map[string]any{"type": "string", "description": "Short task label for the UI."},
			"prompt":      map[string]any{"type": "string", "description": "Self-contained instructions and expected result for the subagent."},
		}, "required": []string{"description", "prompt"}, "additionalProperties": false},
	}}
}

func runTask(cfg config, call toolCall) (string, error) {
	var args struct {
		Description string `json:"description"`
		Prompt      string `json:"prompt"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return "", fmt.Errorf("decode task: %w", err)
	}
	args.Description = strings.TrimSpace(args.Description)
	args.Prompt = strings.TrimSpace(args.Prompt)
	if args.Description == "" || args.Prompt == "" {
		return "", fmt.Errorf("task requires description and prompt")
	}
	if cfg.Subagent {
		return "", fmt.Errorf("subagents cannot start other subagents")
	}
	if cfg.OnTask != nil {
		cfg.OnTask(taskUpdate{ID: call.ID, Description: args.Description, Status: "running"})
	}
	child := cfg
	child.Subagent = true
	child.OnStatus = nil // Child status must not overwrite the parent's tool status.
	child.OnTrace = nil  // Keep child trace events out of the parent's live timeline.
	var childUsage []usageEvent
	child.OnUsage = func(usage usageEvent) {
		// A subagent's prompt is independent of the parent's context window.
		usage.Child = true
		childUsage = append(childUsage, usage)
	}
	// Each child has its own trace run; all runs share the recorder safely.
	child.traceRun = nil
	ctx, cancel := context.WithTimeout(cfg.context(), 5*time.Minute)
	defer cancel()
	child.Context = ctx
	text, err := sendChat(child, []message{{Role: "user", Content: args.Prompt}}, false)
	for _, usage := range childUsage {
		if cfg.OnUsage != nil {
			cfg.OnUsage(usage)
		}
	}
	status := "done"
	if err != nil {
		status = "failed"
	}
	if cfg.context().Err() != nil {
		status = "cancelled"
	}
	if cfg.OnTask != nil {
		cfg.OnTask(taskUpdate{ID: call.ID, Description: args.Description, Status: status})
	}
	return text, err
}

func executeParallelTasks(calls []toolCall, cfg config) ([]message, error) {
	results := make([]message, len(calls))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
	for i, call := range calls {
		if cfg.OnTask != nil {
			var args struct {
				Description string `json:"description"`
			}
			_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
			cfg.OnTask(taskUpdate{ID: call.ID, Description: args.Description, Status: "queued"})
		}
		wg.Add(1)
		go func(i int, call toolCall) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-cfg.context().Done():
				if cfg.OnTask != nil {
					var args struct {
						Description string `json:"description"`
					}
					_ = json.Unmarshal([]byte(call.Function.Arguments), &args)
					cfg.OnTask(taskUpdate{ID: call.ID, Description: args.Description, Status: "cancelled"})
				}
				return
			}
			messages, err := executeToolCalls([]toolCall{call}, cfg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				firstErr = err
			} else {
				results[i] = messages[0]
			}
		}(i, call)
	}
	wg.Wait()
	if err := cfg.context().Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return results, nil
}
