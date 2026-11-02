package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func skillTestModel(t *testing.T) tuiModel {
	t.Helper()
	m := sessionTestModel(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"alpha", "review"} {
		dir := filepath.Join(".hanoi", "skills", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: Review code\n---\nRead references/guide.md before reviewing."
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func TestSkillPickerSendsAndPersistsSelection(t *testing.T) {
	m := skillTestModel(t)
	m.system = "Keep the existing system prompt."
	updated, cmd := m.handleSlashCommand("/skills")
	m = updated.(tuiModel)
	if cmd != nil || !m.skillsOpen || !strings.Contains(m.skillView(), "review") {
		t.Fatal("skill picker missing")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(tuiModel)
	updated, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	defer m.chatCancel()
	if cmd == nil || !m.loading || m.skillsOpen || len(m.messages) != 2 || m.messages[0].Content != m.system {
		t.Fatal("selection did not use the normal send path or lost system prompt")
	}
	if !strings.Contains(m.messages[1].Content, "Use the review skill") || !strings.Contains(m.messages[1].Content, "Read references/guide.md") {
		t.Fatalf("missing skill instructions: %v", m.messages)
	}
	saved, err := loadMemory(m.memoryName)
	if err != nil || len(saved) != 2 || saved[1].Content != m.messages[1].Content {
		t.Fatalf("skill prompt not persisted: %v", err)
	}
}

func TestSkillPickerCloseRefreshAndDeletedSkill(t *testing.T) {
	m := skillTestModel(t)
	updated, _ := m.handleSlashCommand("/skills")
	m = updated.(tuiModel)
	if err := os.Remove(m.skills[0].Path); err != nil {
		t.Fatal(err)
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if cmd != nil || !m.skillsOpen || m.loading || m.err == "" {
		t.Fatal("deleted skill must leave the picker open with an error")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	m = updated.(tuiModel)
	if len(m.skills) != 1 || m.skillCursor != 0 || m.err != "" {
		t.Fatal("refresh did not reload skills")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(tuiModel)
	if m.skillsOpen || len(m.messages) != 0 {
		t.Fatal("closing picker changed conversation")
	}
	updated, _ = m.handleSlashCommand("/skills missing")
	m = updated.(tuiModel)
	if !m.skillsOpen || !strings.Contains(m.err, "Unknown skill") {
		t.Fatal("unknown skill should show available choices")
	}
}

func TestSkillDirectInvocation(t *testing.T) {
	m := skillTestModel(t)
	updated, cmd := m.handleSlashCommand("/skills review")
	m = updated.(tuiModel)
	defer m.chatCancel()
	if cmd == nil || !m.loading || !strings.Contains(m.messages[0].Content, "Use the review skill") {
		t.Fatal("direct invocation did not send skill")
	}
	if got := slashMatches("/sk"); len(got) != 1 || got[0] != "/skills" {
		t.Fatalf("missing completion: %v", got)
	}
}
