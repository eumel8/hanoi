package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "hanoi/internal/config"
)

func TestResponsesToolLoop(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request struct {
					Input  []json.RawMessage `json:"input"`
					Tools  []map[string]any  `json:"tools"`
					Store  bool              `json:"store"`
					Stream bool              `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/responses" || request.Store || request.Stream != stream || request.Tools[0]["name"] != "bash" {
					t.Errorf("invalid request: %+v", request)
				}
				output := `[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"opaque"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"command\":\"printf result\"}"}]`
				if requests == 2 {
					input, _ := json.Marshal(request.Input)
					for _, want := range []string{"opaque", "function_call_output", "call_1", "result"} {
						if !strings.Contains(string(input), want) {
							t.Errorf("missing %s in follow-up: %s", want, input)
						}
					}
					output = `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]`
				}
				result := `{"status":"completed","output":` + output + `}`
				if stream {
					if requests == 2 {
						fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"done\"}\n\n")
					}
					fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", result)
				} else {
					fmt.Fprint(w, result)
				}
			}))
			defer server.Close()
			cfg := config{Config: appconfig.Config{Model: "gpt-6-astra", BaseURL: server.URL + "/responses"}}
			var text string
			var err error
			if stream {
				text, err = sendStreamingChat(cfg, []message{{Role: "user", Content: "run"}}, nil)
			} else {
				text, err = sendChat(cfg, []message{{Role: "user", Content: "run"}}, false)
			}
			if err != nil || text != "done" || requests != 2 {
				t.Fatalf("text=%q requests=%d err=%v", text, requests, err)
			}
		})
	}
}

func TestResponsesRouting(t *testing.T) {
	for _, tc := range []struct {
		url, model string
		want       bool
	}{
		{"https://api.openai.com/v1/chat/completions", "gpt-6-astra", true},
		{"https://api.openai.com/v1/chat/completions", "gpt-6.1-sol", true},
		{"https://api.openai.com/v1/chat/completions", "gpt-4o-mini", false},
		{"https://other.example/v1/chat/completions", "gpt-6-astra", false},
		{"https://other.example/v1/responses", "custom", true},
	} {
		if got := useResponses(config{Config: appconfig.Config{BaseURL: tc.url, Model: tc.model}}); got != tc.want {
			t.Errorf("%s %s: %t", tc.url, tc.model, got)
		}
	}
}

func TestResponsesStreamRequiresCompletion(t *testing.T) {
	for _, data := range []string{
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
		"data: {\"type\":\"response.incomplete\"}\n\n",
	} {
		if _, err := readResponseStream(strings.NewReader(data), nil); err == nil {
			t.Fatal("incomplete response accepted")
		}
	}
}
