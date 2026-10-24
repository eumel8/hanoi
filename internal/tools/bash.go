// Package tools provides local Bash execution and bounded tool-result storage.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// RunBash executes a JSON-encoded command with the default 30-second timeout.
func RunBash(rawArgs string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return RunBashContext(ctx, rawArgs)
}

// RunBashContext executes a JSON-encoded command in the tool working directory.
func RunBashContext(ctx context.Context, rawArgs string) (string, error) {
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil {
		return "", fmt.Errorf("decode bash arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("bash command is empty")
	}
	if err := os.MkdirAll(WorkDir(), 0o700); err != nil {
		return "", fmt.Errorf("create tool working directory: %w", err)
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", args.Command)
	configureBashProcess(cmd)
	cmd.WaitDelay = time.Second
	cmd.Dir = WorkDir()
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return string(output), fmt.Errorf("bash command stopped: %w; use a narrower command or search only the saved tool-results directory", ctx.Err())
	}
	return string(output), err
}
