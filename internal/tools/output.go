package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"unicode/utf8"
)

const maxToolOutputBytes = 32 * 1024

// outputCapture keeps only a bounded preview in memory. Once the limit is
// crossed, subsequent writes go straight to disk, including the original prefix.
// exec.Cmd serializes writes when Stdout and Stderr share this writer.
type outputCapture struct {
	preview bytes.Buffer
	file    *os.File
	total   int64
}

func (c *outputCapture) Write(p []byte) (int, error) {
	if c.file == nil && c.preview.Len()+len(p) > maxToolOutputBytes {
		if err := os.MkdirAll(ResultsDir(), 0o700); err != nil {
			return 0, err
		}
		file, err := os.CreateTemp(ResultsDir(), "result-*.txt")
		if err != nil {
			return 0, err
		}
		c.file = file
		if _, err := file.Write(c.preview.Bytes()); err != nil {
			return 0, err
		}
	}
	if c.file != nil {
		n, err := c.file.Write(p)
		c.total += int64(n)
		if remaining := maxToolOutputBytes - c.preview.Len(); remaining > 0 {
			if n < remaining {
				remaining = n
			}
			c.preview.Write(p[:remaining])
		}
		return n, err
	}
	n, err := c.preview.Write(p)
	c.total += int64(n)
	return n, err
}

func (c *outputCapture) result() (string, error) {
	if c.file == nil {
		return c.preview.String(), nil
	}
	if err := c.file.Close(); err != nil {
		return "", fmt.Errorf("close tool result: %w", err)
	}
	preview := c.preview.String()
	if len(preview) > 24*1024 {
		preview = preview[:24*1024]
	}
	return truncatedToolOutput(preview, c.total, c.file.Name(), ""), nil
}

func truncatedToolOutput(preview string, total int64, path, hint string) string {
	end := len(preview)
	if end > 0 && !utf8.ValidString(preview) {
		preview = string(bytes.ToValidUTF8([]byte(preview), []byte("�")))
		end = len(preview)
	}
	return fmt.Sprintf(
		"[Tool output truncated: showing the first %d of %d bytes. Full output saved to %q. Use the bash tool to filter or aggregate that file and return a small result; do not print the entire file. This preview is incomplete and may contain partial JSON.%s Answer once the requested information is available; avoid repeated reformatting of the same dataset. For a list too large to show in chat, provide the saved file path and a concise overview.]\n\n%s\n\n[End of truncated preview; full output: %q]",
		end, total, path, hint, preview[:end], path,
	)
}

func PrepareOutput(output string) (string, error) {
	if len(output) <= maxToolOutputBytes {
		return output, nil
	}

	dir := ResultsDir()
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
	return truncatedToolOutput(output[:end], int64(len(output)), file.Name(), toolOutputJSONHint(output)), nil
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
