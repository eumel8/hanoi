package app

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
)

func TestVoiceTranscriptReview(t *testing.T) {
	m := tuiModel{textarea: textarea.New(), voiceState: "transcribing"}
	m.textarea.SetValue("draft")
	updated, _ := m.Update(voiceEvent{Type: "transcript", Text: "Hallo Hanoi"})
	m = updated.(tuiModel)
	if m.textarea.Value() != "Hallo Hanoi" || m.voiceState != "" || len(m.messages) != 0 {
		t.Fatalf("transcript should be an editable draft: %+v", m)
	}
	updated, _ = m.Update(voiceEvent{Type: "transcript", Text: "  "})
	m = updated.(tuiModel)
	if m.textarea.Value() != "Hallo Hanoi" {
		t.Fatal("empty transcript must not erase the draft")
	}
	m.loading = true
	updated, _ = m.Update(voiceEvent{Type: "transcript", Text: "late"})
	m = updated.(tuiModel)
	if m.textarea.Value() != "Hallo Hanoi" || len(m.messages) != 0 {
		t.Fatal("busy chat must not send or replace input")
	}
}

func TestVoiceCaptureArgs(t *testing.T) {
	for _, tc := range []struct{ platform, input, want string }{
		{"windows", "Microphone (USB Audio)", "audio=Microphone (USB Audio)"},
		{"darwin", "0", ":0"},
	} {
		args, err := voiceCaptureArgs(tc.platform, tc.input)
		if err != nil || len(args) != 4 || args[3] != tc.want {
			t.Fatalf("%s capture args = %q, %v", tc.platform, args, err)
		}
	}
	if _, err := voiceCaptureArgs("linux", "0"); err == nil {
		t.Fatal("unsupported audio platform must be rejected")
	}
}
