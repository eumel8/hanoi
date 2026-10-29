package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
)

type memorySession struct {
	Name     string
	Title    string
	Updated  time.Time
	Messages int
	Error    string
}

func validateMemoryName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:*?"<>|`) {
		return fmt.Errorf("session name must be a nonempty filename without path separators or reserved characters")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("session name contains control characters")
		}
	}
	return nil
}

func newMemoryName() (string, error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return "session-" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix[:]), nil
}

func listMemorySessions() ([]memorySession, error) {
	entries, err := os.ReadDir(".hanoi")
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var sessions []memorySession
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if validateMemoryName(name) != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		session := memorySession{Name: name, Title: "Empty conversation", Updated: info.ModTime()}
		messages, err := loadMemory(name)
		if err != nil {
			session.Error = "unreadable session"
			session.Title = "Unreadable conversation"
		} else {
			session.Messages = len(messages)
			for _, msg := range messages {
				if msg.Role == "user" {
					title := []rune(strings.Join(strings.Fields(msg.Content), " "))
					if len(title) > 70 {
						title = append(title[:70], '…')
					}
					session.Title = string(title)
					break
				}
			}
		}
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Updated.Equal(sessions[j].Updated) {
			return sessions[i].Name < sessions[j].Name
		}
		return sessions[i].Updated.After(sessions[j].Updated)
	})
	return sessions, nil
}

func (m *tuiModel) preserveSession() error {
	if m.loading {
		return fmt.Errorf("Stop the current response before switching sessions.")
	}
	if m.memoryName != "" {
		return saveMemory(m.memoryName, m.messages)
	}
	if len(m.messages) == 0 {
		return nil
	}
	name, err := newMemoryName()
	if err != nil {
		return err
	}
	if err := saveMemory(name, m.messages); err != nil {
		return err
	}
	m.memoryName = name
	return nil
}

func (m *tuiModel) newSession(name string) error {
	if name == "" {
		var err error
		name, err = newMemoryName()
		if err != nil {
			return err
		}
	}
	if err := validateMemoryName(name); err != nil {
		return err
	}
	if _, err := os.Stat(memoryPath(name)); err == nil {
		return fmt.Errorf("session %q already exists; use /sessions %s", name, name)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := m.preserveSession(); err != nil {
		return err
	}
	if err := saveMemory(name, nil); err != nil {
		return err
	}
	m.activateSession(name, nil)
	return nil
}

func (m *tuiModel) switchSession(name string) error {
	if err := validateMemoryName(name); err != nil {
		return err
	}
	if m.loading {
		return fmt.Errorf("Stop the current response before switching sessions.")
	}
	if name == m.memoryName {
		m.sessionsOpen = false
		return nil
	}
	if _, err := os.Stat(memoryPath(name)); err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	messages, err := loadMemory(name)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if err := m.preserveSession(); err != nil {
		return err
	}
	m.activateSession(name, messages)
	return nil
}

func (m *tuiModel) activateSession(name string, messages []message) {
	m.memoryName, m.messages, m.autoSave = name, messages, true
	m.pendingMessage = nil
	m.system, m.streaming, m.streamRendered, m.err = "", "", "", ""
	m.sessionsOpen, m.traceOpen, m.doctorOpen = false, false, false
	m.traceEvents = nil
	m.userScrolled = false
	m.chatOffset = 0
	m.inputHistory = nil
	m.historyIndex = 0
	m.historyDraft = ""
	m.textarea.SetValue("")
	for _, msg := range messages {
		if msg.Role == "user" {
			m.rememberInput(msg.Content)
		}
	}
	m.rebuildHistory()
	m.viewport.GotoBottom()
}

func (m tuiModel) sessionView() string {
	lines := []string{headStyle.Render("Sessions"), "↑/↓ select · Enter resume · n new session · Esc close", ""}
	if len(m.sessions) == 0 {
		lines = append(lines, "No saved sessions. Use /new [name] or send a message to start one.")
	}
	visible := len(m.sessions)
	if m.height > 0 {
		visible = max(1, (m.height-14)/2)
	}
	start := max(0, m.sessionCursor-visible/2)
	if start+visible > len(m.sessions) {
		start = max(0, len(m.sessions)-visible)
	}
	for i := start; i < len(m.sessions) && i < start+visible; i++ {
		s := m.sessions[i]
		prefix := "  "
		if i == m.sessionCursor {
			prefix = "> "
		}
		active := ""
		if s.Name == m.memoryName {
			active = " (active)"
		}
		lines = append(lines, prefix+s.Title+active, fmt.Sprintf("  %s · %s · %d messages", s.Name, s.Updated.Format("2006-01-02 15:04"), s.Messages))
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) updateSessionMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc", "q":
		m.sessionsOpen = false
	case "up", "k":
		m.sessionCursor = max(0, m.sessionCursor-1)
	case "down", "j":
		if m.sessionCursor+1 < len(m.sessions) {
			m.sessionCursor++
		}
	case "n":
		if err := m.newSession(""); err != nil {
			m.err = err.Error()
		}
	case "enter":
		if m.sessionCursor < len(m.sessions) {
			if err := m.switchSession(m.sessions[m.sessionCursor].Name); err != nil {
				m.err = err.Error()
			}
		}
	}
	return m, nil
}
