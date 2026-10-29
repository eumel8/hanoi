package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

func sessionTestModel(t *testing.T) tuiModel {
	t.Helper()
	t.Chdir(t.TempDir())
	return tuiModel{autoSave: true, textarea: textarea.New(), viewport: viewport.New(70, 10)}
}

func TestSessionLifecycle(t *testing.T) {
	m := sessionTestModel(t)
	m.textarea.SetValue("First question")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	first := m.memoryName
	messages, err := loadMemory(first)
	if err != nil || first == "" || len(messages) != 1 || messages[0].Content != "First question" {
		t.Fatalf("prompt not saved: %v", err)
	}
	updated, _ = m.Update(chatResultMsg{text: "First answer"})
	m = updated.(tuiModel)
	if err := m.newSession("second"); err != nil {
		t.Fatal(err)
	}
	if len(m.messages) != 0 || m.memoryName != "second" || m.system != "" {
		t.Fatal("new session did not reset conversation")
	}
	messages, err = loadMemory(first)
	if err != nil || len(messages) != 2 {
		t.Fatal("previous conversation lost")
	}
	updated, _ = m.handleSlashCommand("/sessions")
	m = updated.(tuiModel)
	if !m.sessionsOpen || !strings.Contains(m.sessionView(), "First question") {
		t.Fatal("session picker missing conversation")
	}
	if err := m.switchSession(first); err != nil {
		t.Fatal(err)
	}
	if m.sessionsOpen || len(m.messages) != 2 || len(m.inputHistory) != 1 || m.inputHistory[0] != "First question" {
		t.Fatal("resume lost messages or input history")
	}
	if err := m.newSession(first); err == nil {
		t.Fatal("new overwrote existing conversation")
	}
	m.loading = true
	if err := m.switchSession("second"); err == nil {
		t.Fatal("switched while streaming")
	}
	m.loading = false
	updated, _ = m.handleSlashCommand("/clear")
	m = updated.(tuiModel)
	messages, err = loadMemory(first)
	if err != nil || len(messages) != 0 {
		t.Fatal("clear did not clear active session")
	}
}

func TestSessionsLegacySortingAndCorruption(t *testing.T) {
	m := sessionTestModel(t)
	if err := saveMemory("legacy", []message{{Role: "user", Content: "Old conversation"}, {Role: "assistant", Content: "Old reply"}}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(memoryPath("legacy"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := saveMemory("recent", []message{{Role: "user", Content: "New conversation"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath("broken"), []byte("invalid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := listMemorySessions()
	if err != nil || len(sessions) != 3 || sessions[2].Name != "legacy" {
		t.Fatalf("sessions=%v err=%v", sessions, err)
	}
	if err := m.switchSession("legacy"); err != nil {
		t.Fatal(err)
	}
	if err := m.switchSession("broken"); err == nil {
		t.Fatal("loaded corrupt conversation")
	}
	if m.memoryName != "legacy" || len(m.messages) != 2 {
		t.Fatal("failed switch lost active conversation")
	}
	if err := m.switchSession("missing"); err == nil {
		t.Fatal("missing session silently created")
	}
	for _, name := range []string{"../outside", `..\outside`, "", "bad\nname"} {
		if err := saveMemory(name, nil); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
}

func TestSessionSaveFailureKeepsConversation(t *testing.T) {
	m := sessionTestModel(t)
	m.messages = []message{{Role: "user", Content: "Unsaved conversation"}}
	if err := os.WriteFile(".hanoi", []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.newSession("next"); err == nil {
		t.Fatal("ignored storage error")
	}
	if len(m.messages) != 1 || m.messages[0].Content != "Unsaved conversation" {
		t.Fatal("storage failure lost conversation")
	}
}

func TestStoppedSessionAndAtomicSave(t *testing.T) {
	m := sessionTestModel(t)
	if err := m.newSession("stopped"); err != nil {
		t.Fatal(err)
	}
	m.messages = []message{{Role: "user", Content: "Question"}}
	m.loading, m.cancelling, m.streaming = true, true, "Partial answer"
	updated, _ := m.Update(chatResultMsg{})
	m = updated.(tuiModel)
	messages, err := loadMemory("stopped")
	if err != nil || len(messages) != 2 || messages[1].Content != "Partial answer" {
		t.Fatalf("stopped turn not saved: %v", err)
	}
	files, err := filepath.Glob(filepath.Join(".hanoi", ".session-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary files left behind")
	}
}
