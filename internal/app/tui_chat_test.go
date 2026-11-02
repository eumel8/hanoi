package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestInputHistory(t *testing.T) {
	m := tuiModel{textarea: textarea.New()}
	m.rememberInput("first prompt")
	m.rememberInput("/models")
	m.rememberInput("/models")
	m.textarea.SetValue("unfinished draft")
	for _, step := range []struct{ key, want string }{
		{"alt+up", "/models"},
		{"alt+up", "first prompt"},
		{"alt+up", "first prompt"},
		{"alt+down", "/models"},
		{"alt+down", "unfinished draft"},
	} {
		if !m.recallInput(step.key) || m.textarea.Value() != step.want {
			t.Fatalf("key %s: got %q, want %q", step.key, m.textarea.Value(), step.want)
		}
	}
	if len(m.inputHistory) != 2 {
		t.Fatal("consecutive duplicates must not create history entries")
	}
}

func TestSlashCommandCompletion(t *testing.T) {
	m := tuiModel{width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	m.textarea.SetValue("/mo")
	if got := m.slashSuggestions(); len(got) != 2 || got[0] != "/model" || got[1] != "/models" {
		t.Fatalf("suggestions for /mo: %v", got)
	}
	if view := m.View(); !strings.Contains(view, "/model") || !strings.Contains(view, "/models") {
		t.Fatalf("matching commands should appear in the TUI: %q", view)
	}
	for _, want := range []string{"/model", "/models", "/model"} {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m = updated.(tuiModel)
		if got := m.textarea.Value(); got != want {
			t.Fatalf("Tab completed to %q, want %q", got, want)
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m = updated.(tuiModel)
	if got := m.textarea.Value(); got != "/models" {
		t.Fatalf("Shift+Tab completed to %q, want /models", got)
	}
	m.textarea.Focus()
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = updated.(tuiModel)
	if m.completionPrefix != "" || len(m.slashSuggestions()) != 0 {
		t.Fatalf("editing must reset completion and hide stale matches: %q, prefix %q", m.textarea.Value(), m.completionPrefix)
	}
}

func TestSlashCommandCompletionOnlyForCommandName(t *testing.T) {
	m := tuiModel{textarea: textarea.New()}
	for _, input := range []string{"hello /mo", "/model local/mo", "/mo\nnext"} {
		m.textarea.SetValue(input)
		if len(m.slashSuggestions()) != 0 || m.completeSlashCommand(false) {
			t.Fatalf("unexpected command completion for %q", input)
		}
		if m.textarea.Value() != input {
			t.Fatalf("completion changed %q", input)
		}
	}
	m.textarea.SetValue("/m")
	if got := m.slashSuggestions(); len(got) != 3 || got[0] != "/mcps" || got[1] != "/model" || got[2] != "/models" {
		t.Fatalf("suggestions for /m: %v", got)
	}
}

func TestChatScrollDuringStreaming(t *testing.T) {
	m := tuiModel{
		width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10),
		history: []string{strings.Repeat("chat line\n", 100)}, loading: true,
	}
	m.textarea.SetHeight(4)
	m.resizeChat()
	m.refreshChat()
	if !m.viewport.AtBottom() {
		t.Fatal("new chat content must follow the bottom")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(tuiModel)
	offset := m.viewport.YOffset
	if m.viewport.AtBottom() {
		t.Fatal("page up must work while streaming")
	}
	m.streaming = "new streamed answer"
	m.streamRendered = m.streaming
	m.refreshChat()
	if m.viewport.YOffset != offset {
		t.Fatal("new tokens must not jump a scrolled chat to the bottom")
	}
	if !strings.Contains(m.chatViewport(), "┃") {
		t.Fatal("scrollbar thumb missing")
	}
	if lipgloss.Height(m.View()) > m.height {
		t.Fatal("chat layout exceeds the terminal height")
	}
	m.scrollChat(tea.KeyMsg{Type: tea.KeyCtrlDown})
	m.scrollChat(tea.KeyMsg{Type: tea.KeyCtrlDown})
	if !m.viewport.AtBottom() {
		t.Fatal("Ctrl+End must resume following")
	}
}

func TestStreamRenderingIsBatched(t *testing.T) {
	m := tuiModel{loading: true, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	updated, cmd := m.Update(chatChunkMsg{text: "hello"})
	m = updated.(tuiModel)
	if cmd == nil || !m.renderPending || m.streamRendered != "" {
		t.Fatal("first chunk must schedule a render instead of rendering immediately")
	}
	updated, cmd = m.Update(chatChunkMsg{text: " world"})
	m = updated.(tuiModel)
	if cmd != nil || m.streaming != "hello world" {
		t.Fatal("subsequent chunks must share the pending render without losing text")
	}
}

func TestToolActivityStatus(t *testing.T) {
	m := tuiModel{loading: true, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	updated, _ := m.Update(chatStatusMsg("running tool: bash"))
	m = updated.(tuiModel)
	if !strings.Contains(m.View(), "running tool: bash") || strings.Contains(m.View(), "streaming...") {
		t.Fatal("tool execution must not be displayed as model streaming")
	}
}
