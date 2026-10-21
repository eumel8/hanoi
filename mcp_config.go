package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	appconfig "hanoi/internal/config"

	"github.com/tailscale/hujson"
)

type mcpTimeouts struct {
	Startup   int `json:"startup,omitempty"`
	Catalog   int `json:"catalog,omitempty"`
	Execution int `json:"execution,omitempty"`
}

// OpenCode accepts a millisecond integer in legacy entries and an object in mcp.servers.
func (t *mcpTimeouts) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("MCP timeout must be a positive integer or object")
	}
	if data[0] != '{' {
		var ms int
		if err := json.Unmarshal(data, &ms); err != nil || ms <= 0 {
			return fmt.Errorf("MCP timeout must be positive milliseconds")
		}
		*t = mcpTimeouts{ms, ms, ms}
		return nil
	}
	type plain mcpTimeouts
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		var ms int
		if name != "startup" && name != "catalog" && name != "execution" {
			return fmt.Errorf("unknown MCP timeout field %q", name)
		}
		if err := json.Unmarshal(value, &ms); err != nil || ms <= 0 {
			return fmt.Errorf("MCP timeout.%s must be positive milliseconds", name)
		}
	}
	return json.Unmarshal(data, (*plain)(t))
}

func (t mcpTimeouts) withDefaults(parent mcpTimeouts) mcpTimeouts {
	if t.Startup == 0 {
		t.Startup = parent.Startup
	}
	if t.Catalog == 0 {
		t.Catalog = parent.Catalog
	}
	if t.Execution == 0 {
		t.Execution = parent.Execution
	}
	return t
}

func milliseconds(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

type mcpServerConfig struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command"`
	CWD         string            `json:"cwd"`
	Environment map[string]string `json:"environment"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers"`
	OAuth       json.RawMessage   `json:"oauth,omitempty"`
	Enabled     *bool             `json:"enabled"`
	Disabled    bool              `json:"disabled"`
	Timeout     mcpTimeouts       `json:"timeout,omitzero"`
	Protocol    string            `json:"protocol"`
	Codemode    bool              `json:"codemode"`
}

func loadMCPConfig(path string) (map[string]mcpServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err = hujson.Standardize(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var root struct {
		MCP map[string]json.RawMessage `json:"mcp"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	servers := make(map[string]mcpServerConfig)
	defaults := mcpTimeouts{5000, 5000, 60000}
	if raw, ok := root.MCP["timeout"]; ok {
		var configured mcpTimeouts
		if err := json.Unmarshal(raw, &configured); err != nil {
			return nil, err
		}
		defaults = configured.withDefaults(defaults)
	}
	for name, raw := range root.MCP {
		if name == "timeout" || name == "servers" {
			continue
		}
		var entry mcpServerConfig
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, fmt.Errorf("mcp.%s: %w", name, err)
		}
		servers[name] = entry
	}
	if raw, ok := root.MCP["servers"]; ok {
		var current map[string]mcpServerConfig
		if err := json.Unmarshal(raw, &current); err != nil {
			return nil, fmt.Errorf("mcp.servers: %w", err)
		}
		for name, entry := range current {
			servers[name] = entry
		}
	}
	for name, entry := range servers {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("MCP server name is empty")
		}
		if entry.Enabled != nil && !*entry.Enabled {
			entry.Disabled = true
		}
		entry.Timeout = entry.Timeout.withDefaults(defaults)
		switch entry.Type {
		case "local":
			if len(entry.Command) == 0 || entry.Command[0] == "" {
				return nil, fmt.Errorf("MCP %s: command is required", name)
			}
		case "remote":
			if entry.URL == "" {
				return nil, fmt.Errorf("MCP %s: url is required", name)
			}
		case "":
			if !entry.Disabled {
				return nil, fmt.Errorf("MCP %s: server definition is missing (type/command or url)", name)
			}
		default:
			return nil, fmt.Errorf("MCP %s: unknown type %q", name, entry.Type)
		}
		switch entry.Protocol {
		case "", "legacy", "auto", "2026-07-28":
		default:
			return nil, fmt.Errorf("MCP %s: unsupported protocol %q", name, entry.Protocol)
		}
		servers[name] = entry
	}
	return servers, nil
}

func expandMCPServer(entry mcpServerConfig) (mcpServerConfig, error) {
	var err error
	expand := func(value string) string {
		if err != nil {
			return value
		}
		var out string
		out, err = appconfig.ExpandEnv(value)
		return out
	}
	entry.Command = append([]string{}, entry.Command...)
	for i, value := range entry.Command {
		entry.Command[i] = expand(value)
	}
	entry.CWD, entry.URL = expand(entry.CWD), expand(entry.URL)
	env, headers := make(map[string]string), make(map[string]string)
	for key, value := range entry.Environment {
		env[key] = expand(value)
	}
	for key, value := range entry.Headers {
		headers[key] = expand(value)
	}
	entry.Environment, entry.Headers = env, headers
	if err != nil {
		return entry, err
	}
	if entry.Type == "remote" {
		u, err := url.Parse(entry.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return entry, fmt.Errorf("remote MCP url must be http:// or https://")
		}
	}
	return entry, nil
}
