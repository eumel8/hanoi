package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type mcpResultMsg struct {
	text string
	err  error
}

func (m *mcpManager) rows() []string {
	names := m.names()
	m.mu.RLock()
	defer m.mu.RUnlock()
	rows := make([]string, 0, len(names))
	for _, name := range names {
		server := m.servers[name]
		if server == nil {
			continue
		}
		row := fmt.Sprintf("%s · %s · %s · %d tools", name, server.config.Type, server.status, len(server.tools))
		if server.authStatus != "" {
			row += " · OAuth: " + server.authStatus
		}
		if server.err != "" {
			row += "\n    " + server.err
		}
		if server.authURL != "" {
			row += "\n    Open in browser: " + server.authURL + "\n    Esc cancels login."
		}
		rows = append(rows, row)
	}
	return rows
}

func (m *mcpManager) toolSummary(name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, ok := m.servers[name]
	if !ok {
		return "", fmt.Errorf("unknown MCP server %q", name)
	}
	var lines []string
	for _, tool := range server.tools {
		lines = append(lines, fmt.Sprintf("%s\n  %s\n  API name: %s", tool.Name, tool.Description, mcpToolName(name, tool.Name)))
	}
	if len(lines) == 0 {
		return name + ": no tools available (" + server.status + ")", nil
	}
	return strings.Join(lines, "\n"), nil
}

func (m tuiModel) mcpView() string {
	lines := []string{headStyle.Render("MCP servers"), "↑/↓ select · Enter toggle · r reconnect · t tools · a login · l logout · Ctrl+R reload · Esc close/cancel", ""}
	if m.cfg.MCP == nil {
		return strings.Join(append(lines, "MCP is not initialized."), "\n")
	}
	m.cfg.MCP.mu.RLock()
	path := m.cfg.MCP.path
	m.cfg.MCP.mu.RUnlock()
	if path == "" {
		path = "no config found; create hanoi.json, then Ctrl+R"
	}
	lines = append(lines, "Config: "+path, "")
	rows := m.cfg.MCP.rows()
	if len(rows) == 0 {
		lines = append(lines, "No MCP servers configured.")
	}
	for index, row := range rows {
		prefix := "  "
		if index == m.mcpCursor {
			prefix = "> "
		}
		lines = append(lines, prefix+row)
	}
	if m.mcpInfo != "" {
		lines = append(lines, "", m.mcpInfo)
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) startMCPAction(action, name string) (tea.Model, tea.Cmd) {
	if m.cfg.MCP == nil {
		m.mcpInfo = "MCP is not initialized"
		return m, nil
	}
	m.mcpBusy, m.mcpInfo, m.err = true, "", ""
	manager := m.cfg.MCP
	return m, tea.Batch(m.spinner.Tick, func() tea.Msg {
		var err error
		switch action {
		case "enable", "reconnect":
			err = manager.Enable(name)
		case "disable":
			err = manager.Disable(name)
		case "reload":
			err = manager.Reload()
		case "auth", "login":
			err = manager.Login(name)
		case "logout":
			err = manager.Logout(name)
		}
		return mcpResultMsg{text: "MCP " + action + " completed.", err: err}
	})
}

func (m tuiModel) handleMCPCommand(arg string) (tea.Model, tea.Cmd) {
	m.mcpOpen, m.err, m.mcpInfo = true, "", ""
	parts := strings.SplitN(strings.TrimSpace(arg), " ", 2)
	action := parts[0]
	name := ""
	if len(parts) == 2 {
		name = strings.TrimSpace(parts[1])
	}
	if m.cfg.MCP == nil {
		m.mcpInfo = "MCP is not initialized"
		return m, nil
	}
	switch action {
	case "", "list":
		return m, nil
	case "reload":
		return m.startMCPAction(action, "")
	case "enable", "disable", "reconnect", "tools", "auth", "login", "logout":
		if name == "" {
			m.mcpInfo = "usage: /mcps " + action + " <name>"
			return m, nil
		}
		if action == "tools" {
			text, err := m.cfg.MCP.toolSummary(name)
			m.mcpInfo = text
			if err != nil {
				m.mcpInfo = err.Error()
			}
			return m, nil
		}
		return m.startMCPAction(action, name)
	default:
		m.mcpInfo = "usage: /mcps [list|reload|enable <name>|disable <name>|reconnect <name>|tools <name>|auth <name>|logout <name>]"
		return m, nil
	}
}

func (m tuiModel) updateMCPMenu(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "esc" || key.String() == "q" {
		m.mcpOpen = false
		return m, nil
	}
	if m.cfg.MCP == nil {
		return m, nil
	}
	names := m.cfg.MCP.names()
	if m.mcpCursor >= len(names) {
		m.mcpCursor = max(0, len(names)-1)
	}
	switch key.String() {
	case "up", "k":
		m.mcpCursor = max(0, m.mcpCursor-1)
	case "down", "j":
		if m.mcpCursor+1 < len(names) {
			m.mcpCursor++
		}
	case "ctrl+r":
		return m.startMCPAction("reload", "")
	case "enter", "r", "t", "a", "l":
		if len(names) == 0 {
			return m, nil
		}
		name := names[m.mcpCursor]
		if key.String() == "a" {
			return m.startMCPAction("auth", name)
		}
		if key.String() == "l" {
			return m.startMCPAction("logout", name)
		}
		if key.String() == "t" {
			m.mcpInfo, _ = m.cfg.MCP.toolSummary(name)
			return m, nil
		}
		action := "reconnect"
		if key.String() == "enter" {
			m.cfg.MCP.mu.RLock()
			connected := m.cfg.MCP.servers[name].session != nil
			needsAuth := m.cfg.MCP.servers[name].status == "needs_auth"
			m.cfg.MCP.mu.RUnlock()
			action = "enable"
			if connected {
				action = "disable"
			}
			if needsAuth {
				action = "auth"
			}
		}
		return m.startMCPAction(action, name)
	}
	return m, nil
}
