package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	appconfig "hanoi/internal/config"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

type copilotTestTransport func(*http.Request) (*http.Response, error)

func (f copilotTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func isolatedCopilotStore(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("APPDATA", dir)
}

func TestCopilotDeviceLogin(t *testing.T) {
	isolatedCopilotStore(t)
	previous := http.DefaultClient
	defer func() { http.DefaultClient = previous }()
	polls := 0
	http.DefaultClient = &http.Client{Transport: copilotTestTransport(func(r *http.Request) (*http.Response, error) {
		var body map[string]string
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		result := ""
		switch r.URL.String() {
		case "https://github.com/login/device/code":
			if body["client_id"] != copilotClientID || body["scope"] != "read:user" {
				t.Error("incorrect device request")
			}
			result = `{"device_code":"private-device","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":60,"interval":1}`
		case "https://github.com/login/oauth/access_token":
			polls++
			if body["device_code"] != "private-device" || body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Error("incorrect token request")
			}
			result = `{"error":"authorization_pending"}`
			if polls == 2 {
				result = `{"access_token":"private-github-token"}`
			}
		case "https://api.github.com/copilot_internal/user":
			if r.Header.Get("Authorization") != "Bearer private-github-token" {
				t.Error("missing user authentication")
			}
			result = `{"chat_enabled":true,"endpoints":{"api":"https://api.business.githubcopilot.com"}}`
		default:
			t.Fatalf("unexpected OAuth endpoint: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header)}, nil
	})}
	device, err := startCopilotLogin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := finishCopilotLogin(context.Background(), device); err != nil {
		t.Fatal(err)
	}
	session, err := loadCopilotSession()
	if err != nil || session.AccessToken != "private-github-token" || session.APIEndpoint != "https://api.business.githubcopilot.com" || polls != 2 {
		t.Fatalf("session not persisted correctly: %v", err)
	}
	path, _ := copilotSessionPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file permissions: %v", info.Mode())
	}
	if err := logoutCopilot(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCopilotSession(); err == nil {
		t.Fatal("logout retained session")
	}
}

func TestCopilotLoginFailures(t *testing.T) {
	for _, outcome := range []string{"access_denied", "expired_token", "no_entitlement", "bad_endpoint", "transport"} {
		t.Run(outcome, func(t *testing.T) {
			isolatedCopilotStore(t)
			previous := http.DefaultClient
			defer func() { http.DefaultClient = previous }()
			http.DefaultClient = &http.Client{Transport: copilotTestTransport(func(r *http.Request) (*http.Response, error) {
				if outcome == "transport" {
					return nil, errors.New("secret response private-device private-token")
				}
				body := `{"error":"` + outcome + `"}`
				if outcome == "no_entitlement" || outcome == "bad_endpoint" {
					body = `{"access_token":"private-token"}`
					if r.Method == http.MethodGet {
						body = `{"chat_enabled":false}`
						if outcome == "bad_endpoint" {
							body = `{"endpoints":{"api":"https://evil.example"}}`
						}
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			err := finishCopilotLogin(context.Background(), copilotDevice{DeviceCode: "private-device", ExpiresIn: 30, Interval: 1})
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe or missing error: %v", err)
			}
			if _, err := loadCopilotSession(); err == nil {
				t.Fatal("failed login saved a token")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := finishCopilotLogin(ctx, copilotDevice{ExpiresIn: 60, Interval: 30}); !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("login cancellation failed: %v", err)
	}
}

func TestCopilotRequests(t *testing.T) {
	isolatedCopilotStore(t)
	if err := saveCopilotSession(copilotSession{AccessToken: "private-copilot-token"}); err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, model := range []string{"gpt-4.1", "gpt-6.1-sol"} {
			t.Run(model+map[bool]string{true: "/stream", false: "/normal"}[stream], func(t *testing.T) {
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var request struct {
						Stream bool `json:"stream"`
					}
					_ = json.NewDecoder(r.Body).Decode(&request)
					if r.Header.Get("Authorization") != "Bearer private-copilot-token" || r.Header.Get("X-Initiator") != "user" || r.Header.Get("X-GitHub-Api-Version") != "2026-08-01" || r.Header.Get("Openai-Intent") != "conversation-edits" {
						t.Error("incorrect Copilot headers")
					}
					responses := model == "gpt-6.1-sol"
					want := "/chat/completions"
					if responses {
						want = "/responses"
					}
					if r.URL.Path != want {
						t.Errorf("route=%s want=%s", r.URL.Path, want)
					}
					if responses {
						if request.Stream {
							io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}]}}\n\n")
						} else {
							io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}]}`)
						}
					} else if request.Stream {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`)
					}
				}))
				defer api.Close()
				data, _ := json.Marshal(map[string]any{"model": "github-copilot/" + model, "provider": map[string]any{"github-copilot": map[string]any{"options": map[string]string{"baseURL": api.URL}}}})
				cfg, err := loadConfig(writeProviderTestConfig(t, string(data)), "")
				if err != nil {
					t.Fatal(err)
				}
				var logs bytes.Buffer
				cfg.Logger = log.New(&logs, "", 0)
				var text string
				if stream {
					text, err = sendStreamingChat(cfg, []message{{Role: "user", Content: "hello"}}, nil)
				} else {
					text, err = sendChat(cfg, []message{{Role: "user", Content: "hello"}}, false)
				}
				if err != nil || text != "hello" {
					t.Fatalf("text=%q err=%v", text, err)
				}
				if strings.Contains(logs.String(), "private-copilot-token") {
					t.Fatal("token leaked to log")
				}
				if check := probeProvider(cfg); check.Status != "OK" {
					t.Fatalf("doctor: %+v", check)
				}
			})
		}
	}
}

