package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	appconfig "hanoi/internal/config"
)

func TestDoctorLocalMakesNoProviderRequest(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("local doctor must not contact provider")
	}))
	defer server.Close()
	cfg := config{Config: appconfig.Config{Model: "test-model", BaseURL: server.URL, APIKey: "secret"}, ConfigPath: "hanoi.json", LogPath: "api.log", TracePath: "trace.jsonl"}
	text := string(doctorCmd(cfg, false)().(doctorResultMsg))
	for _, want := range []string{"test-model", "hanoi.json", "api.log", "trace.jsonl", "[SKIP] Provider connection", "not initialized"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
	if strings.Contains(text, "secret") {
		t.Fatal("API key leaked")
	}
}

func TestDoctorProviderProbe(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, want string
	}{
		{200, `{"choices":[{"message":{"content":"OK"}}]}`, "OK"},
		{401, `{"error":"private-secret"}`, "FAIL"},
		{200, `{}`, "FAIL"},
	} {
		t.Run(fmt.Sprint(tc.status, tc.body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Test") != "custom" {
					t.Error("provider authentication headers missing")
				}
				var req chatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "test" || len(req.Tools) != 0 || req.Stream {
					t.Errorf("invalid tool-free probe: %+v, %v", req, err)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			check := probeProvider(config{Config: appconfig.Config{Model: "test", BaseURL: server.URL, APIKey: "secret", Headers: map[string]string{"X-Test": "custom"}}})
			if check.Status != tc.want || strings.Contains(check.Detail, "secret") {
				t.Fatalf("unexpected result: %+v", check)
			}
		})
	}
}

func TestDoctorViewRestoresChat(t *testing.T) {
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10), history: []string{strings.Repeat("chat line\n", 80)}}
	m.resizeChat()
	m.refreshChat()
	m.viewport.SetYOffset(10)
	updated, cmd := m.handleSlashCommand("/doctor")
	m = updated.(tuiModel)
	if cmd == nil || !m.doctorBusy || !m.doctorOpen {
		t.Fatal("doctor must start asynchronously")
	}
	updated, _ = m.Update(doctorResultMsg("[OK] Model\n  test"))
	m = updated.(tuiModel)
	if m.doctorBusy || !strings.Contains(m.View(), "[OK] Model") || len(m.messages) != 0 {
		t.Fatal("diagnostics must be visible without entering model conversation")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.doctorOpen || m.viewport.YOffset != 10 {
		t.Fatal("chat scroll position must be restored")
	}
}

func TestDoctorMCPStates(t *testing.T) {
	manager := &mcpManager{servers: map[string]*mcpServerState{
		"available": {status: "connected", session: &mcp.ClientSession{}, tools: []*mcp.Tool{{Name: "lookup"}}},
		"disabled":  {status: "disabled", config: mcpServerConfig{Disabled: true}},
		"failed":    {status: "disconnected", err: "secret connection URL"},
	}}
	checks := doctorMCPChecks(manager)
	if len(checks) != 3 || checks[0].Status != "OK" || checks[1].Status != "SKIP" || checks[2].Status != "WARN" {
		t.Fatalf("incorrect MCP states: %+v", checks)
	}
	if !strings.Contains(checks[0].Detail, mcpToolName("available", "lookup")) || strings.Contains(checks[2].Detail, "secret") {
		t.Fatalf("incorrect MCP details: %+v", checks)
	}
}
