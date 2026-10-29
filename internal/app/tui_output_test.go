package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestLargeToolErrorFitsTUIAndCanStop(t *testing.T) {
	m := tuiModel{loading: true, width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	m.textarea.SetHeight(4)
	m.resizeChat()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.chatCancel = cancel
	m.err = strings.Repeat("tool error\n\x1b[2J\x1b[H", 100000)
	view := m.View()
	if lipgloss.Height(view) > m.height || strings.Contains(view, "\x1b[2J") {
		t.Fatal("tool error escaped TUI bounds")
	}
	m = updateModel(m, tea.KeyMsg{Type: tea.KeyEsc, Alt: true})
	if ctx.Err() != context.Canceled {
		t.Fatal("could not cancel after large error")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C did not quit")
	}
}

func TestLargeStreamingResponseDisplayBounded(t *testing.T) {
	m := tuiModel{loading: true, width: 80, height: 30, textarea: textarea.New(), viewport: viewport.New(60, 10)}
	m.textarea.SetHeight(4)
	m.resizeChat()
	large := strings.Repeat("| a | b |\n", 200000)
	started := time.Now()
	m = updateModel(m, chatChunkMsg{text: large})
	m = updateModel(m, streamRenderMsg{})
	if len(m.streaming) > maxTUIDisplayBytes+1 || !m.streamDisplayFull {
		t.Fatal("stream display unbounded")
	}
	if !strings.Contains(m.streamRendered, "Display shortened") {
		t.Fatal("missing display truncation notice")
	}
	for i := 0; i < 100; i++ {
		updated, cmd := m.Update(chatChunkMsg{text: large})
		m = updated.(tuiModel)
		if cmd != nil {
			t.Fatal("continued rendering after display cap")
		}
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("large stream processing blocked UI")
	}
	m = updateModel(m, chatResultMsg{text: large})
	if len(m.messages) != 1 || m.messages[0].Content != large {
		t.Fatal("display limit changed full response")
	}
	if lipgloss.Height(m.View()) > m.height {
		t.Fatal("large response escaped viewport")
	}
}
