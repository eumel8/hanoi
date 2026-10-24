package app

import (
	"os"
	"strings"
	"testing"
)

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
