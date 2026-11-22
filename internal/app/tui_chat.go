package app

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Keep aliases here as well so every command accepted by handleSlashCommand
// can be discovered while typing.
var slashCommands = []string{
	"/clear", "/connect", "/doctor", "/exit", "/help", "/logout",
	"/mcps", "/model", "/models", "/new", "/providers", "/quit",
	"/save", "/sessions", "/skills", "/system", "/trace",
}

func slashMatches(prefix string) []string {
	if prefix == "" || prefix[0] != '/' || strings.ContainsAny(prefix, " \t\n\r") {
		return nil
	}
	var matches []string
	for _, command := range slashCommands {
		if strings.HasPrefix(command, prefix) {
			matches = append(matches, command)
		}
	}
	return matches
}

func (m tuiModel) slashSuggestions() []string {
	value := m.textarea.Value()
	if m.completionPrefix != "" {
		value = m.completionPrefix
	}
	return slashMatches(value)
}

func (m *tuiModel) completeSlashCommand(reverse bool) bool {
	value := m.textarea.Value()
	line := m.textarea.LineInfo()
	if m.textarea.LineCount() != 1 || line.StartColumn+line.ColumnOffset != len([]rune(value)) {
		return false
	}
	prefix := value
	if m.completionPrefix != "" {
		prefix = m.completionPrefix
	}
	matches := slashMatches(prefix)
	if len(matches) == 0 {
		m.completionPrefix = ""
		return false
	}
	if m.completionPrefix == "" {
		m.completionPrefix = prefix
		m.completionIndex = 0
		if reverse {
			m.completionIndex = len(matches) - 1
		}
	} else if reverse {
		m.completionIndex = (m.completionIndex + len(matches) - 1) % len(matches)
	} else {
		m.completionIndex = (m.completionIndex + 1) % len(matches)
	}
	m.textarea.SetValue(matches[m.completionIndex])
	m.textarea.CursorEnd()
	return true
}

// toolArgsSummary renders the key arguments of a tool call for status lines.
func toolArgsSummary(call toolCall) string {
	if call.Function.Name != "bash" {
		if call.Function.Arguments == "" {
			return "(no args)"
		}
		return "(" + clipArgs(call.Function.Arguments, 80) + ")"
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args.Command == "" {
		return clipArgs(call.Function.Arguments, 100)
	}
	command := args.Command
	if len(command) > 100 {
		command = command[:100] + "…"
	}
	return clipArgs(command, 100)
}

func clipArgs(s string, limit int) string {
	if len(s) > 4096 {
		s = s[:4096]
	}
	s = stripTerminalControls(s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}

func (m tuiModel) chatHeader() []string {
	header := []string{
		headStyle.Render("hanoi TUI"),
		hintStyle.Render("Enter send · Esc Esc stop · ↑/↓ history · Ctrl+↑↓ scroll · Ctrl+T trace · Ctrl+S tasks · /help"),
		hintStyle.Render("model: " + m.cfg.ModelReference()),
	}
	header = append(header, hintStyle.Render(m.usage.summary(m.cfg)))
	if len(m.tasks) > 0 {
		header = append(header, hintStyle.Render(tuiStatusText(m.taskSummary(), max(1, m.width-appStyle.GetHorizontalFrameSize()))))
	}
	if m.mouseEnabled {
		header[0] += hintStyle.Render(" · mouse scroll on (Ctrl+G: select text)")
	} else {
		header[0] += hintStyle.Render(" · text selection (Ctrl+G: mouse scroll)")
	}
	if m.memoryName != "" {
		header = append(header, hintStyle.Render("session: "+m.memoryName))
	}
	if m.system != "" {
		header = append(header, hintStyle.Render(tuiStatusText("system: "+m.system, max(20, m.width-6))))
	}
	if m.cfg.AgentInstructionsPath != "" {
		header = append(header, hintStyle.Render(tuiStatusText("instructions: "+m.cfg.AgentInstructionsPath, max(20, m.width-6))))
	}
	if m.traceOpen {
		header = append(header, headStyle.Render("Hanoi Trace · Ctrl+T / Esc returns to chat"))
	}
	if m.doctorOpen {
		header = append(header, headStyle.Render("Hanoi Doctor · Esc returns to chat"))
	}
	if m.tasksOpen {
		header = append(header, headStyle.Render("Subtasks · Ctrl+S / Esc returns to chat"))
	}
	// Header text must not expand the layout beyond the terminal width. Its
	// wrapped height is also used by resizeChat.
	for i := range header {
		header[i] = ansi.Hardwrap(header[i], max(1, m.width-appStyle.GetHorizontalFrameSize()), true)
	}
	return header
}

func (m tuiModel) taskSummary() string {
	counts := map[string]int{}
	for _, task := range m.tasks {
		counts[task.Status]++
	}
	return fmt.Sprintf("tasks: %d · Ctrl+S details · %d running · %d queued · %d done · %d failed · %d cancelled",
		len(m.tasks), counts["running"], counts["queued"], counts["done"], counts["failed"], counts["cancelled"])
}

func (m tuiModel) taskDetails() string {
	if len(m.tasks) == 0 {
		return "No subtasks in this turn."
	}
	ids := make([]string, 0, len(m.tasks))
	for id := range m.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var details []string
	for _, id := range ids {
		task := m.tasks[id]
		line := "task " + task.Description + ": " + task.Status
		if task.Reason != "" {
			line += " · " + task.Reason
		}
		details = append(details, tuiDisplayText(line))
	}
	return strings.Join(details, "\n\n")
}

func (m *tuiModel) toggleTasks() {
	if !m.tasksOpen {
		if !m.traceOpen {
			m.chatOffset = m.viewport.YOffset
		}
		m.traceOpen = false
	}
	m.tasksOpen = !m.tasksOpen
	m.userScrolled = false
	m.resizeChat()
	m.refreshChat()
	if m.tasksOpen {
		m.viewport.GotoTop()
	} else {
		m.viewport.SetYOffset(m.chatOffset)
		m.userScrolled = !m.viewport.AtBottom()
	}
}

func (m *tuiModel) resizeChat() {
	boxWidth := max(1, m.width-appStyle.GetHorizontalFrameSize()-boxStyle.GetHorizontalBorderSize())
	m.textarea.SetWidth(max(1, boxWidth-boxStyle.GetHorizontalPadding()))
	m.viewport.Width = max(1, boxWidth-boxStyle.GetHorizontalPadding()-2)
	// An empty body occupies one line; replace it with all remaining rows.
	chromeHeight := lipgloss.Height(m.renderChatLayout("")) - 1
	m.viewport.Height = max(1, m.height-chromeHeight)
}

func (m *tuiModel) refreshChat() {
	follow := !m.userScrolled && (m.viewport.AtBottom() || m.viewport.TotalLineCount() == 0)
	body := strings.Join(m.history, "\n\n")
	if m.doctorOpen {
		body = doctorReportText(m.doctorText)
	} else if m.traceOpen {
		body = renderTrace(m.traceEvents)
	} else if m.tasksOpen {
		body = m.taskDetails()
	} else if m.streaming != "" {
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
		m.viewport.LineUp(3)
	case "ctrl+down":
		m.viewport.LineDown(3)
	case "ctrl+home":
		m.viewport.GotoTop()
	case "ctrl+end":
		m.viewport.GotoBottom()
	default:
		return false
	}
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
