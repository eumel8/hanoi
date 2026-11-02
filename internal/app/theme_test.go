package app

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestTerminalThemeSelection(t *testing.T) {
	previous := lipgloss.HasDarkBackground()
	t.Cleanup(func() { lipgloss.SetHasDarkBackground(previous) })
	for _, tc := range []struct {
		theme, fgbg string
		dark        bool
	}{
		{"light", "15;0", false}, {"dark", "0;15", true},
		{"auto", "0;15", false}, {"auto", "15;0", true},
		{"auto", "15;default;8", true},
	} {
		t.Setenv("COLORFGBG", tc.fgbg)
		if err := configureTerminalTheme(tc.theme); err != nil || lipgloss.HasDarkBackground() != tc.dark {
			t.Fatalf("theme=%s COLORFGBG=%s: %v", tc.theme, tc.fgbg, err)
		}
	}
	for _, value := range []string{"", "invalid", "15;invalid", "15;256", "15;-1"} {
		if _, ok := colorFGBGDark(value); ok {
			t.Fatalf("invalid COLORFGBG accepted: %q", value)
		}
	}
	if err := configureTerminalTheme("invalid"); err == nil {
		t.Fatal("invalid theme accepted")
	}
}

func TestTerminalColorsInheritedByInputAndMarkdown(t *testing.T) {
	input := textarea.New()
	configureTextareaColors(&input)
	for _, style := range []textarea.Style{input.FocusedStyle, input.BlurredStyle} {
		if _, ok := style.CursorLine.GetBackground().(lipgloss.NoColor); !ok {
			t.Fatal("cursor line paints over terminal background")
		}
		if _, ok := style.Text.GetForeground().(lipgloss.NoColor); !ok {
			t.Fatal("input text overrides terminal foreground")
		}
	}
	for _, dark := range []bool{false, true} {
		style := terminalMarkdownStyle(dark)
		if style.Document.Color != nil || style.Document.BackgroundColor != nil {
			t.Fatal("Markdown body overrides terminal colors")
		}
	}
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	t.Cleanup(func() { lipgloss.SetColorProfile(previous) })
	m := tuiModel{}
	if got := m.renderMarkdown(""); got != "" || m.markdown != nil {
		t.Fatal("empty startup content initializes Markdown renderer")
	}
	if got := m.renderMarkdown("Readable **text**"); !strings.Contains(got, "Readable") || strings.Contains(got, "38;") || strings.Contains(got, "48;") {
		t.Fatalf("no-color terminal received color escapes: %q", got)
	}
}
