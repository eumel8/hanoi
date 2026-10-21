package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "hanoi/internal/config"
)

func TestToolFollowupWithEmptyContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, msg := range request.Messages {
			if _, ok := msg["content"].(string); !ok {
				t.Errorf("content must be a string: %#v", msg)
				http.Error(w, "expected string content", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_test","type":"function","function":{"name":"bash","arguments":"{\"command\":\"true\"}"}}]}}]}`)
			return
		}
		if len(request.Messages) != 3 {
			t.Errorf("expected user, assistant and tool messages, got %d", len(request.Messages))
		} else {
			assistant, tool := request.Messages[1], request.Messages[2]
			if assistant["content"] != "" || assistant["role"] != "assistant" || assistant["tool_calls"] == nil {
				t.Errorf("invalid assistant follow-up: %#v", assistant)
			}
			if tool["content"] != "" || tool["role"] != "tool" || tool["tool_call_id"] != "call_test" {
				t.Errorf("invalid tool result: %#v", tool)
			}
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
	}))
	defer server.Close()

	text, err := sendChat(config{Config: appconfig.Config{APIKey: "test-key", Model: "test", BaseURL: server.URL}}, []message{{Role: "user", Content: "run true"}}, false)
	if err != nil || text != "done" || requests != 2 {
		t.Fatalf("text=%q requests=%d err=%v", text, requests, err)
	}
}

func TestAPILogging(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		stream bool
	}{
		{"normal", 200, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`, false},
		{"stream", 200, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n", true},
		{"error", 400, `{"error":{"message":"expected string content"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer private-test-key" {
					t.Error("missing authorization header")
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			var output bytes.Buffer
			cfg := config{Config: appconfig.Config{APIKey: "private-test-key", Model: "test", BaseURL: server.URL}, Logger: log.New(&output, "", 0)}
			text, err := sendChat(cfg, []message{{Role: "user", Content: "test prompt"}}, tc.stream)
			if tc.status == 400 {
				if err == nil || !strings.Contains(err.Error(), "expected string content") {
					t.Fatalf("expected API error, got %v", err)
				}
			} else if err != nil || text != "hello" {
				t.Fatalf("text=%q err=%v", text, err)
			}
			logs := output.String()
			for _, want := range []string{"request body=", "test prompt", "response status=", http.StatusText(tc.status), "response body_chunk="} {
				if !strings.Contains(logs, want) {
					t.Errorf("log missing %q: %s", want, logs)
				}
			}
			if strings.Contains(logs, "private-test-key") || strings.Contains(logs, "Authorization") {
				t.Error("log contains authorization credentials")
			}
		})
	}
}

func writeStreamChoice(w io.Writer, delta map[string]any, finishReason any) {
	data, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	})
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func TestStreamingToolFollowup(t *testing.T) {
	for _, rounds := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d rounds", rounds), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request chatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if !request.Stream || len(request.Tools) == 0 {
					t.Error("follow-up must retain streaming and tool definitions")
				}
				if len(request.Messages) != 1+3*(requests-1) {
					t.Errorf("unexpected message count: %d", len(request.Messages))
					w.WriteHeader(400)
					return
				}
				for round := 0; round < requests-1; round++ {
					assistant := request.Messages[1+3*round]
					if assistant.Role != "assistant" || assistant.Content != "Checking." || len(assistant.ToolCalls) != 2 {
						t.Errorf("invalid tool-calling assistant: %#v", assistant)
						w.WriteHeader(400)
						return
					}
					for index, want := range []string{"tool-zero", "tool-one"} {
						call := assistant.ToolCalls[index]
						result := request.Messages[2+3*round+index]
						if call.ID != fmt.Sprintf("call_%d_%d", round+1, index) || call.Function.Name != "bash" || result.ToolCallID != call.ID || result.Role != "tool" || result.Content != want {
							t.Errorf("invalid call/result: %#v / %#v", call, result)
						}
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests <= rounds {
					writeStreamChoice(w, map[string]any{"role": "assistant", "content": "Checking."}, nil)
					// Interleave two calls, starting with index 1, and split both names and arguments.
					for _, part := range []int{0, 1} {
						for _, index := range []int{1, 0} {
							name := []string{"tool-zero", "tool-one"}[index]
							args, _ := json.Marshal(bashToolArgs{Command: "printf " + name})
							mid := len(args) / 2
							call := map[string]any{"index": index}
							if part == 0 {
								call["id"] = fmt.Sprintf("call_%d_%d", requests, index)
								call["type"] = "function"
								call["function"] = map[string]any{"name": "ba", "arguments": string(args[:mid])}
							} else {
								call["function"] = map[string]any{"name": "sh", "arguments": string(args[mid:])}
							}
							writeStreamChoice(w, map[string]any{"tool_calls": []any{call}}, nil)
						}
					}
					writeStreamChoice(w, map[string]any{}, "tool_calls")
				} else {
					writeStreamChoice(w, map[string]any{"content": "hello"}, nil)
					writeStreamChoice(w, map[string]any{}, "stop")
				}
				io.WriteString(w, "data: [DONE]\n\n")
			}))
			defer server.Close()

			var chunks strings.Builder
			messages := []message{{Role: "user", Content: "run tools"}}
			text, err := sendStreamingChat(config{Config: appconfig.Config{APIKey: "test", Model: "test", BaseURL: server.URL}}, messages, func(piece string) {
				chunks.WriteString(piece)
			})
			want := strings.Repeat("Checking.", rounds) + "hello"
			if err != nil || text != want || chunks.String() != want+"\n" || requests != rounds+1 {
				t.Fatalf("text=%q chunks=%q requests=%d err=%v", text, chunks.String(), requests, err)
			}
		})
	}
}

func TestIncompleteStreamingToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   string
		index  any
		finish string
		done   bool
	}{
		{"truncated stream", `{"command":"true"}`, 0, "tool_calls", false},
		{"token limit", `{"command":"true"}`, 0, "length", true},
		{"invalid arguments", `{"command":`, 0, "tool_calls", true},
		{"missing index", `{"command":"true"}`, nil, "tool_calls", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stream bytes.Buffer
			writeStreamChoice(&stream, map[string]any{"tool_calls": []any{map[string]any{
				"index": tc.index, "id": "call_test", "type": "function",
				"function": map[string]any{"name": "bash", "arguments": tc.args},
			}}}, nil)
			writeStreamChoice(&stream, map[string]any{}, tc.finish)
			if tc.done {
				io.WriteString(&stream, "data: [DONE]\n\n")
			}
			if _, err := handleStream(&stream, nil); err == nil {
				t.Fatal("expected an incomplete or malformed tool-call error")
			}
		})
	}
}
