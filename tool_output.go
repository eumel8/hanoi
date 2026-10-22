package main

import (
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"
)

const maxToolOutputBytes = 32 * 1024

func prepareToolOutput(output string) (string, error) {
	if len(output) <= maxToolOutputBytes {
		return output, nil
	}

	dir := toolResultsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create tool result storage: %w", err)
	}
	file, err := os.CreateTemp(dir, "result-*.txt")
	if err != nil {
		return "", fmt.Errorf("create tool result file: %w", err)
	}
	complete := false
	defer func() {
		file.Close()
		if !complete {
			os.Remove(file.Name())
		}
	}()
	if _, err := file.WriteString(output); err != nil {
		return "", fmt.Errorf("write tool result: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close tool result: %w", err)
	}
	complete = true

	end := maxToolOutputBytes
	for end > 0 && !utf8.RuneStart(output[end]) {
		end--
	}
	return fmt.Sprintf(
		"[Tool output truncated: showing the first %d of %d bytes. Full output saved to %q. Use the bash tool to filter or aggregate that file and return a small result; do not print the entire file. This preview is incomplete and may contain partial JSON.%s Answer once the requested information is available; avoid repeated reformatting of the same dataset. For a list too large to show in chat, provide the saved file path and a concise overview.]\n\n%s\n\n[End of truncated preview; full output: %q]",
		end, len(output), file.Name(), toolOutputJSONHint(output), output[:end], file.Name(),
	), nil
}

func toolOutputJSONHint(output string) string {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(output), &result) == nil && len(result.Content) == 1 && result.Content[0].Type == "text" && json.Valid([]byte(result.Content[0].Text)) {
		return " JSON layout: MCP envelope with JSON encoded in content[0].text. Python: envelope = json.load(open(path)); data = json.loads(envelope['content'][0]['text']). With jq: .content[0].text | fromjson."
	}
	return ""
}
