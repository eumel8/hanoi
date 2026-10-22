package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func (m tuiModel) chatHeader() []string {
	header := []string{
		headStyle.Render("hanoi TUI"),
		hintStyle.Render("Enter send · ↑/↓ history · Ctrl+↑↓ scroll · 2x Ctrl+↑↓ top/bottom · /help"),
		hintStyle.Render("model: " + m.cfg.ModelReference()),
	}
	if m.memoryName != "" {
		header = append(header, hintStyle.Render("memory: "+m.memoryName))
	}
	if m.system != "" {
		header = append(header, hintStyle.Render("system: "+m.system))
	}
	return header
}

func (m *tuiModel) resizeChat() {
	boxWidth := max(20, m.width-6)
	m.textarea.SetWidth(max(1, boxWidth-boxStyle.GetHorizontalPadding()))
	m.viewport.Width = max(1, boxWidth-boxStyle.GetHorizontalPadding()-2)
	headerHeight := lipgloss.Height(strings.Join(m.chatHeader(), "\n"))
	// Outer padding, two borders, two blank lines, status, and input area.
	m.viewport.Height = max(1, m.height-headerHeight-m.textarea.Height()-9)
}

func (m *tuiModel) refreshChat() {
	follow := !m.userScrolled && (m.viewport.AtBottom() || m.viewport.TotalLineCount() == 0)
	body := strings.Join(m.history, "\n\n")
	if m.streaming != "" {
		body += "\n\n" + botStyle.Render("assistant") + "\n" + m.streamRendered
	}
	if body == "" {
		body = hintStyle.Render("No messages yet.")
	}
	m.viewport.SetContent(ansi.Hardwrap(body, max(1, m.viewport.Width), true))
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m tuiModel) chatViewport() string {
	height := max(1, m.viewport.Height)
	total := max(height, m.viewport.TotalLineCount())
	thumb := max(1, height*height/total)
	top := int(float64(height-thumb) * m.viewport.ScrollPercent())
	bar := make([]string, height)
	for i := range bar {
		bar[i] = hintStyle.Render("│")
		if i >= top && i < top+thumb {
			bar[i] = botStyle.Render("┃")
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, m.viewport.View(), " ", strings.Join(bar, "\n"))
}

func (m *tuiModel) scrollChat(key tea.KeyMsg) bool {
	k := key.String()
	switch k {
	case "pgup":
		m.viewport.PageUp()
	case "pgdown":
		m.viewport.PageDown()
	case "ctrl+up":
		if m.lastScrollKey == "ctrl+up" {
			m.viewport.GotoTop()
		} else {
			m.viewport.LineUp(3)
		}
	case "ctrl+down":
		if m.lastScrollKey == "ctrl+down" {
			m.viewport.GotoBottom()
		} else {
			m.viewport.LineDown(3)
		}
	default:
		return false
	}
	m.lastScrollKey = k
	return true
}

func (m *tuiModel) rememberInput(input string) {
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != input {
		m.inputHistory = append(m.inputHistory, input)
	}
	m.historyIndex = len(m.inputHistory)
	m.historyDraft = ""
}

func (m *tuiModel) recallInput(key string) bool {
	up := key == "up" || key == "alt+up"
	if !strings.HasPrefix(key, "alt+") {
		if up && m.textarea.Line() != 0 {
			return false
		}
		if !up && m.textarea.Line() != m.textarea.LineCount()-1 {
			return false
		}
	}
	if len(m.inputHistory) == 0 {
		return false
	}
	if !up && m.historyIndex == len(m.inputHistory) {
		return false
	}
	if m.historyIndex == len(m.inputHistory) {
		m.historyDraft = m.textarea.Value()
	}
	if up {
		m.historyIndex = max(0, m.historyIndex-1)
	} else if m.historyIndex < len(m.inputHistory) {
		m.historyIndex++
	}
	value := m.historyDraft
	if m.historyIndex < len(m.inputHistory) {
		value = m.inputHistory[m.historyIndex]
	}
	m.textarea.SetValue(value)
	m.textarea.CursorEnd()
	return true
}
