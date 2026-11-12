package app

import (
	"os"
	"strings"

	"hanoi/internal/skills"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) refreshSkills() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	workspace, err := os.Getwd()
	if err != nil {
		return err
	}
	m.skills, m.skillWarnings = skills.Discover(home, workspace)
	m.skillCursor = 0
	return nil
}

func (m tuiModel) handleSkillsCommand(name string) (tea.Model, tea.Cmd) {
	if m.loading {
		m.err = "Stop the current response before loading a skill."
		return m, nil
	}
	if err := m.refreshSkills(); err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.err = ""
	if name != "" {
		for i, skill := range m.skills {
			if skill.Name == name {
				m.skillCursor = i
				return m.selectSkill()
			}
		}
		m.err = "Unknown skill: " + name + ". Use /skills to list available skills."
	}
	m.skillsOpen = true
	return m, nil
}

func (m tuiModel) selectSkill() (tea.Model, tea.Cmd) {
	if m.skillCursor < 0 || m.skillCursor >= len(m.skills) {
		return m, nil
	}
	// Re-read at activation so edits and deleted files are handled immediately.
	skill, err := skills.Load(m.skills[m.skillCursor].Path)
	if err != nil {
		m.err = err.Error()
		return m, nil
	}
	m.skillsOpen = false
	m.textarea.SetValue(skill.Prompt())
	// Use the regular send path for persistence, cancellation and streaming.
	return m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

func (m tuiModel) skillView() string {
	lines := []string{headStyle.Render("Skills"), "↑/↓ select · Enter load and send · r refresh · Esc close", ""}
	if len(m.skills) == 0 {
		lines = append(lines, "No skills found. Add <name>/SKILL.md under ~/.hanoi/skills/ or ./.hanoi/skills/.")
	}
	if len(m.skillWarnings) > 0 {
		lines = append(lines, "Some skills could not be loaded:", tuiStatusText(strings.Join(m.skillWarnings, "; "), max(20, m.width-10)), "")
	}
	visible := len(m.skills)
	if m.height > 0 {
		visible = max(1, (m.height-18)/3)
	}
	start := max(0, m.skillCursor-visible/2)
	if start+visible > len(m.skills) {
		start = max(0, len(m.skills)-visible)
	}
	if start > 0 {
		lines = append(lines, "↑ more skills")
	}
	for i := start; i < len(m.skills) && i < start+visible; i++ {
		skill := m.skills[i]
		prefix := "  "
		if i == m.skillCursor {
			prefix = "> "
		}
		lines = append(lines, prefix+skill.Name,
			"  "+tuiStatusText(skill.Description, max(20, m.width-12)),
			"  "+tuiStatusText(skill.Path, max(20, m.width-12)))
	}
	if start+visible < len(m.skills) {
		lines = append(lines, "↓ more skills")
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) updateSkillMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.skillsOpen = false
	case "up", "k":
		m.skillCursor = max(0, m.skillCursor-1)
	case "down", "j":
		if m.skillCursor+1 < len(m.skills) {
			m.skillCursor++
		}
	case "r", "ctrl+r":
		m.err = ""
		if err := m.refreshSkills(); err != nil {
			m.err = err.Error()
		}
	case "enter":
		return m.selectSkill()
	}
	return m, nil
}
