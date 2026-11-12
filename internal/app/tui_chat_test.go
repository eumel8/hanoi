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
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
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

func TestRepeatedCtrlArrowsScrollOnlyThreeLines(t *testing.T) {
	m := tuiModel{width: 100, height: 40, textarea: textarea.New(), viewport: viewport.New(60, 10), history: []string{strings.Repeat("chat line\n", 200)}}
	m.textarea.SetHeight(4)
	m.resizeChat()
	m.refreshChat()
	bottom := m.viewport.YOffset
	for i := 1; i <= 4; i++ {
		m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlUp})
		if got, want := m.viewport.YOffset, bottom-3*i; got != want {
			t.Fatalf("Ctrl+Up #%d: offset %d, want %d", i, got, want)
		}
	}
	for i := 0; i < 4; i++ {
		m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlDown})
	}
	if !m.viewport.AtBottom() || m.userScrolled {
		t.Fatal("scrolling back to the bottom must resume following")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlHome})
	if m.viewport.YOffset != 0 || !m.userScrolled {
		t.Fatal("Ctrl+Home must jump to the top and pause following")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.viewport.AtBottom() || m.userScrolled {
		t.Fatal("Ctrl+End must jump to the bottom and resume following")
	}
}

func TestChatUsesAvailableTerminalHeight(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 30}, {120, 50}, {60, 25}, {40, 30}} {
		m := tuiModel{textarea: textarea.New(), viewport: viewport.New(70, 10), messages: []message{{Role: "assistant", Content: strings.Repeat("chat line\n", 100)}}}
		m.textarea.SetHeight(4)
		m = updateModel(m, tea.WindowSizeMsg{Width: size.width, Height: size.height})
		view := m.View()
		if got := lipgloss.Height(view); got != size.height {
			t.Errorf("%dx%d: rendered height %d, want %d", size.width, size.height, got, size.height)
		}
		if got := lipgloss.Width(view); got > size.width {
			t.Errorf("%dx%d: rendered width %d exceeds terminal", size.width, size.height, got)
		}
		if size.height == 50 && m.viewport.Height <= 10 {
			t.Fatal("large terminal still limited to ten chat lines")
		}
	}
}

func TestMouseReportingIsOptIn(t *testing.T) {
	m := tuiModel{textarea: textarea.New(), viewport: viewport.New(60, 10)}
	m.viewport.SetContent(strings.Repeat("chat line\n", 100))
	m.viewport.SetYOffset(30)
	wheel := tea.MouseMsg{Button: tea.MouseButtonWheelUp}
	m = updateModel(m, wheel)
	if m.viewport.YOffset != 30 {
		t.Fatal("default selection mode must not consume mouse wheel events")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(tuiModel)
	if !m.mouseEnabled || cmd == nil {
		t.Fatal("Ctrl+G must enable application mouse reporting")
	}
	m = updateModel(m, wheel)
	if m.viewport.YOffset != 27 {
		t.Fatal("opt-in mouse mode must scroll three lines")
	}
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	m = updated.(tuiModel)
	if m.mouseEnabled || cmd == nil {
		t.Fatal("Ctrl+G must restore native terminal selection")
	}
}

func TestHelpIsScrollableAndFitsTerminal(t *testing.T) {
	m := tuiModel{width: 60, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	m.textarea.SetHeight(4)
	m.resizeChat()
	updated, _ := m.handleSlashCommand("/help")
	m = updated.(tuiModel)
	if !strings.Contains(strings.Join(m.history, "\n"), "Ctrl+Home/End") || m.err != "" {
		t.Fatal("full help should be in the scrollable chat, not the status line")
	}
	if lipgloss.Height(m.View()) > m.height || lipgloss.Width(m.View()) > m.width {
		t.Fatal("help escaped terminal bounds")
	}
}

func newScrollModel() tuiModel {
	m := tuiModel{
		width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10),
		history: []string{strings.Repeat("chat line\n", 100)}, loading: true,
	}
	m.textarea.SetHeight(4)
	m.resizeChat()
	m.refreshChat()
	return m
}

func TestScrollBackToBottomResumesFollowing(t *testing.T) {
	var model tea.Model = newScrollModel()
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlUp})
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlDown})
	m := model.(tuiModel)
	if !m.viewport.AtBottom() || m.userScrolled {
		t.Fatalf("reaching the bottom must resume following, atBottom=%v userScrolled=%v", m.viewport.AtBottom(), m.userScrolled)
	}
	m.streaming = strings.Repeat("new\n", 30)
	m.streamRendered = m.streaming
	m.refreshChat()
	if !m.viewport.AtBottom() {
		t.Fatal("streamed lines must stay visible after scrolling back to the bottom")
	}
}

func TestMouseWheelBackToBottomResumesFollowing(t *testing.T) {
	m0 := newScrollModel()
	m0.mouseEnabled = true
	var model tea.Model = m0
	model, _ = model.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	m := model.(tuiModel)
	if !m.userScrolled {
		t.Fatal("wheel up must pause following")
	}
	model, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	m = model.(tuiModel)
	if !m.viewport.AtBottom() || m.userScrolled {
		t.Fatalf("wheel down to the bottom must resume following, atBottom=%v userScrolled=%v", m.viewport.AtBottom(), m.userScrolled)
	}
}
