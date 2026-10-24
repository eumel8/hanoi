package app

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

func (m *tuiModel) toggleTrace() {
	if !m.traceOpen {
		m.chatOffset = m.viewport.YOffset
	}
	m.traceOpen = !m.traceOpen
	m.userScrolled = false
	m.resizeChat()
	m.refreshChat()
	if !m.traceOpen {
		m.viewport.SetYOffset(m.chatOffset)
		m.userScrolled = !m.viewport.AtBottom()
	} else {
		m.viewport.GotoBottom()
	}
}

func renderTrace(events []traceEvent) string {
	if len(events) == 0 {
		return "No trace events yet. Send a prompt to record a turn."
	}
	var out strings.Builder
	for _, e := range events {
		prefix := fmt.Sprintf("%s · Turn %d · %s", e.Time.Local().Format("15:04:05"), e.Turn, traceText(e.Model))
		var detail string
		switch e.Type {
		case "turn.started":
			detail = "User · " + traceText(e.Text)
		case "model.started":
			detail = "Model request · waiting for response"
		case "model.completed":
			detail = fmt.Sprintf("Model response · %s · %.1f ms · %d tool calls · %d bytes", e.Status, e.DurationMS, e.ToolCalls, e.OutputBytes)
		case "tool.started":
			detail = "Tool · " + traceText(e.Tool) + " · " + traceText(e.Arguments)
		case "tool.completed":
			detail = fmt.Sprintf("Tool · %s · %s · %.1f ms · %d output bytes", traceText(e.Tool), e.Status, e.DurationMS, e.OutputBytes)
			if e.ExitCode != nil {
				detail += fmt.Sprintf(" · exit %d", *e.ExitCode)
			}
		case "tool.result":
			detail = fmt.Sprintf("Tool result → model · %s · %d bytes", traceText(e.Tool), e.ResultBytes)
		case "turn.completed":
			detail = fmt.Sprintf("Turn · %s · %.1f ms", e.Status, e.DurationMS)
		}
		if e.Error != "" {
			detail += " · " + traceText(e.Error)
		}
		fmt.Fprintf(&out, "%s\n%s\n\n", prefix, detail)
	}
	return strings.TrimSpace(out.String())
}

func traceText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(text))
	runes := []rune(text)
	if len(runes) > 500 {
		return string(runes[:500]) + "…"
	}
	return text
}
