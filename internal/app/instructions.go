package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// loadAgentInstructions loads the closest AGENTS.md from the launch directory
// or its ancestors. Nested directories are not scanned proactively.
func loadAgentInstructions(workspace string) (text, path string, err error) {
	dir, err := filepath.Abs(workspace)
	if err != nil {
		return "", "", err
	}
	for {
		path = filepath.Join(dir, "AGENTS.md")
		file, openErr := os.Open(path)
		if openErr == nil {
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				return "", "", fmt.Errorf("read %s: %w", path, err)
			}
			if !info.Mode().IsRegular() {
				return "", "", fmt.Errorf("%s must be a regular file", path)
			}
			const maxSize = 1024 * 1024
			data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
			if err != nil {
				return "", "", fmt.Errorf("read %s: %w", path, err)
			}
			if len(data) > maxSize || !utf8.Valid(data) {
				return "", "", fmt.Errorf("%s must be UTF-8 and at most 1 MiB", path)
			}
			text = strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff"))
			if text == "" {
				return "", path, nil
			}
			return fmt.Sprintf("Project instructions from %s\nBase directory for relative references: %s\n\n%s", path, dir, text), path, nil
		}
		if !os.IsNotExist(openErr) {
			return "", "", fmt.Errorf("open %s: %w", path, openErr)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil
		}
		dir = parent
	}
}

// sessionSystem restores the manual prompt without persisting AGENTS.md content.
func sessionSystem(messages []message, fallback string) string {
	for _, msg := range messages {
		if msg.Role == "system" {
			return msg.Content
		}
	}
	return fallback
}

const systemPromptPath = ".hanoi/system.md"

func loadSystemPrompt() (string, error) {
	file, err := os.Open(systemPromptPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open system prompt: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return "", fmt.Errorf("read system prompt: %w", err)
	}
	if err := validateSystemPrompt(string(data)); err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "\ufeff")), nil
}

func validateSystemPrompt(text string) error {
	if len(text) > 1024*1024 || !utf8.ValidString(text) {
		return fmt.Errorf("system prompt must be UTF-8 and at most 1 MiB")
	}
	return nil
}

// saveSystemPrompt uses the same private temporary-file/rename pattern as sessions.
// Clearing writes an empty file so no unrelated files need to be removed.
func saveSystemPrompt(text string) error {
	if err := validateSystemPrompt(text); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(systemPromptPath), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(systemPromptPath), ".system-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(text); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), systemPromptPath)
}
