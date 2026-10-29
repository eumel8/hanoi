package app

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

const maxTUIDisplayBytes = 32 * 1024
const maxTUIMarkdownBytes = 8 * 1024

// Bound work before stripping control sequences or parsing Markdown. The full
// response remains in messages/session storage; this only limits its display.
func tuiDisplayText(text string) string {
	const notice = "\n[Display shortened; full response remains in the session.]"
	shortened := len(text) > maxTUIDisplayBytes
	if shortened {
		end := maxTUIDisplayBytes
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		text = text[:end]
	}
	text = stripTerminalControls(text)
	if shortened {
		text += notice
	}
	return text
}

func stripTerminalControls(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

func tuiStatusText(text string, width int) string {
	if width <= 1 {
		width = 80
	}
	if len(text) > 4096 {
		text = text[:4096]
	}
	text = strings.Join(strings.Fields(stripTerminalControls(text)), " ")
	return ansi.Truncate(text, max(1, width), "…")
}
