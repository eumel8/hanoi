package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"hanoi/internal/doctor"
	"hanoi/internal/tools"

	tea "github.com/charmbracelet/bubbletea"
)

type doctorResultMsg string

func doctorReportText(text string) string {
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = traceText(lines[i])
	}
	return strings.Join(lines, "\n")
}

func doctorCmd(cfg config, provider bool) tea.Cmd {
	return func() tea.Msg {
		checks := []doctor.Check{{Name: "Model", Status: "OK", Detail: cfg.ModelReference()}}
		if cfg.Model == "" {
			checks[0].Status, checks[0].Detail = "FAIL", "no model configured"
		}
		checks = append(checks, doctor.Local(tools.WorkDir(), tools.ResultsDir())...)
		for _, file := range []struct{ name, path string }{{"Configuration", cfg.ConfigPath}, {"API log", cfg.LogPath}, {"Trace file", cfg.TracePath}} {
			detail := file.path
			if detail != "" {
				if absolute, err := filepath.Abs(detail); err == nil {
					detail = absolute
				}
			} else if file.name == "Configuration" {
				detail = "environment / .env (no config file)"
			} else {
				detail = "disabled"
			}
			checks = append(checks, doctor.Check{Name: file.name, Status: "INFO", Detail: detail})
		}
		checks = append(checks, doctorMCPChecks(cfg.MCP)...)
		if provider {
			checks = append(checks, probeProvider(cfg))
		} else {
			checks = append(checks, doctor.Check{Name: "Provider connection", Status: "SKIP", Detail: "Use /doctor provider for a model request (20s timeout)."})
		}
		return doctorResultMsg(doctor.Render(checks))
	}
}

func doctorMCPChecks(manager *mcpManager) []doctor.Check {
	if manager == nil {
		return []doctor.Check{{Name: "MCP", Status: "INFO", Detail: "not initialized"}}
	}
	names := manager.names()
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(names) == 0 {
		return []doctor.Check{{Name: "MCP", Status: "INFO", Detail: "no servers configured"}}
	}
	var checks []doctor.Check
	for _, name := range names {
		server := manager.servers[name]
		if server == nil {
			continue
		}
		check := doctor.Check{Name: "MCP " + name, Status: "WARN", Detail: fmt.Sprintf("%s · %d tools", server.status, len(server.tools))}
		if server.status == "connected" && server.session != nil {
			check.Status = "OK"
		} else if server.config.Disabled {
			check.Status = "SKIP"
		}
		if server.authStatus != "" {
			check.Detail += " · OAuth: " + server.authStatus
		}
		for _, tool := range server.tools {
			check.Detail += "\n  " + mcpToolName(name, tool.Name)
		}
		checks = append(checks, check)
	}
	return checks
}

// probeProvider sends a small, tool-free request. It deliberately avoids API
// logging and raw error bodies/URLs, which can expose credentials.
func probeProvider(cfg config) doctor.Check {
	check := doctor.Check{Name: "Provider connection", Status: "FAIL"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	payload, err := json.Marshal(chatRequest{Model: cfg.Model, Messages: []message{{Role: "user", Content: "Reply with OK."}}})
	if err != nil {
		check.Detail = "could not encode test request"
		return check
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		check.Detail = "invalid provider endpoint"
		return check
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for name, value := range cfg.Headers {
		req.Header.Set(name, value)
	}
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		check.Detail = "request failed (connection, TLS or timeout)"
		return check
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		check.Detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return check
	}
	var result chatResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&result); err != nil || len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
		check.Detail = "response is not a usable chat completion"
		return check
	}
	check.Status, check.Detail = "OK", fmt.Sprintf("model response received in %s", time.Since(started).Round(time.Millisecond))
	return check
}
