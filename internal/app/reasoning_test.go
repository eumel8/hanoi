package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	appconfig "hanoi/internal/config"
)

const toolReasoningError = `{"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6-astra in /v1/chat/completions. To use function tools, use /v1/responses or set reasoning_effort to 'none'.","type":"invalid_request_error","param":"reasoning_effort"}}`

func TestToolReasoningRetry(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, rejectRetry := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/reject=%t", streaming, rejectRetry), func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					var request chatRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if len(request.Tools) == 0 || request.Stream != streaming || request.Model != "gpt-6-astra" {
						t.Errorf("request settings lost: %+v", request)
					}
					if requests == 1 {
						if request.ReasoningEffort != "" {
							t.Error("initial request must retain provider defaults")
						}
					} else if request.ReasoningEffort != "none" {
						t.Error("retry must disable reasoning")
					}
					if requests == 1 || rejectRetry {
						w.WriteHeader(400)
						fmt.Fprint(w, toolReasoningError)
					} else if streaming {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\ndata: [DONE]\n\n")
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}]}`)
					}
				}))
				defer server.Close()
				cfg := config{Config: appconfig.Config{Model: "gpt-6-astra", BaseURL: server.URL}}
				msg, err := requestModel(cfg, []message{{Role: "user", Content: "hello"}}, streaming, nil)
				if requests != 2 || (err != nil) != rejectRetry || (!rejectRetry && msg.Content != "OK") {
					t.Fatalf("requests=%d msg=%+v err=%v", requests, msg, err)
				}
			})
		}
	}
}

func TestToolReasoningRetryIsTargeted(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{500, toolReasoningError},
		{400, `{"error":{"param":"reasoning_effort","type":"invalid_request_error","message":"Unsupported value"}}`},
		{400, `{"error":{"param":"tools","message":"Tools unsupported"}}`},
	} {
		if requiresToolReasoningNone(tc.status, []byte(tc.body)) {
			t.Fatalf("unrelated error must not trigger retry: %s", tc.body)
		}
	}
}