func TestCopilotRequestRoutingAndLogout(t *testing.T) {
	isolatedCopilotStore(t)
	session := copilotSession{AccessToken: "token", APIEndpoint: "https://api.business.githubcopilot.com"}
	if err := saveCopilotSession(session); err != nil {
		t.Fatal(err)
	}
	cfg := config{Config: appconfig.Config{Provider: "github-copilot"}}
	for payload, want := range map[string]string{
		`{"messages":[{"role":"user"}]}`:                                      "user",
		`{"messages":[{"role":"user"},{"role":"assistant"},{"role":"tool"}]}`: "agent",
		`{"input":[{"role":"user"},{"type":"function_call_output"}]}`:         "agent",
		`{"input":[{"role":"user"}]}`:                                         "user",
	} {
		req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/responses", nil)
		if err := authorizeProviderRequest(cfg, req, []byte(payload)); err != nil {
			t.Fatal(err)
		}
		if req.URL.Host != "api.business.githubcopilot.com" || req.URL.Path != "/responses" || req.Header.Get("X-Initiator") != want {
			t.Error("wrong endpoint or initiator")
		}
	}
	if err := logoutCopilot(); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/responses", nil)
	if err := authorizeProviderRequest(cfg, req, nil); err == nil {
		t.Fatal("request allowed after logout")
	}
}

func TestCopilotTUIAuth(t *testing.T) {
	m := tuiModel{textarea: textarea.New()}
	updated, cmd := m.handleSlashCommand("/connect github-copilot")
	m = updated.(tuiModel)
	defer m.providerAuthCancel()
	if cmd == nil || !m.providersOpen || !strings.Contains(m.providerView(), "Requesting GitHub") {
		t.Fatal("missing login UI")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	updated, _ = m.Update(copilotLoginFinishedMsg{err: context.Canceled})
	m = updated.(tuiModel)
	if m.providerAuthCancel != nil || !strings.Contains(m.providerView(), "canceled") {
		t.Fatal("login cancellation did not finish")
	}
}

func TestCopilotBuiltInConfiguration(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	for _, tc := range []struct{ model, endpoint string }{
		{"gpt-4.1", "/chat/completions"},
		{"gpt-5-mini", "/chat/completions"},
		{"gpt-6.1-sol", "/responses"},
		{"grok-code-fast-1", "/responses"},
		{"gemini-2.5-pro", "/chat/completions"},
	} {
		cfg, err := loadConfig(writeProviderTestConfig(t, `{}`), "github-copilot/"+tc.model)
		if err != nil || cfg.BaseURL != "https://api.githubcopilot.com"+tc.endpoint || cfg.APIKey != "" {
			t.Fatalf("model=%s endpoint=%s err=%v", tc.model, cfg.BaseURL, err)
		}
	}
	for _, endpoint := range []string{"chat/completions", "responses"} {
		path := writeProviderTestConfig(t, `{"model":"github-copilot/gpt-6.1-sol","provider":{"github-copilot":{"options":{"baseURL":"https://api.githubcopilot.com/`+endpoint+`"}}}}`)
		cfg, err := loadConfig(path, "")
		if err != nil || cfg.BaseURL != "https://api.githubcopilot.com/"+endpoint {
			t.Fatalf("explicit endpoint overridden: %s %v", cfg.BaseURL, err)
		}
	}
	// Switching from Copilot clears OAuth behavior and restores the other provider.
	path := writeProviderTestConfig(t, `{"model":"local/model","provider":{"local":{"options":{"baseURL":"http://localhost:11434/v1"},"models":{"model":{}}}}}`)
	cfg, err := loadConfig(path, "github-copilot/gpt-4.1")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err = cfg.selectModel("local/model")
	if err != nil || cfg.Provider != "local" || cfg.BaseURL != "http://localhost:11434/v1/chat/completions" {
		t.Fatalf("switch: %s %v", cfg.Provider, err)
	}
}
