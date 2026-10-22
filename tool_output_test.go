package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPrepareToolOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{"small", "tool result"},
		{"at limit", strings.Repeat("a", maxToolOutputBytes)},
		{"large", strings.Repeat("a", maxToolOutputBytes+1)},
		{"unicode boundary", strings.Repeat("a", maxToolOutputBytes-1) + strings.Repeat("€", 10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("TMPDIR", t.TempDir())
			t.Setenv("TMP", os.Getenv("TMPDIR"))
			preview, err := prepareToolOutput(tc.output)
			if err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(toolResultsDir(), "result-*.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.output) <= maxToolOutputBytes {
				if preview != tc.output || len(files) != 0 {
					t.Fatal("small output must be unchanged and must not create a file")
				}
				return
			}
			if len(files) != 1 {
				t.Fatalf("expected one result file, got %v", files)
			}
			data, err := os.ReadFile(files[0])
			if err != nil || string(data) != tc.output {
				t.Fatalf("full output was not preserved: %v", err)
			}
			info, err := os.Stat(files[0])
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("unexpected file permissions: %v", info.Mode())
			}
			if !utf8.ValidString(preview) || len(preview) > maxToolOutputBytes+1024 || !strings.Contains(preview, "Tool output truncated") || !strings.Contains(preview, files[0]) {
				t.Fatal("preview must be bounded, valid UTF-8, and identify the full output file")
			}
		})
	}
}

func TestToolOutputStorageFailure(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	if err := os.WriteFile(toolWorkDir(), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareToolOutput(strings.Repeat("a", maxToolOutputBytes+1)); err == nil {
		t.Fatal("expected storage error instead of silently losing full output")
	}
}

func TestMCPJSONReadingHint(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want bool
	}{
		{"JSON array", `[{"status":{"state":"active"}}]`, true},
		{"plain text", "not JSON", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(map[string]any{"content": []any{map[string]string{"type": "text", "text": tc.text}}})
			hint := toolOutputJSONHint(string(data))
			if strings.Contains(hint, "json.loads(envelope['content'][0]['text'])") != tc.want {
				t.Fatalf("unexpected JSON layout hint: %q", hint)
			}
		})
	}
}

func TestLargeBashToolOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	call := toolCall{ID: "call_large", Type: "function"}
	call.Function.Name = "bash"
	call.Function.Arguments = `{"command":"printf '%040000d' 0"}`
	results, err := executeToolCalls([]toolCall{call}, config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ToolCallID != call.ID || results[0].Role != "tool" || !strings.Contains(results[0].Content, "Tool output truncated") || len(results[0].Content) >= 40000 {
		t.Fatalf("large tool output was not bounded or lost its call association: %#v", results)
	}
}

func TestBashToolTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	output, err := runBashToolContext(ctx, `{"command":"printf started; sleep 30 | cat"}`)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got output=%q err=%v", output, err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("timeout must also bound waiting for child process pipes")
	}
}

func TestBashToolWorkingDirectory(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	output, err := runBashTool(`{"command":"pwd -P"}`)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(toolWorkDir())
	if err != nil || strings.TrimSpace(output) != want {
		t.Fatalf("working directory: got %q, want %q, err=%v", output, want, err)
	}
}
