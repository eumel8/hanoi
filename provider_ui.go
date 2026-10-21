package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m tuiModel) providerView() string {
	lines := []string{headStyle.Render("Providers and models"), "↑/↓ select · Enter activate · Esc close", ""}
	choices := m.cfg.modelChoices()
	if len(choices) == 0 {
		lines = append(lines, "No providers configured; using OPENAI_* environment settings.", "Add a provider section to hanoi.json to configure multiple providers.")
	}
	for index, choice := range choices {
		providerID, modelID, _ := strings.Cut(choice, "/")
		provider := m.cfg.Providers[providerID]
		row := choice
		if provider.Name != "" {
			row += " · " + provider.Name
		}
		if model := provider.Models[modelID]; model.Name != "" {
			row += " · " + model.Name
		}
		if choice == m.cfg.modelReference() {
			row += " (active)"
		}
		prefix := "  "
		if index == m.providerCursor {
			prefix = "> "
		}
		lines = append(lines, prefix+row)
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) updateProviderMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	choices := m.cfg.modelChoices()
	switch key.String() {
	case "esc", "q":
		m.providersOpen = false
	case "up", "k":
		m.providerCursor = max(0, m.providerCursor-1)
	case "down", "j":
		if m.providerCursor+1 < len(choices) {
			m.providerCursor++
		}
	case "enter":
		if m.providerCursor >= len(choices) {
			return m, nil
		}
		cfg, err := m.cfg.selectModel(choices[m.providerCursor])
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.cfg, m.err, m.providersOpen = cfg, "", false
	}
	return m, nil
}
