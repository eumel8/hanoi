package app

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m tuiModel) providerView() string {
	lines := []string{headStyle.Render("Providers and models"), "↑/↓ select · Enter activate · r refresh models · a Copilot login · l Copilot logout · Esc close", ""}
	if m.providerAuthInfo != "" {
		lines = append(lines, m.providerAuthInfo, "")
	}
	if m.discoveryInfo != "" {
		lines = append(lines, m.discoveryInfo, "")
	}
	choices := m.cfg.ModelChoices()
	if len(choices) == 0 {
		lines = append(lines, "No providers configured; using OPENAI_* environment settings.", "Add a provider section to hanoi.json to configure multiple providers.")
	}
	// Keep the selected row visible even with a large remote catalog.
	visible := len(choices)
	if m.height > 0 {
		visible = max(1, m.height-14-strings.Count(m.providerAuthInfo, "\n"))
	}
	start := max(0, m.providerCursor-visible/2)
	if start+visible > len(choices) {
		start = max(0, len(choices)-visible)
	}
	if start > 0 {
		lines = append(lines, "↑ more models")
	}
	for index := start; index < len(choices) && index < start+visible; index++ {
		choice := choices[index]
		providerID, modelID, _ := strings.Cut(choice, "/")
		provider := m.cfg.Providers[providerID]
		row := choice
		if provider.Name != "" {
			row += " · " + provider.Name
		}
		if model := provider.Models[modelID]; model.Name != "" {
			row += " · " + model.Name
		}
		if choice == m.cfg.ModelReference() {
			row += " (active)"
		}
		prefix := "  "
		if index == m.providerCursor {
			prefix = "> "
		}
		lines = append(lines, prefix+row)
	}
	if start+visible < len(choices) {
		lines = append(lines, "↓ more models")
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) updateProviderMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	choices := m.cfg.ModelChoices()
	switch key.String() {
	case "r", "ctrl+r":
		return m.startModelDiscovery()
	case "a":
		return m.startProviderAuth()
	case "l":
		m.providerAuthInfo = "GitHub Copilot logged out locally."
		if err := logoutCopilot(); err != nil {
			m.providerAuthInfo = err.Error()
		}
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
