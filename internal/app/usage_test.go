package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appconfig "hanoi/internal/config"
)

func pricedTestConfig(url string) config {
	model := appconfig.Model{ContextWindow: 1000}
	model.Cost.Input, model.Cost.Output = 2, 4
	return config{Config: appconfig.Config{Model: "demo", Provider: "test", ModelKey: "demo", BaseURL: url, Providers: map[string]appconfig.Provider{"test": {Models: map[string]appconfig.Model{"demo": model}}}}}
}

func TestUsageAcrossResponsesAndChatStreams(t *testing.T) {
	for _, kind := range []string{"chat", "stream", "responses"} {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				switch kind {
				case "chat":
					io.WriteString(w, `{"usage":{"prompt_tokens":12,"completion_tokens":3},"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
				case "stream":
					io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
				case "responses":
					io.WriteString(w, `{"status":"completed","usage":{"input_tokens":12,"output_tokens":3},"output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
				}
			}))
			defer server.Close()
			cfg := pricedTestConfig(server.URL)
			if kind == "responses" {
				cfg.BaseURL += "/responses"
			}
			var usage sessionUsage
			cfg.OnUsage = func(e usageEvent) { usage.add(e, cfg) }
			text, err := sendChat(cfg, []message{{Role: "user", Content: "test"}}, kind == "stream")
			if err != nil || text != "ok" {
				t.Fatalf("response %q: %v", text, err)
			}
			if usage.Input != 12 || usage.Output != 3 || usage.LastInput != 12 || !strings.Contains(usage.summary(cfg), "context: 12/1000") || !strings.Contains(usage.summary(cfg), "$0.0000") {
				t.Fatalf("unexpected usage: %+v (%s)", usage, usage.summary(cfg))
			}
		})
	}
}

func TestUsagePersistenceAndUnknownPrice(t *testing.T) {
	t.Chdir(t.TempDir())
	cfg := pricedTestConfig("")
	var usage sessionUsage
	usage.add(usageEvent{Model: cfg.ModelReference(), Usage: tokenUsage{Input: 2000, Output: 1000}}, cfg)
	if usage.Cost != .008 {
		t.Fatalf("unexpected cost: %f", usage.Cost)
	}
	if err := saveMemoryWithUsage("usage", []message{{Role: "user", Content: "hi"}}, usage); err != nil {
		t.Fatal(err)
	}
	if got := loadSessionUsage("usage"); got.Input != 2000 || got.Cost != usage.Cost {
		t.Fatalf("usage not saved: %+v", got)
	}
	usage.add(usageEvent{Model: "other/model", Usage: tokenUsage{Input: 1}}, cfg)
	if !strings.Contains(usage.summary(cfg), "cost: ?") {
		t.Fatal("unpriced usage must not be reported as a complete cost")
	}
	usage.add(usageEvent{Model: cfg.ModelReference(), Usage: tokenUsage{Input: 10}, Child: true}, cfg)
	if usage.LastInput != 1 {
		t.Fatal("subagent input must not replace the main context count")
	}
}
