package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	appconfig "hanoi/internal/config"
	"hanoi/internal/trace"
)

func TestTraceToolRoundTrip(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				msg := message{Role: "assistant", Content: "done"}
				if requests == 1 {
					var call toolCall
					call.ID, call.Type = "call-1", "function"
					call.Function.Name, call.Function.Arguments = "bash", `{"command":"printf abc; exit 7"}`
					msg.Content, msg.ToolCalls = "", []toolCall{call}
				}
				if streaming {
					// Stream tool calls require an index on each delta.
					if requests == 1 {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"bash\",\"arguments\":\"{\\\"command\\\":\\\"printf abc; exit 7\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\ndata: [DONE]\n\n")
					}
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
			}))
			defer server.Close()
			var log bytes.Buffer
			var events []traceEvent
			cfg := config{Config: appconfig.Config{Model: "test", BaseURL: server.URL}, Trace: trace.New(&log), OnTrace: func(e traceEvent) { events = append(events, e) }}
			messages := []message{{Role: "user", Content: "run command"}}
			var text string
			var err error
			if streaming {
				text, err = sendStreamingChat(cfg, messages, nil)
			} else {
				text, err = sendChat(cfg, messages, false)
			}
			if err != nil || text != "done" {
				t.Fatalf("text=%q err=%v", text, err)
			}
			var types []string
			decoder := json.NewDecoder(&log)
			for i, e := range events {
				types = append(types, e.Type)
				var saved traceEvent
				if err := decoder.Decode(&saved); err != nil || !reflect.DeepEqual(saved, e) {
					t.Fatalf("JSONL event mismatch: %v", err)
				}
				if e.Sequence != int64(i+1) || e.RunID == "" || e.RunID != events[0].RunID || e.Turn != 1 {
					t.Fatalf("invalid correlation: %+v", e)
				}
				if e.Type == "tool.completed" && (e.ExitCode == nil || *e.ExitCode != 7 || e.Status != "error" || e.OutputBytes != 3) {
					t.Fatalf("invalid tool outcome: %+v", e)
				}
			}
			want := []string{"turn.started", "model.started", "model.completed", "tool.started", "tool.completed", "tool.result", "model.started", "model.completed", "turn.completed"}
			if !reflect.DeepEqual(types, want) {
				t.Fatalf("event order: %v", types)
			}
		})
	}
}

func TestTraceAPIErrorAndWriteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }))
	defer server.Close()
	var events []traceEvent
	cfg := config{Config: appconfig.Config{Model: "test", BaseURL: server.URL}, Trace: trace.New(nil), OnTrace: func(e traceEvent) { events = append(events, e) }}
	_, err := sendChat(cfg, []message{{Role: "user", Content: "hello"}}, false)
	if err == nil || len(events) != 4 || events[2].Status != "error" || events[3].Status != "error" {
		t.Fatalf("API failure not traced: %+v, %v", events, err)
	}
}

func TestTraceViewDuringStreaming(t *testing.T) {
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10), loading: true, history: []string{strings.Repeat("chat line\n", 80)}}
	m.resizeChat()
	m.refreshChat()
	m.viewport.SetYOffset(10)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m = updated.(tuiModel)
	if !m.traceOpen {
		t.Fatal("trace must open during a running turn")
	}
	updated, _ = m.Update(traceEvent{Type: "tool.completed", Tool: "bash", Status: "error", Error: "test failure"})
	m = updated.(tuiModel)
	if !strings.Contains(m.View(), "test failure") {
		t.Fatal("live trace event missing")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.traceOpen || m.viewport.YOffset != 10 {
		t.Fatal("returning to chat must restore the scroll position")
	}
}
