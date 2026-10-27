package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	appconfig "hanoi/internal/config"
)

func TestEscapeStopAndContinue(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprintf("combined=%v", combined), func(t *testing.T) {
			m := newToolTestModel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.chatCancel = cancel
			m.messages = []message{{Role: "user", Content: "first"}}
			m.streaming = "partial answer"
			m.traceOpen = true
			if !combined {
				m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
				if ctx.Err() != nil || m.traceOpen {
					t.Fatal("first escape must close trace without stopping")
				}
			}
			m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc, Alt: combined})
			if ctx.Err() != context.Canceled || !m.cancelling || !m.loading {
				t.Fatal("double escape must cancel and wait for the worker to stop")
			}
			m = updateModel(m, chatStatusMsg("running tool: stale"))
			m = updateModel(m, chatChunkMsg{text: "stale"})
			if m.activity != "stopping..." || m.streaming != "partial answer" {
				t.Fatal("in-flight updates must not overwrite the stopping state")
			}
			m = updateModel(m, chatResultMsg{err: context.Canceled})
			if m.loading || m.chatCancel != nil || m.err != "Stopped." || len(m.messages) != 2 || m.messages[1].Content != "partial answer" {
				t.Fatalf("cancelled turn was not preserved: %+v", m.messages)
			}
			m.textarea.SetValue("continue")
			updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			m = updated.(tuiModel)
			defer m.chatCancel()
			if !m.loading || m.cancelling || m.err != "" || cmd == nil || len(m.messages) != 3 {
				t.Fatal("must accept a new turn after cancellation")
			}
		})
	}
}

func TestEscapeStopRequiresConsecutivePresses(t *testing.T) {
	m := newToolTestModel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.chatCancel = cancel
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	m.lastEscape = time.Now().Add(-2 * time.Second)
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() != nil {
		t.Fatal("escapes more than one second apart must not stop")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc})
	if ctx.Err() != nil {
		t.Fatal("another key must reset the double escape")
	}
}

func TestModelRequestCancellation(t *testing.T) {
	for _, endpoint := range []string{"/chat/completions", "/responses"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", endpoint, stream), func(t *testing.T) {
				started := make(chan struct{})
				stopped := make(chan struct{})
				release := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(started)
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						w.WriteHeader(http.StatusOK)
						w.(http.Flusher).Flush()
					}
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-release:
					}
				}))
				defer server.Close()
				defer close(release)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cfg := config{Config: appconfig.Config{BaseURL: server.URL + endpoint, Model: "test"}, Context: ctx}
				done := make(chan error, 1)
				go func() {
					var err error
					if stream {
						_, err = sendStreamingChat(cfg, nil, nil)
					} else {
						_, err = sendChat(cfg, nil, false)
					}
					done <- err
				}()
				select {
				case <-started:
				case <-time.After(3 * time.Second):
					t.Fatal("request did not start")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("expected cancellation, got %v", err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("model request did not stop")
				}
				select {
				case <-stopped:
				case <-time.After(3 * time.Second):
					t.Fatal("HTTP request remained open")
				}
			})
		}
	}
}

func TestBashTurnCancellationStopsRemainingTools(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	cfg := config{Context: ctx, OnStatus: func(string) { close(started) }}
	first := mkCall("bash", `{"command":"sleep 30 | cat"}`)
	first.Type = "function"
	second := mkCall("bash", `{"command":"exit 0"}`)
	second.Type = "function"
	done := make(chan error, 1)
	go func() {
		_, err := executeToolCalls([]toolCall{first, second}, cfg)
		done <- err
	}()
	<-started
	// Allow Bash to start, so this exercises active process cancellation.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Bash turn did not stop")
	}
}

func TestMCPToolCancellation(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	fixture := mcp.NewServer(&mcp.Implementation{Name: "cancel-test", Version: "1"}, nil)
	fixture.AddTool(&mcp.Tool{Name: "wait", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	server := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "config.json")
	writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: server.URL})
	manager, err := newMCPManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	call := mkCall(mcpToolName("test", "wait"), `{}`)
	call.Type = "function"
	done := make(chan error, 1)
	go func() {
		_, err := executeToolCalls([]toolCall{call}, config{MCP: manager, Context: ctx})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP call did not start: " + strings.Join(manager.rows(), ","))
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MCP call did not stop")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("MCP server did not receive cancellation")
	}
}
