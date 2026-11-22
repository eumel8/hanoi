package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	appconfig "hanoi/internal/config"
)

func TestContextLimitCompactsChatAndPreservesSavedHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var mu sync.Mutex
			attempts, summaries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req chatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if len(req.Messages) == 2 && strings.Contains(req.Messages[0].Content, "Summarize the prior conversation") {
					summaries++
					if len(req.Tools) != 0 || req.Stream {
						t.Error("compaction must be non-streaming and have no tools")
					}
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Prior work summarized."}}]}`)
					return
				}
				attempts++
				if attempts == 1 {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"error":{"code":"context_length_exceeded","message":"maximum context length exceeded"}}`)
					return
				}
				if len(req.Messages) != 4 || req.Messages[0].Content != "rules" || !strings.Contains(req.Messages[1].Content, "Prior work summarized") || req.Messages[2].Content != "recent answer" || req.Messages[3].Content != "latest request" {
					t.Errorf("unexpected compacted request: %+v", req.Messages)
				}
				if stream {
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
				} else {
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
				}
			}))
			defer server.Close()
			cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}}
			messages := []message{{Role: "system", Content: "rules"}, {Role: "user", Content: "old request"}, {Role: "assistant", Content: "recent answer"}, {Role: "user", Content: "latest request"}}
			var result string
			var err error
			if stream {
				result, err = sendStreamingChat(cfg, messages, nil)
			} else {
				result, err = sendChat(cfg, messages, false)
			}
			if err != nil || strings.TrimSpace(result) != "done" || attempts != 2 || summaries != 1 || messages[1].Content != "old request" {
				t.Fatalf("result=%q attempts=%d summaries=%d original=%+v err=%v", result, attempts, summaries, messages, err)
			}
		})
	}
}

func TestContextLimitResponsesKeepsToolExchange(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []map[string]any `json:"input"`
			Tools []any            `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		if requests == 2 {
			if len(req.Tools) != 0 {
				t.Error("summary used tools")
			}
			io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Summary."}]}]}`)
			return
		}
		if requests == 1 {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"This model's maximum context length was exceeded."}}`)
			return
		}
		calls, outputs := 0, 0
		for _, item := range req.Input {
			if item["type"] == "function_call" {
				calls++
			}
			if item["type"] == "function_call_output" {
				outputs++
			}
		}
		if calls != 1 || outputs != 1 {
			t.Errorf("tool exchange broken: %+v", req.Input)
		}
		io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL + "/responses"}}
	call := toolCall{ID: "call-1", Type: "function"}
	call.Function.Name, call.Function.Arguments = "bash", `{}`
	msgs := []message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "old reply"}, {Role: "user", Content: "latest"}, {Role: "assistant", ToolCalls: []toolCall{call}}, {Role: "tool", ToolCallID: "call-1", Content: "result"}}
	msg, _, err := requestWithCompaction(cfg, msgs, false, nil)
	if err != nil || msg.Content != "ok" || requests != 3 {
		t.Fatalf("result=%+v requests=%d err=%v", msg, requests, err)
	}
}

func TestContextLimitNoRetryForOtherErrorsOrUncompactableInput(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		wantRequests int
	}{
		{"rate-limit", `{"error":{"code":"rate_limit_exceeded","message":"try later"}}`, 1},
		{"tool-error", `{"error":{"code":"invalid_request_error","message":"invalid tool arguments"}}`, 1},
		{"single-request", `{"error":{"code":"context_length_exceeded"}}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				w.WriteHeader(400)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}}
			_, _, err := requestWithCompaction(cfg, []message{{Role: "user", Content: "single"}}, false, nil)
			if err == nil || requests != tc.wantRequests {
				t.Fatalf("err=%v requests=%d", err, requests)
			}
		})
	}
}

func TestContextLimitResponsesStreamEvent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"context_length_exceeded\"}}}\n\n")
			return
		}
		if requests == 2 {
			io.WriteString(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"summary"}]}]}`)
			return
		}
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL + "/responses"}}
	msg, _, err := requestWithCompaction(cfg, []message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "reply"}, {Role: "user", Content: "latest"}}, true, nil)
	if err != nil || msg.Content != "done" || requests != 3 {
		t.Fatalf("result=%+v requests=%d err=%v", msg, requests, err)
	}
}

func TestContextLimitSubagentCompactsOwnHistory(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		requests++
		if len(req.Messages) == 2 && strings.Contains(req.Messages[0].Content, "Summarize the prior conversation") {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"previous research"}}]}`)
			return
		}
		if requests == 1 {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"code":"context_length_exceeded"}}`)
			return
		}
		if len(req.Messages) < 3 || !strings.Contains(req.Messages[0].Content, "previous research") {
			t.Errorf("subagent did not receive summary: %+v", req.Messages)
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"finished"}}]}`)
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}, Subagent: true}
	result, err := sendChat(cfg, []message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "older"}, {Role: "user", Content: "new"}}, false)
	if err != nil || result != "finished" || requests != 3 {
		t.Fatalf("result=%q requests=%d err=%v", result, requests, err)
	}
}

func TestContextLimitStreamingAfterPartialOutputDoesNotRetry(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"code\":\"context_length_exceeded\"}}}\n\n")
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL + "/responses"}}
	_, _, err := requestWithCompaction(cfg, []message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "older"}, {Role: "user", Content: "new"}}, true, func(string) {})
	if err == nil || requests != 1 {
		t.Fatalf("partial stream must not retry: requests=%d err=%v", requests, err)
	}
}

func TestContextLimitRetryIsBounded(t *testing.T) {
	requests, summaries := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if len(req.Messages) == 2 && strings.Contains(req.Messages[0].Content, "Summarize the prior conversation") {
			summaries++
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"short summary"}}]}`)
			return
		}
		requests++
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"code":"context_length_exceeded"}}`)
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "demo", BaseURL: server.URL}}
	_, _, err := requestWithCompaction(cfg, []message{{Role: "user", Content: "old"}, {Role: "assistant", Content: "older"}, {Role: "user", Content: "new"}}, false, nil)
	if err == nil || requests > 4 || summaries > 3 {
		t.Fatalf("unbounded retries: requests=%d summaries=%d err=%v", requests, summaries, err)
	}
}
