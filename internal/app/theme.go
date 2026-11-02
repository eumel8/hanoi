package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
)

func configureTerminalTheme(theme string) error {
	switch theme {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	case "auto":
		// COLORFGBG is available in some terminals even when OSC queries are
		// unavailable, including Windows executables launched from Unix shells.
		if dark, ok := colorFGBGDark(os.Getenv("COLORFGBG")); ok {
			lipgloss.SetHasDarkBackground(dark)
		}
	default:
		return fmt.Errorf("invalid theme %q: use auto, light or dark", theme)
	}
	return nil
}

func colorFGBGDark(value string) (bool, bool) {
	parts := strings.Split(value, ";")
	if len(parts) < 2 {
		return false, false
	}
	index, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || index < 0 || index > 15 {
		return false, false
	}
	// Match the conventional ANSI palette used by COLORFGBG.
	return index < 7 || index == 8, true
}

func configureTextareaColors(input *textarea.Model) {
	// Inherit the terminal's default foreground/background instead of painting
	// a gray or black cursor line over a user-selected terminal theme.
	style := textarea.Style{
		Base: lipgloss.NewStyle(), CursorLine: lipgloss.NewStyle(),
		Text: lipgloss.NewStyle(), Placeholder: hintStyle,
		Prompt: hintStyle, EndOfBuffer: lipgloss.NewStyle(),
	}
	input.FocusedStyle, input.BlurredStyle = style, style
}

func terminalMarkdownStyle(dark bool) ansi.StyleConfig {
	style := styles.LightStyleConfig
	if dark {
		style = styles.DarkStyleConfig
	}
	// Body text follows the actual terminal foreground, not hardcoded gray.
	style.Document.Color = nil
	style.Document.BackgroundColor = nil
	return style
}
