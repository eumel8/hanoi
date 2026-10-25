package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Astra and Sol require Responses for function calling. Explicit Responses
// endpoints also allow compatible providers without model-name heuristics.
func useResponses(cfg config) bool {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/responses") ||
		(u.Hostname() == "api.openai.com" && (cfg.Model == "gpt-6-astra" || cfg.Model == "gpt-6.1-sol"))
}

type responseResult struct {
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func responseInput(messages []message) []any {
	input := []any{}
	for _, msg := range messages {
		if len(msg.ResponseOutput) > 0 {
			for _, item := range msg.ResponseOutput {
				input = append(input, item)
			}
			continue
		}
		if msg.Role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": msg.ToolCallID, "output": msg.Content})
			continue
		}
		if msg.Content != "" {
			input = append(input, map[string]any{"role": msg.Role, "content": msg.Content})
		}
		for _, call := range msg.ToolCalls {
			input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	return input
}

func requestResponse(cfg config, messages []message, tools []tool, stream bool, onChunk func(string)) (message, error) {
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return message{}, fmt.Errorf("responses endpoint: %w", err)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(u.Path, "/chat/completions") {
		u.Path = strings.TrimSuffix(u.Path, "/chat/completions") + "/responses"
	}
	cfg.BaseURL = u.String()
	definitions := []any{}
	for _, tool := range tools {
		definitions = append(definitions, map[string]any{"type": "function", "name": tool.Function.Name, "description": tool.Function.Description, "parameters": tool.Function.Parameters, "strict": false})
	}
	payload, err := json.Marshal(map[string]any{"model": cfg.Model, "input": responseInput(messages), "tools": definitions, "stream": stream, "store": false, "include": []string{"reasoning.encrypted_content"}})
	if err != nil {
		return message{}, fmt.Errorf("marshal responses request: %w", err)
	}
	resp, err := sendChatRequest(cfg, payload)
	if err != nil {
		return message{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return message{}, fmt.Errorf("responses API error (%s)", resp.Status)
		}
		return message{}, fmt.Errorf("responses API error (%s): %s", resp.Status, body)
	}
	var result responseResult
	if stream {
		result, err = readResponseStream(resp.Body, onChunk)
	} else {
		err = json.NewDecoder(resp.Body).Decode(&result)
	}
	if err != nil {
		return message{}, err
	}
	if result.Error != nil {
		return message{}, fmt.Errorf("responses API: %s", result.Error.Message)
	}
	if result.Status != "completed" {
		return message{}, fmt.Errorf("responses API did not complete (status=%q)", result.Status)
	}
	msg := message{Role: "assistant", ResponseOutput: result.Output}
	for _, raw := range result.Output {
		var item struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Type    string `json:"type"`
				Text    string `json:"text"`
				Refusal string `json:"refusal"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return message{}, fmt.Errorf("decode response output: %w", err)
		}
		if item.Type == "function_call" {
			if item.CallID == "" || item.Name == "" || !json.Valid([]byte(item.Arguments)) {
				return message{}, fmt.Errorf("invalid responses function call")
			}
			call := toolCall{ID: item.CallID, Type: "function"}
			call.Function.Name, call.Function.Arguments = item.Name, item.Arguments
			msg.ToolCalls = append(msg.ToolCalls, call)
		}
		for _, content := range item.Content {
			if content.Type == "output_text" {
				msg.Content += content.Text
			}
			if content.Type == "refusal" {
				msg.Content += content.Refusal
			}
		}
	}
	return msg, nil
}

func readResponseStream(body io.Reader, onChunk func(string)) (responseResult, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	var data []string
	var result responseResult
	completed := false
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		payload := strings.Join(data, "\n")
		data = nil
		if payload == "[DONE]" {
			return nil
		}
		var event struct {
			Type     string         `json:"type"`
			Delta    string         `json:"delta"`
			Message  string         `json:"message"`
			Response responseResult `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("decode responses stream: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta", "response.refusal.delta":
			if onChunk != nil {
				onChunk(event.Delta)
			}
		case "response.completed":
			result, completed = event.Response, true
		case "response.failed", "response.incomplete":
			return fmt.Errorf("responses stream: %s", event.Type)
		case "error":
			return fmt.Errorf("responses stream error: %s", event.Message)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return result, err
			}
			if completed {
				return result, nil
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read responses stream: %w", err)
	}
	if err := consume(); err != nil {
		return result, err
	}
	if !completed {
		return result, fmt.Errorf("read responses stream: %w (missing response.completed)", io.ErrUnexpectedEOF)
	}
	return result, nil
}
