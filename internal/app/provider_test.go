package app

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func writeProviderTestConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hanoi.jsonc")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderSelectionAndRequests(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			requests := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request chatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Model != "vendor/actual-model" || r.URL.Path != "/v1/chat/completions" || r.URL.Query().Get("version") != "1" || r.Header.Get("X-Provider") != "second" || r.Header.Get("Authorization") != "" {
					t.Errorf("wrong provider request: model=%s path=%s headers=%v", request.Model, r.URL, r.Header)
				}
				if stream {
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
				} else {
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
				}
			}))
			defer api.Close()
			t.Setenv("HANOI_PROVIDER_URL", api.URL+"/v1/?version=1")
			path := writeProviderTestConfig(t, `{
				// OpenCode-compatible JSONC with unused credentials and a model alias.
				"model": "first/default",
				"provider": {
					"first": {"options":{"baseURL":"https://example.test/v1","apiKey":"{env:HANOI_UNUSED_PROVIDER_KEY}"},"models":{"default":{}}},
					"second": {"npm":"@ai-sdk/openai-compatible","name":"Local","options":{"baseURL":"{env:HANOI_PROVIDER_URL}","headers":{"X-Provider":"second"}},"models":{"vendor/alias":{"id":"vendor/actual-model","name":"Alias"}}},
				},
			}`)
			cfg, err := loadConfig(path, "second/vendor/alias")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ModelReference() != "second/vendor/alias" {
				t.Fatal("lost model alias")
			}
			// Simulate switching away from a provider with credentials and custom headers.
			cfg.APIKey, cfg.Headers = "previous-secret", map[string]string{"Authorization": "previous-secret"}
			cfg, err = cfg.selectModel("vendor/alias")
			if err != nil {
				t.Fatal(err)
			}
			var text string
			if stream {
				text, err = sendStreamingChat(cfg, []message{{Role: "user", Content: "hello"}}, nil)
			} else {
				text, err = sendChat(cfg, []message{{Role: "user", Content: "hello"}}, false)
			}
			if err != nil || text != "hello" || requests != 1 {
				t.Fatalf("text=%q requests=%d err=%v", text, requests, err)
			}
			if _, err := cfg.selectModel("first/default"); err == nil {
				t.Fatal("missing selected provider variable was ignored")
			}
		})
	}
}

func TestProviderConfigDefaultsAndErrors(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "environment-key")
	for _, tc := range []struct {
		name, data, selection, want string
	}{
		{"single model", `{"provider":{"openai":{"models":{"mini":{}}}}}`, "", ""},
		{"default selection", `{"model":"openai/mini","provider":{"openai":{"models":{"mini":{},"other":{}}}}}`, "", ""},
		{"ambiguous default", `{"provider":{"openai":{"models":{"mini":{},"other":{}}}}}`, "", "set model"},
		{"unknown provider", `{"provider":{"openai":{"models":{"mini":{}}}}}`, "missing/mini", "unknown provider"},
		{"unknown model", `{"provider":{"openai":{"models":{"mini":{}}}}}`, "openai/missing", "unknown model"},
		{"invalid URL", `{"provider":{"custom":{"options":{"baseURL":"file:///tmp/model"},"models":{"mini":{}}}}}`, "", "baseURL must"},
		{"unsupported adapter", `{"provider":{"anthropic":{"npm":"@ai-sdk/anthropic","models":{"mini":{}}}}}`, "", "unsupported adapter"},
		{"empty ID", `{"provider":{"":{"models":{"mini":{}}}}}`, "", "provider ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadConfig(writeProviderTestConfig(t, tc.data), tc.selection)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("expected %q, got %v", tc.want, err)
				}
				return
			}
			if err != nil || cfg.APIKey != "environment-key" || cfg.BaseURL != "https://api.openai.com/v1/chat/completions" || cfg.Model != "mini" {
				t.Fatalf("incorrect OpenAI defaults: model=%s url=%s err=%v", cfg.Model, cfg.BaseURL, err)
			}
		})
	}
}

func TestProviderLegacyConfig(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "legacy-key")
	t.Setenv("OPENAI_MODEL", "legacy-model")
	t.Setenv("OPENAI_BASE_URL", "https://example.test/custom/completions")
	cfg, err := loadConfig(writeProviderTestConfig(t, `{"model":"ignored/model","mcp":{}}`), "")
	if err != nil || cfg.Model != "legacy-model" || cfg.APIKey != "legacy-key" || cfg.BaseURL != "https://example.test/custom/completions" {
		t.Fatalf("legacy configuration changed: model=%s url=%s err=%v", cfg.Model, cfg.BaseURL, err)
	}
	cfg, err = cfg.selectModel("vendor/legacy-model")
	if err != nil || cfg.Model != "vendor/legacy-model" {
		t.Fatal("legacy model switching changed")
	}
}

func TestProviderSwitchAuthentication(t *testing.T) {
	t.Setenv("HANOI_PROVIDER_KEY", "provider-key")
	t.Setenv("HANOI_PROVIDER_HEADER", "Bearer header-key")
	requests := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		want := "Bearer provider-key"
		if r.URL.Path == "/second/chat/completions" {
			want = "Bearer header-key"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("wrong provider credentials")
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer api.Close()
	t.Setenv("HANOI_PROVIDER_TEST_URL", api.URL)
	path := writeProviderTestConfig(t, `{"model":"first/a","provider":{
		"first":{"options":{"baseURL":"{env:HANOI_PROVIDER_TEST_URL}/first","apiKey":"{env:HANOI_PROVIDER_KEY}"},"models":{"a":{}}},
		"second":{"options":{"baseURL":"{env:HANOI_PROVIDER_TEST_URL}/second/chat/completions","headers":{"Authorization":"{env:HANOI_PROVIDER_HEADER}"}},"models":{"b":{}}}
	}}`)
	cfg, err := loadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []string{"first/a", "second/b", "first/a"} {
		cfg, err = cfg.selectModel(selection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := sendChat(cfg, []message{{Role: "user", Content: "hello"}}, false); err != nil {
			t.Fatal(err)
		}
	}
	if requests != 3 {
		t.Fatalf("expected three routed requests, got %d", requests)
	}
}

func TestProviderMenu(t *testing.T) {
	path := writeProviderTestConfig(t, `{"model":"local/a","provider":{"local":{"name":"Local","options":{"baseURL":"http://localhost:11434/v1"},"models":{"a":{},"b":{"name":"Model B"}}}}}`)
	cfg, err := loadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	logger, manager := log.New(io.Discard, "", 0), &mcpManager{}
	cfg.Logger, cfg.MCP = logger, manager
	m := tuiModel{cfg: cfg, textarea: textarea.New(), messages: []message{{Role: "user", Content: "keep me"}}}
	updated, _ := m.handleSlashCommand("/providers")
	m = updated.(tuiModel)
	if !m.providersOpen || !strings.Contains(m.providerView(), "local/a · Local (active)") {
		t.Fatal("provider menu missing active model")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.providersOpen || m.cfg.Model != "b" || len(m.messages) != 1 {
		t.Fatal("menu selection did not switch model or preserve conversation")
	}
	if m.cfg.Logger != logger || m.cfg.MCP != manager {
		t.Fatal("model selection lost application runtime services")
	}
	updated, _ = m.handleSlashCommand("/model local/missing")
	m = updated.(tuiModel)
	if m.err == "" || m.cfg.Model != "b" {
		t.Fatal("invalid selection changed active model")
	}
}
