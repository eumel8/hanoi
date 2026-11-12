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

// The status must use terminal defaults, even when the detected theme is wrong
// for a custom palette. Do not reintroduce a blue foreground here.
func TestStatusInheritsTerminalColors(t *testing.T) {
	if _, ok := headStyle.GetForeground().(lipgloss.NoColor); !ok {
		t.Fatal("status overrides terminal foreground")
	}
	if _, ok := headStyle.GetBackground().(lipgloss.NoColor); !ok {
		t.Fatal("status overrides terminal background")
	}
	if !headStyle.GetBold() {
		t.Fatal("status should remain visually emphasized")
	}
	for _, dark := range []bool{false, true} {
		for _, profile := range []termenv.Profile{termenv.Ascii, termenv.ANSI, termenv.ANSI256, termenv.TrueColor} {
			renderer := lipgloss.NewRenderer(&strings.Builder{})
			renderer.SetHasDarkBackground(dark)
			renderer.SetColorProfile(profile)
			got := headStyle.Renderer(renderer).Render("Hanoi status")
			want := "\x1b[1mHanoi status\x1b[0m"
			if profile == termenv.Ascii {
				want = "Hanoi status"
			}
			if got != want {
				t.Errorf("dark=%v profile=%v: got %q, want %q", dark, profile, got, want)
			}
		}
	}
}

func TestTUIAccentColorsAreAdaptive(t *testing.T) {
	for name, style := range map[string]lipgloss.Style{
		"user": userStyle, "assistant": botStyle, "error": errStyle, "warning": warnStyle,
	} {
		color, ok := style.GetForeground().(lipgloss.AdaptiveColor)
		if !ok || color.Light == "" || color.Dark == "" || color.Light == color.Dark {
			t.Errorf("%s should have distinct light and dark accent colors", name)
		}
	}
}
