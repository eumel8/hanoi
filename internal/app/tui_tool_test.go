package app

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func mkCall(name, args string) toolCall {
	return toolCall{Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: name, Arguments: args}}
}

func updateModel(m tuiModel, msg tea.Msg) tuiModel {
	updated, _ := m.Update(msg)
	return updated.(tuiModel)
}

func TestFmtDuration(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{9 * time.Second, "9s"},
		{59 * time.Second, "59s"},
		{65 * time.Second, "1m05s"},
		{3725 * time.Second, "1h02m"},
	} {
		if got := fmtDuration(tc.d); got != tc.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestToolArgsSummary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		call      toolCall
		want      string
		maxLength int
	}{
		{"bash command is shown", mkCall("bash", `{"command":"ls -la  /tmp"}`), "ls -la /tmp", 0},
		{"bash command is clipped", mkCall("bash", `{"command":"`+strings.Repeat("x", 200)+`"}`), "", 101},
		{"invalid bash arguments fall back to raw args", mkCall("bash", "not-json"), "not-json", 0},
		{"mcp arguments are wrapped in parentheses", mkCall("alertmanager_list_alerts", `{"filter":["severity=critical"]}`), `({"filter":["severity=critical"]})`, 0},
		{"mcp without arguments", mkCall("alertmanager_list_silences", ""), "(no args)", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolArgsSummary(tc.call)
			if tc.want != "" && got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if tc.maxLength > 0 && len([]rune(got)) > tc.maxLength {
				t.Fatalf("summary too long (%d runes): %q", len([]rune(got)), got)
			}
		})
	}
}

func newToolTestModel() tuiModel {
	ta := textarea.New()
	ta.SetHeight(4)
	return tuiModel{
		loading:  true,
		width:    100,
		height:   30,
		textarea: ta,
		viewport: viewport.New(60, 10),
	}
}

func TestToolStatusShowsCommand(t *testing.T) {
	m := newToolTestModel()
	updated, cmd := m.Update(chatStatusMsg("running tool: bash find / -name SKILL.md"))
	m = updated.(tuiModel)
	if cmd == nil {
		t.Fatal("a running tool must start the elapsed-time ticker")
	}
	if !strings.Contains(m.activity, "bash find / -name SKILL.md") {
		t.Fatalf("activity must contain the command, got %q", m.activity)
	}
	if !strings.Contains(m.View(), "bash find / -name SKILL.md") {
		t.Fatal("the status line must show the running command")
	}
}

func TestToolStatusTickAppendsElapsedOnce(t *testing.T) {
	m := newToolTestModel()
	m = updateModel(m, chatStatusMsg("running tool: bash find / -name SKILL.md"))
	m.activitySince = time.Now().Add(-45 * time.Second)

	m = updateModel(m, chatTickMsg{})
	if strings.Count(m.activity, "·") != 1 {
		t.Fatalf("elapsed time must be appended exactly once, got %q", m.activity)
	}
	if !strings.HasSuffix(m.activity, "45s") {
		t.Fatalf("elapsed suffix missing, got %q", m.activity)
	}
	if !strings.Contains(m.View(), "· 45s") {
		t.Fatalf("status line must show elapsed time, got %q", m.activity)
	}

	// A second tick must replace the elapsed time, not stack another suffix.
	m.activitySince = time.Now().Add(-46 * time.Second)
	m = updateModel(m, chatTickMsg{})
	if strings.Count(m.activity, "·") != 1 || !strings.HasSuffix(m.activity, "46s") {
		t.Fatalf("elapsed time must be replaced, not stacked, got %q", m.activity)
	}
}

func TestToolStatusWarnsAfter30Seconds(t *testing.T) {
	// Tests run without a TTY, so lipgloss renders the warning style as plain
	// text; assert the elapsed label is stable and present after 30 seconds.
	m := newToolTestModel()
	m = updateModel(m, chatStatusMsg("running tool: bash sleep 90"))
	m.activitySince = time.Now().Add(-45 * time.Second)
	m = updateModel(m, chatTickMsg{})
	if !strings.Contains(m.activity, "· 45s") {
		t.Fatalf("status must carry the elapsed time after 30s, got %q", m.activity)
	}
	if strings.Count(m.activity, "·") != 1 {
		t.Fatalf("warning render must not add another separator, got %q", m.activity)
	}
}

func TestToolStatusTickerStopsAfterResult(t *testing.T) {
	m := newToolTestModel()
	m = updateModel(m, chatStatusMsg("running tool: bash true"))
	m = updateModel(m, chatResultMsg{text: "done"})
	if m.toolTickCmdActive() {
		t.Fatal("the ticker must stop once the turn finished")
	}
	if m.activity != "" || m.activityBase != "" {
		t.Fatalf("activity must be cleared, got %q / %q", m.activity, m.activityBase)
	}
}

func TestWaitingStatusDoesNotTick(t *testing.T) {
	m := newToolTestModel()
	updated, cmd := m.Update(chatStatusMsg("waiting for model..."))
	m = updated.(tuiModel)
	if cmd != nil {
		t.Fatal("the model wait must not start the tool ticker")
	}
	m.activitySince = time.Now().Add(-10 * time.Second)
	m = updateModel(m, chatTickMsg{})
	if strings.Contains(m.activity, "·") {
		t.Fatalf("elapsed time must only be shown for tools, got %q", m.activity)
	}
}
