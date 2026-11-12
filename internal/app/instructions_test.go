package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appconfig "hanoi/internal/config"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestSystemMessagesNormalizedForProvider(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			messages := []message{
				datedMessage(message{Role: "user", Content: "Earlier question"}),
				{Role: "system", Content: "Manual rules"},
				{Role: "assistant", Content: "Earlier answer"},
				{Role: "system", Content: ""},
				{Role: "system", Content: "Additional rules"},
				{Role: "user", Content: "Next question"},
			}
			original := append([]message(nil), messages...)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request chatRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				want := []message{
					{Role: "system", Content: "Project rules\n\nManual rules\n\nAdditional rules"},
					messages[0], messages[2], messages[5],
				}
				want[1].Timestamp = nil
				if !reflect.DeepEqual(request.Messages, want) {
					t.Errorf("unexpected provider messages: %+v", request.Messages)
					http.Error(w, "System message must be at the beginning.", 400)
					return
				}
				if stream {
					writeStreamChoice(w, map[string]any{"content": "done"}, "stop")
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"done"}}]}`)
				}
			}))
			defer server.Close()
			cfg := config{Config: appconfig.Config{Model: "test", BaseURL: server.URL}, AgentInstructions: "Project rules"}
			msg, err := requestModel(cfg, messages, stream, nil)
			if err != nil || msg.Content != "done" {
				t.Fatalf("response=%q err=%v", msg.Content, err)
			}
			if !reflect.DeepEqual(messages, original) {
				t.Fatal("request normalization changed session history")
			}
		})
	}
}

func TestAgentInstructionsDiscovery(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project", "subproject")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(dir, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(root, "Parent rules")
	text, path, err := loadAgentInstructions(child)
	if err != nil || path != filepath.Join(root, "AGENTS.md") || !strings.Contains(text, "Parent rules") || !strings.Contains(text, root) {
		t.Fatalf("ancestor instructions: %q, %q, %v", text, path, err)
	}
	write(child, "\ufeffLocal rules")
	text, path, err = loadAgentInstructions(child)
	if err != nil || path != filepath.Join(child, "AGENTS.md") || !strings.Contains(text, "Local rules") || strings.Contains(text, "Parent rules") {
		t.Fatalf("closest instructions: %q, %q, %v", text, path, err)
	}
	write(child, " \n")
	text, path, err = loadAgentInstructions(child)
	if err != nil || text != "" || path != filepath.Join(child, "AGENTS.md") {
		t.Fatalf("empty nearest file: %q, %q, %v", text, path, err)
	}
	for _, content := range []string{string([]byte{0xff}), strings.Repeat("x", 1024*1024+1)} {
		write(child, content)
		if _, _, err := loadAgentInstructions(child); err == nil {
			t.Fatal("invalid instructions silently accepted")
		}
	}
	if err := os.Remove(filepath.Join(child, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(child, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadAgentInstructions(child); err == nil {
		t.Fatal("directory accepted as instructions")
	}
}

func TestAgentInstructionsMissing(t *testing.T) {
	// Start at a filesystem root to avoid inheriting developer-specific rules.
	root := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	if _, err := os.Stat(filepath.Join(root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Skip("filesystem root has an AGENTS.md or is inaccessible")
	}
	text, path, err := loadAgentInstructions(root)
	if err != nil || text != "" || path != "" {
		t.Fatalf("missing instructions: %q, %q, %v", text, path, err)
	}
}

func TestSystemCommandFeedbackPersistenceAndClear(t *testing.T) {
	m := sessionTestModel(t)
	m.width, m.height = 100, 40
	m.cfg.AgentInstructions, m.cfg.AgentInstructionsPath = "Project rules", "/project/AGENTS.md"
	if err := m.newSession("system"); err != nil {
		t.Fatal(err)
	}
	m.messages = []message{{Role: "user", Content: "Previous question"}}
	for _, step := range []struct{ input, want string }{
		{"/system Answer in German.", "Answer in German."},
		{"/system Answer concisely.", "Answer concisely."},
		{"/system", ""},
	} {
		m.textarea.SetValue(step.input)
		updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(tuiModel)
		if cmd != nil || m.loading || m.system != step.want || m.textarea.Value() != "" || !strings.Contains(m.View(), "System prompt") {
			t.Fatalf("command did not update visibly: %q, system=%q input=%q status=%q", step.input, m.system, m.textarea.Value(), m.err)
		}
		if lipgloss.Height(m.View()) > m.height {
			t.Fatal("system prompt header causes layout to exceed terminal height")
		}
		saved, err := loadMemory("system")
		if err != nil || sessionSystem(saved, "") != step.want {
			t.Fatalf("system prompt not saved: %v, %v", saved, err)
		}
		m.activateSession("system", saved)
		if m.system != step.want || m.cfg.AgentInstructions != "Project rules" {
			t.Fatal("session activation lost manual or project instructions")
		}
	}
	if len(m.messages) != 2 || m.messages[0].Role != "system" || m.messages[0].Content != "" || m.messages[1].Role != "user" {
		t.Fatalf("clearing left stale system messages or lost history: %v", m.messages)
	}
	m.textarea.SetValue("Next question")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	defer m.chatCancel()
	for _, msg := range m.pendingMessage {
		if msg.Role == "system" && msg.Content != "" {
			t.Fatal("cleared system prompt returned on next turn")
		}
	}
}

func TestSystemPromptWorkspacePersistence(t *testing.T) {
	m := sessionTestModel(t)
	if prompt, err := loadSystemPrompt(); err != nil || prompt != "" {
		t.Fatalf("missing prompt: %q, %v", prompt, err)
	}
	updated, _ := m.handleSlashCommand("/system Answer in German.")
	m = updated.(tuiModel)
	prompt, err := loadSystemPrompt()
	if err != nil || prompt != "Answer in German." || m.cfg.SystemPrompt != prompt {
		t.Fatalf("workspace prompt not saved: %q, %v", prompt, err)
	}
	if err := m.newSession("new"); err != nil {
		t.Fatal(err)
	}
	if m.system != prompt || sessionSystem(nil, prompt) != prompt {
		t.Fatal("new session or CLI default lost workspace prompt")
	}
	m.activateSession("old", []message{{Role: "system", Content: "Session rules"}})
	if m.system != "Session rules" {
		t.Fatal("workspace prompt replaced session-specific prompt")
	}
	m.activateSession("cleared", []message{{Role: "system", Content: ""}})
	if m.system != "" {
		t.Fatal("workspace prompt replaced explicitly cleared session prompt")
	}
	if err := saveSystemPrompt("New default"); err != nil {
		t.Fatal(err)
	}
	if prompt, err := loadSystemPrompt(); err != nil || prompt != "New default" {
		t.Fatalf("replacing existing prompt failed: %q, %v", prompt, err)
	}
	files, err := filepath.Glob(filepath.Join(".hanoi", ".system-*"))
	if err != nil || len(files) != 0 {
		t.Fatal("temporary prompt files left behind")
	}
	if err := saveSystemPrompt(strings.Repeat("x", 1024*1024+1)); err == nil {
		t.Fatal("oversized prompt accepted")
	}
	if err := os.WriteFile(systemPromptPath, []byte{0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSystemPrompt(); err == nil {
		t.Fatal("invalid UTF-8 prompt accepted")
	}
}

func TestSystemPromptSaveFailureKeepsActivePrompt(t *testing.T) {
	m := sessionTestModel(t)
	m.system = "Original prompt"
	if err := os.WriteFile(".hanoi", []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, _ := m.handleSlashCommand("/system Replacement")
	m = updated.(tuiModel)
	if m.system != "Original prompt" || !strings.Contains(m.err, "save system prompt") {
		t.Fatal("failed save changed the active prompt or hid the error")
	}
}
