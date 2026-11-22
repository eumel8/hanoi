package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	appconfig "hanoi/internal/config"
)

func TestParallelSubagentsAndParentFollowup(t *testing.T) {
	var mu sync.Mutex
	started := make(chan string, 2)
	release := make(chan struct{})
	var updates []taskUpdate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		prompt := req.Messages[len(req.Messages)-1].Content
		w.Header().Set("Content-Type", "application/json")
		switch prompt {
		case "parent":
			var calls []toolCall
			for i := 1; i <= 2; i++ {
				call := toolCall{ID: fmt.Sprintf("task-%d", i), Type: "function"}
				call.Function.Name = "task"
				call.Function.Arguments = fmt.Sprintf(`{"description":"research %d","prompt":"child %d"}`, i, i)
				calls = append(calls, call)
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Role: "assistant", ToolCalls: calls}}}})
		case "child 1", "child 2":
			for _, tool := range req.Tools {
				if tool.Function.Name == "task" {
					t.Error("child may not receive task tool")
				}
			}
			started <- prompt
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 2}, "choices": []any{map[string]any{"message": message{Role: "assistant", Content: prompt + " done"}}}})
		default:
			if len(req.Messages) != 4 || req.Messages[2].Content != "child 1 done" || req.Messages[3].Content != "child 2 done" {
				t.Errorf("results out of order or missing: %#v", req.Messages)
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message{Role: "assistant", Content: "combined"}}}})
		}
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "test", APIKey: "test", BaseURL: server.URL}, Context: context.Background(), OnTask: func(u taskUpdate) {
		mu.Lock()
		updates = append(updates, u)
		mu.Unlock()
	}}
	result := make(chan string, 1)
	go func() {
		text, err := sendChat(cfg, []message{{Role: "user", Content: "parent"}}, false)
		if err != nil {
			result <- err.Error()
		} else {
			result <- text
		}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started: // Both children must start before either is released.
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("subagents did not start concurrently")
		}
	}
	close(release)
	if got := <-result; got != "combined" {
		t.Fatalf("result: %s", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 6 {
		t.Fatalf("expected queued/running/done for both tasks, got %#v", updates)
	}
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10), tasks: map[string]taskUpdate{}}
	for _, update := range updates {
		m.tasks[update.ID] = update
	}
	m.toggleTasks()
	if view := m.View(); !strings.Contains(view, "research 1: done") || !strings.Contains(view, "research 2: done") {
		t.Fatalf("task status missing in TUI: %s", view)
	}
}

func TestSubagentUsageDoesNotReplaceParentContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		prompt := req.Messages[len(req.Messages)-1].Content
		if prompt == "child" {
			io.WriteString(w, `{"usage":{"prompt_tokens":9,"completion_tokens":2},"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
		} else {
			io.WriteString(w, `{"usage":{"prompt_tokens":30,"completion_tokens":4},"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
		}
	}))
	defer server.Close()
	cfg := pricedTestConfig(server.URL)
	var usage sessionUsage
	cfg.OnUsage = func(e usageEvent) { usage.add(e, cfg) }
	_, err := sendChat(cfg, []message{{Role: "user", Content: "parent"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	call := toolCall{ID: "child", Type: "function"}
	call.Function.Name, call.Function.Arguments = "task", `{"description":"work","prompt":"child"}`
	if _, err := runTask(cfg, call); err != nil {
		t.Fatal(err)
	}
	if usage.Input != 39 || usage.Output != 6 || usage.LastInput != 30 {
		t.Fatalf("subagent usage must count without changing parent context: %+v", usage)
	}
}

func TestTaskValidationAndCancellation(t *testing.T) {
	call := toolCall{}
	call.Function.Arguments = `{}`
	if _, err := runTask(config{}, call); err == nil {
		t.Fatal("empty task accepted")
	}
	call.Function.Arguments = `{"description":"x","prompt":"y"}`
	if _, err := runTask(config{Subagent: true}, call); err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("nested task: %v", err)
	}
}

func TestSubagentCancelStopsRequest(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() { close(release); server.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var updates []taskUpdate
	var mu sync.Mutex
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}, Context: ctx, OnTask: func(u taskUpdate) {
		mu.Lock()
		updates = append(updates, u)
		mu.Unlock()
	}}
	call := toolCall{ID: "cancel-task", Type: "function"}
	call.Function.Name = "task"
	call.Function.Arguments = `{"description":"wait","prompt":"wait"}`
	finished := make(chan error, 1)
	go func() { _, err := executeToolCalls([]toolCall{call}, cfg); finished <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("task request did not start")
	}
	cancel()
	select {
	case err := <-finished:
		if err != context.Canceled {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subagent did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 2 || updates[1].Status != "cancelled" {
		t.Fatalf("missing cancelled task status: %+v", updates)
	}
}

func TestSubtaskFailureShowsReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"provider unavailable"}}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	var updates []taskUpdate
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}, OnTask: func(update taskUpdate) { updates = append(updates, update) }}
	call := toolCall{ID: "failed-task", Type: "function"}
	call.Function.Name, call.Function.Arguments = "task", `{"description":"research","prompt":"check"}`
	_, err := runTask(cfg, call)
	if err == nil || len(updates) != 2 || updates[1].Status != "failed" || !strings.Contains(updates[1].Reason, "503") {
		t.Fatalf("expected failed status with provider cause: err=%v updates=%+v", err, updates)
	}
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10), tasks: map[string]taskUpdate{call.ID: updates[1]}}
	if !strings.Contains(m.taskDetails(), "503") {
		t.Fatal("task details omit failure cause")
	}
}

func TestSubtaskCancellationShowsReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var updates []taskUpdate
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}, Context: ctx, OnTask: func(update taskUpdate) { updates = append(updates, update) }}
	call := toolCall{ID: "timeout-task", Type: "function"}
	call.Function.Name, call.Function.Arguments = "task", `{"description":"research","prompt":"check"}`
	_, _ = runTask(cfg, call)
	if len(updates) != 2 || updates[1].Status != "cancelled" || updates[1].Reason == "" {
		t.Fatalf("expected cancellation cause: %+v", updates)
	}
}

func TestTaskSummaryAndDetailsToggle(t *testing.T) {
	m := tuiModel{
		width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10),
		history: []string{strings.Repeat("chat line\n", 80)}, loading: true, tasks: make(map[string]taskUpdate),
	}
	m.textarea.SetHeight(4)
	for i := 0; i < 10; i++ {
		m.tasks[fmt.Sprintf("task-%02d", i)] = taskUpdate{Description: fmt.Sprintf("research %d", i), Status: "failed", Reason: "context deadline exceeded"}
	}
	m.resizeChat()
	m.refreshChat()
	if got := lipgloss.Height(m.View()); got > m.height {
		t.Fatalf("collapsed tasks exceed terminal height: %d > %d", got, m.height)
	}
	if got := strings.Count(strings.Join(m.chatHeader(), "\n"), "research "); got != 0 {
		t.Fatalf("collapsed summary unexpectedly contains task details: %d", got)
	}
	if m.viewport.Height < 8 {
		t.Fatalf("collapsed tasks leave too little chat space: %d", m.viewport.Height)
	}
	m.viewport.GotoBottom()
	offset := m.viewport.YOffset
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.tasksOpen || !strings.Contains(m.View(), "context deadline exceeded") || lipgloss.Height(m.View()) > m.height {
		t.Fatal("Ctrl+S must open scrollable task details within terminal height")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyPgDown})
	if m.viewport.YOffset == 0 {
		t.Fatal("task details must be scrollable")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.tasksOpen || m.viewport.YOffset != offset {
		t.Fatal("Esc must restore chat and scroll position")
	}
}

func TestTaskDetailsUpdateWhileLoading(t *testing.T) {
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10), loading: true}
	m.textarea.SetHeight(4)
	m.resizeChat()
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	m = updateModel(m, taskUpdate{ID: "one", Description: "research", Status: "failed", Reason: "context deadline exceeded"})
	if !strings.Contains(m.View(), "context deadline exceeded") {
		t.Fatal("task detail view did not refresh during an active response")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.tasksOpen || !strings.Contains(m.View(), "tasks: 1") {
		t.Fatal("Ctrl+S must close details while loading without losing task count")
	}
}
