package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	appconfig "hanoi/internal/config"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func writeMCPTestConfig(t *testing.T, path string, entry mcpServerConfig) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"mcp": map[string]any{"servers": map[string]any{"test": entry}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPConfigFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hanoi.jsonc")
	data := `{
		"$schema": "https://opencode.ai/config.json",
		// unrelated OpenCode configuration is ignored
		"model": "openai/gpt-4o-mini",
		"mcp": {
			"timeout": {"startup": 1234, "execution": 5678},
			"old": {"type": "local", "command": ["server"], "enabled": false, "timeout": 900},
			"servers": {
				"new": {"type": "remote", "url": "https://example.test/mcp", "headers": {"Authorization": "Bearer {env:HANOI_MCP_TOKEN}"}, "timeout": {"catalog": 2345}},
			},
		},
	}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, err := loadMCPConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || !servers["old"].Disabled || servers["old"].Timeout != (mcpTimeouts{900, 900, 900}) || servers["new"].Timeout != (mcpTimeouts{1234, 2345, 5678}) {
		t.Fatalf("incorrect normalization: %#v", servers)
	}
	t.Setenv("HANOI_MCP_TOKEN", "secret")
	expanded, err := expandMCPServer(servers["new"])
	if err != nil || expanded.Headers["Authorization"] != "Bearer secret" {
		t.Fatalf("env expansion failed: %#v %v", expanded, err)
	}
	if servers["new"].Headers["Authorization"] != "Bearer {env:HANOI_MCP_TOKEN}" {
		t.Fatal("expansion modified original config")
	}
	if _, err := appconfig.ExpandEnv("{env:HANOI_MISSING_MCP_TEST_ENV}"); err == nil {
		t.Fatal("expected missing env error")
	}
	for _, invalid := range []string{
		`{"mcp":{"servers":{"a":{"type":"local","command":[]}}}}`,
		`{"mcp":{"servers":{"a":{"type":"remote","url":"https://example.test","timeout":{"startup":0}}}}}`,
		`{"mcp":{"servers":{"a":{"type":"remote","url":"https://example.test","timeout":-1}}}}`,
	} {
		os.WriteFile(path, []byte(invalid), 0o600)
		if _, err := loadMCPConfig(path); err == nil {
			t.Fatalf("expected invalid config error: %s", invalid)
		}
	}
}

func newMCPTestServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	server.AddTool(&mcp.Tool{Name: "echo", Description: "Echo a message", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}, StructuredContent: map[string]any{"echo": args.Text}}, nil
	})
	server.AddTool(&mcp.Tool{Name: "fail", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "fixture tool failed"}}}, nil
	})
	return server
}

func TestMCPRemoteTransports(t *testing.T) {
	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			fixture := newMCPTestServer()
			var handler http.Handler
			if transport == "http" {
				handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil)
			} else {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return fixture }, nil)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer remote-secret" {
					http.Error(w, "unauthorized", 401)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			t.Setenv("HANOI_REMOTE_TOKEN", "remote-secret")
			path := filepath.Join(t.TempDir(), "config.json")
			writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer {env:HANOI_REMOTE_TOKEN}"}})
			var logs bytes.Buffer
			manager, err := newMCPManager(path, log.New(&logs, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if tools := manager.Tools(); len(tools) != 2 {
				t.Fatalf("expected 2 paginated tools, got %d: %v", len(tools), manager.rows())
			}
			result, err := manager.Call(mcpToolName("test", "echo"), `{"text":"hello MCP"}`)
			if err != nil || !strings.Contains(result, "hello MCP") || !strings.Contains(result, "structuredContent") {
				t.Fatalf("result=%s err=%v", result, err)
			}
			result, err = manager.Call(mcpToolName("test", "fail"), `{}`)
			if err != nil || !strings.Contains(result, `"isError":true`) {
				t.Fatalf("lost tool error: %s %v", result, err)
			}
			if strings.Contains(logs.String(), "remote-secret") {
				t.Fatal("authentication header leaked into log")
			}
			if err := manager.Disable("test"); err != nil {
				t.Fatal(err)
			}
			if len(manager.Tools()) != 0 {
				t.Fatal("disabled tools still advertised")
			}
			if _, err := manager.Call(mcpToolName("test", "echo"), `{}`); err == nil {
				t.Fatal("disabled tool executed")
			}
			if err := manager.Enable("test"); err != nil {
				t.Fatal(err)
			}
			if len(manager.Tools()) != 2 {
				t.Fatal("tools not restored")
			}
			os.WriteFile(path, []byte(`{"mcp":`), 0o600)
			if err := manager.Reload(); err == nil || len(manager.Tools()) != 2 {
				t.Fatal("invalid reload should preserve existing connections")
			}
		})
	}
}

func TestMCPChatIntegration(t *testing.T) {
	fixture := newMCPTestServer()
	mcpHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil))
	defer mcpHTTP.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: mcpHTTP.URL})
	manager, err := newMCPManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			requests := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				var request chatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if len(request.Tools) != 3 {
					t.Errorf("expected bash + 2 MCP tools, got %d", len(request.Tools))
				}
				var response message
				if requests == 1 {
					call := toolCall{ID: "call_mcp", Type: "function"}
					call.Function.Name, call.Function.Arguments = mcpToolName("test", "echo"), `{"text":"model input"}`
					response = message{Role: "assistant", ToolCalls: []toolCall{call}}
				} else {
					if len(request.Messages) != 3 || request.Messages[2].ToolCallID != "call_mcp" || !strings.Contains(request.Messages[2].Content, "model input") {
						t.Error("missing MCP tool result")
					}
					response = message{Role: "assistant", Content: "MCP worked"}
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					if requests == 1 {
						call := response.ToolCalls[0]
						writeStreamChoice(w, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": call.ID, "type": call.Type, "function": call.Function}}}, nil)
						writeStreamChoice(w, map[string]any{}, "tool_calls")
					} else {
						writeStreamChoice(w, map[string]any{"content": response.Content}, "stop")
					}
					io.WriteString(w, "data: [DONE]\n\n")
				} else {
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": response}}})
				}
			}))
			defer api.Close()
			cfg := config{Config: appconfig.Config{APIKey: "test", Model: "test", BaseURL: api.URL}, MCP: manager}
			var text string
			var err error
			if stream {
				text, err = sendStreamingChat(cfg, []message{{Role: "user", Content: "use MCP"}}, nil)
			} else {
				text, err = sendChat(cfg, []message{{Role: "user", Content: "use MCP"}}, false)
			}
			if err != nil || text != "MCP worked" || requests != 2 {
				t.Fatalf("text=%q requests=%d err=%v", text, requests, err)
			}
		})
	}
}

func TestMCPLocalHelper(t *testing.T) {
	if os.Getenv("HANOI_MCP_HELPER") != "1" {
		return
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "local-fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "context", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		cwd, _ := os.Getwd()
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: os.Getenv("HANOI_MCP_VALUE") + "|" + cwd}}}, nil
	})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestMCPLocalLifecycleAndMenu(t *testing.T) {
	dir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HANOI_MCP_VALUE", "parent")
	path := filepath.Join(dir, "hanoi.json")
	entry := mcpServerConfig{Type: "local", Command: []string{executable, "-test.run=^TestMCPLocalHelper$"}, CWD: dir, Environment: map[string]string{"HANOI_MCP_HELPER": "1", "HANOI_MCP_VALUE": "child"}, Disabled: true}
	writeMCPTestConfig(t, path, entry)
	manager, err := newMCPManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.Tools()) != 0 || !strings.Contains(manager.rows()[0], "disabled") {
		t.Fatal("disabled server started")
	}
	model, cmd := (tuiModel{cfg: config{MCP: manager}, textarea: textarea.New()}).handleSlashCommand("/mcps enable test")
	m := model.(tuiModel)
	if !m.mcpOpen || !m.mcpBusy || cmd == nil {
		t.Fatal("/mcps did not start asynchronous action")
	}
	batch := cmd().(tea.BatchMsg)
	for _, command := range batch {
		if result, ok := command().(mcpResultMsg); ok {
			if result.err != nil {
				t.Fatal(result.err)
			}
			updated, _ := m.Update(result)
			m = updated.(tuiModel)
		}
	}
	if m.mcpBusy || !strings.Contains(m.mcpView(), "connected") {
		t.Fatalf("menu not updated: %s", m.mcpView())
	}
	result, err := manager.Call(mcpToolName("test", "context"), `{}`)
	if err != nil || !strings.Contains(result, "child|"+dir) {
		t.Fatalf("local environment/cwd failed: %s %v", result, err)
	}
	manager.mu.RLock()
	session := manager.servers["test"].session
	manager.mu.RUnlock()
	if err := manager.Disable("test"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { session.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("local process not closed")
	}
	entry.Disabled = false
	writeMCPTestConfig(t, path, entry)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(manager.Tools()) != 1 {
		t.Fatalf("reload did not re-enable server: %v", manager.rows())
	}
	m.mcpCursor = 100
	model, _ = m.updateMCPMenu(tea.KeyMsg{Type: tea.KeyEsc})
	if model.(tuiModel).mcpOpen {
		t.Fatal("Esc did not close menu")
	}
}

func TestMCPToolNames(t *testing.T) {
	a, b := mcpToolName("a.b", strings.Repeat("tool", 30)), mcpToolName("a/b", strings.Repeat("tool", 30))
	if a == b || len(a) > 64 || !regexp.MustCompile(`^[a-zA-Z0-9_-]+$`).MatchString(a) {
		t.Fatalf("invalid/colliding tool names: %q %q", a, b)
	}
}

func TestMCPTimeoutAndFailureIsolation(t *testing.T) {
	fixture := newMCPTestServer()
	fixture.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
			return &mcp.CallToolResult{}, nil
		}
	})
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"mcp": map[string]any{"servers": map[string]any{
		"good":   mcpServerConfig{Type: "remote", URL: server.URL, Timeout: mcpTimeouts{Execution: 30}},
		"broken": mcpServerConfig{Type: "local", Command: []string{"/does-not-exist/hanoi-mcp-test"}},
	}}})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := newMCPManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if len(manager.Tools()) != 3 || !strings.Contains(strings.Join(manager.rows(), "\n"), "broken · local · error") {
		t.Fatalf("failure was not isolated: %v", manager.rows())
	}
	start := time.Now()
	call := toolCall{ID: "call_slow", Type: "function"}
	call.Function.Name, call.Function.Arguments = mcpToolName("good", "slow"), `{}`
	results, err := executeToolCalls([]toolCall{call}, config{MCP: manager})
	if err != nil || len(results) != 1 || !strings.Contains(results[0].Content, "deadline exceeded") || time.Since(start) > time.Second {
		t.Fatalf("timeout not returned to model: %#v %v", results, err)
	}
}

func TestMCPProtocolNegotiation(t *testing.T) {
	fixture := newMCPTestServer()
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer server.Close()
	for _, protocol := range []string{"legacy", "auto", "2026-07-28"} {
		t.Run(protocol, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: server.URL, Protocol: protocol})
			manager, err := newMCPManager(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if len(manager.Tools()) != 2 {
				t.Fatalf("protocol failed: %v", manager.rows())
			}
			if _, err := manager.Call(mcpToolName("test", "echo"), `{"text":"negotiated"}`); err != nil {
				t.Fatal(err)
			}
		})
	}
}
