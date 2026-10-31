package app

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestWhisperArgs(t *testing.T) {
	base := []string{"-m", "ggml-small.bin", "-f", "audio.wav", "-l", "de", "-nt", "-otxt", "-of", "output"}
	options := voiceOptions{whisper: "ggml-small.bin"}
	if got := whisperArgs(options, "audio.wav", "output"); !reflect.DeepEqual(got, base) {
		t.Fatalf("default Whisper args = %q", got)
	}
	options.prompt = "  Hanoi, Copilot, MCP.  "
	want := append(append([]string(nil), base...), "--prompt", "Hanoi, Copilot, MCP.")
	if got := whisperArgs(options, "audio.wav", "output"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Whisper context args = %q, want %q", got, want)
	}
}

func TestCopyVoiceDebugFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "recording.wav")
	target := filepath.Join(dir, "saved.wav")
	if err := os.WriteFile(source, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyVoiceDebugFile(source, target); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "audio" {
		t.Fatalf("debug recording = %q, %v", data, err)
	}
	if err := copyVoiceDebugFile(source, target); !os.IsExist(err) {
		t.Fatalf("existing debug recording must not be overwritten: %v", err)
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
