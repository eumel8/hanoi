package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	appconfig "hanoi/internal/config"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpServerState struct {
	config        mcpServerConfig
	session       *mcp.ClientSession
	tools         []*mcp.Tool
	status        string
	err           string
	authStatus    string
	authURL       string
	authChallenge *mcpOAuthChallenge
}

type mcpManager struct {
	opMu             sync.Mutex // Serialize lifecycle changes, without blocking status reads.
	mu               sync.RWMutex
	servers          map[string]*mcpServerState
	path             string
	explicitPath     string
	logger           *log.Logger
	oauthDir         string
	openBrowser      func(string) error
	authCtx          context.Context
	authCancel       context.CancelFunc
	authName         string
	authCallbackDone chan struct{}
}

func newMCPManager(path string, logger *log.Logger) (*mcpManager, error) {
	m := &mcpManager{servers: make(map[string]*mcpServerState), explicitPath: path, logger: logger}
	if err := m.Reload(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *mcpManager) logf(format string, args ...any) {
	if m.logger != nil {
		m.logger.Printf("mcp "+format, args...)
	}
}

func (m *mcpManager) names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (m *mcpManager) Reload() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	path, err := appconfig.FindFile(m.explicitPath)
	if err != nil {
		return err
	}
	entries := make(map[string]mcpServerConfig)
	if path != "" {
		entries, err = loadMCPConfig(path)
		if err != nil {
			return err
		} // Keep working connections if the new config is invalid.
	}
	m.closeSessions()
	m.mu.Lock()
	m.path = path
	m.servers = make(map[string]*mcpServerState)
	for name, entry := range entries {
		status := "disconnected"
		if entry.Disabled {
			status = "disabled"
		}
		m.servers[name] = &mcpServerState{config: entry, status: status}
	}
	m.mu.Unlock()
	for _, name := range m.names() {
		if !entries[name].Disabled {
			_ = m.connect(name)
		}
	}
	return nil
}

func (m *mcpManager) closeSessions() {
	m.mu.Lock()
	var sessions []*mcp.ClientSession
	for _, server := range m.servers {
		if server.session != nil {
			sessions = append(sessions, server.session)
		}
		server.session, server.tools = nil, nil
	}
	m.mu.Unlock()
	for _, session := range sessions {
		_ = session.Close()
	}
}

func (m *mcpManager) Close() {
	m.CancelAuth()
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.closeSessions()
}

type mcpHeaderTransport struct {
	base    http.RoundTripper
	headers map[string]string
	origin  string
}

func (t mcpHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if req.URL.Scheme+"://"+req.URL.Host == t.origin {
		for key, value := range t.headers {
			clone.Header.Set(key, value)
		}
	}
	return t.base.RoundTrip(clone)
}

type mcpStderr struct {
	manager *mcpManager
	name    string
}

func (w mcpStderr) Write(p []byte) (int, error) {
	w.manager.logf("server=%q stderr=%q", w.name, p)
	return len(p), nil
}

func (m *mcpManager) openSession(entry mcpServerConfig, name string) (*mcp.ClientSession, error) {
	if entry.Type == "" {
		return nil, fmt.Errorf("server definition is missing; add type and command/url to config")
	}
	if entry.Codemode {
		return nil, fmt.Errorf("codemode is not supported; set codemode to false to expose MCP tools directly")
	}
	entry, err := expandMCPServer(entry)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "hanoi", Version: "1.0.0"}, nil)
	opts := &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"}
	if entry.Protocol == "auto" || entry.Protocol == "2026-07-28" {
		opts.ProtocolVersion = "2026-07-28"
	}
	connect := func(transport mcp.Transport) (*mcp.ClientSession, error) {
		// SSE uses this context for the entire connection, not only initialization.
		// Stop the startup timer on success and cancel only when the session closes.
		ctx, cancel := context.WithCancel(context.Background())
		startup := milliseconds(entry.Timeout.Startup)
		m.mu.RLock()
		authCtx := m.authCtx
		if m.authName != name {
			authCtx = nil
		}
		m.mu.RUnlock()
		stopAuth := func() bool { return true }
		if authCtx != nil {
			startup = 3 * time.Minute
			stopAuth = context.AfterFunc(authCtx, cancel)
		}
		timer := time.AfterFunc(startup, cancel)
		session, err := client.Connect(ctx, transport, opts)
		stopAuth() // A successful connection must outlive the login action.
		if !timer.Stop() && err == nil {
			err = context.DeadlineExceeded
		}
		if err != nil {
			cancel()
			if session != nil {
				_ = session.Close()
			}
			return nil, err
		}
		go func() { _ = session.Wait(); cancel() }()
		if entry.Protocol == "2026-07-28" && session.InitializeResult().ProtocolVersion != "2026-07-28" {
			session.Close()
			return nil, fmt.Errorf("server does not support required protocol 2026-07-28")
		}
		return session, err
	}
	if entry.Type == "local" {
		cmd := exec.Command(entry.Command[0], entry.Command[1:]...)
		cmd.Dir = entry.CWD
		cmd.Env = os.Environ()
		for key, value := range entry.Environment {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Stderr = io.Discard
		if m.logger != nil {
			cmd.Stderr = mcpStderr{m, name}
		}
		return connect(&mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second})
	}
	endpoint, _ := url.Parse(entry.URL) // Validated by expandMCPServer.
	base := mcpHeaderTransport{base: http.DefaultTransport, headers: entry.Headers, origin: endpoint.Scheme + "://" + endpoint.Host}
	transport, err := m.oauthTransport(name, entry, base)
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{Transport: transport}
	session, err := connect(&mcp.StreamableClientTransport{Endpoint: entry.URL, HTTPClient: httpClient})
	if err == nil {
		return session, nil
	}
	var authErr *mcpOAuthLocalError
	var authFailure *mcpOAuthFailure
	if errors.Is(err, errMCPAuthRequired) || errors.Is(err, context.Canceled) || errors.As(err, &authErr) || errors.As(err, &authFailure) {
		return nil, err
	}
	// Compatibility with servers using the older HTTP+SSE transport.
	m.logf("server=%q streamable_http_failed; trying SSE", name)
	session, sseErr := connect(&mcp.SSEClientTransport{Endpoint: entry.URL, HTTPClient: httpClient})
	if sseErr != nil {
		return nil, fmt.Errorf("Streamable HTTP: %v; SSE: %w", err, sseErr)
	}
	return session, nil
}

func (m *mcpManager) connect(name string) error {
	m.mu.Lock()
	server, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown MCP server %q", name)
	}
	old := server.session
	server.session, server.tools, server.err, server.status = nil, nil, "", "connecting"
	entry := server.config
	server.config.Disabled = false
	m.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	m.logf("server=%q connecting", name)
	session, err := m.openSession(entry, name)
	var tools []*mcp.Tool
	if err == nil && session.InitializeResult().Capabilities != nil && session.InitializeResult().Capabilities.Tools != nil {
		parent := context.Background()
		catalogTimeout := milliseconds(entry.Timeout.Catalog)
		m.mu.RLock()
		if m.authName == name && m.authCtx != nil {
			parent, catalogTimeout = m.authCtx, 3*time.Minute
		}
		m.mu.RUnlock()
		ctx, cancel := context.WithTimeout(parent, catalogTimeout)
		for tool, listErr := range session.Tools(ctx, nil) {
			if listErr != nil {
				err = listErr
				break
			}
			tools = append(tools, tool)
		}
		cancel()
		if err != nil {
			_ = session.Close()
			session = nil
			tools = nil
		}
	}
	m.mu.Lock()
	server.session, server.tools = session, tools
	if err != nil {
		server.status, server.err = "error", err.Error()
		if errors.Is(err, errMCPAuthRequired) {
			server.status, server.authStatus = "needs_auth", "not authenticated"
		}
	} else {
		server.status = "connected"
	}
	m.mu.Unlock()
	if err != nil {
		m.logf("server=%q error=%q", name, err)
		return fmt.Errorf("MCP %s: %w", name, err)
	}
	m.logf("server=%q connected tools=%d", name, len(tools))
	go func() {
		err := session.Wait()
		m.mu.Lock()
		defer m.mu.Unlock()
		if server.session == session {
			server.session, server.tools, server.status = nil, nil, "disconnected"
			if err != nil {
				server.err = err.Error()
			}
		}
	}()
	return nil
}

func (m *mcpManager) Enable(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.connect(name)
}

func (m *mcpManager) Disable(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	server, ok := m.servers[name]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("unknown MCP server %q", name)
	}
	session := server.session
	server.session, server.tools, server.status, server.err = nil, nil, "disabled", ""
	server.config.Disabled = true
	m.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	m.logf("server=%q disabled", name)
	return nil
}

func mcpToolName(server, name string) string {
	slug := func(value string, limit int) string {
		var out strings.Builder
		for _, r := range value {
			if out.Len() == limit {
				break
			}
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				out.WriteRune(r)
			} else {
				out.WriteByte('_')
			}
		}
		return out.String()
	}
	hash := sha256.Sum256([]byte(server + "\x00" + name))
	return "mcp_" + slug(server, 16) + "_" + slug(name, 24) + "_" + hex.EncodeToString(hash[:6])
}

func (m *mcpManager) Tools() []tool {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var tools []tool
	for name, server := range m.servers {
		if server.session == nil {
			continue
		}
		for _, remote := range server.tools {
			tools = append(tools, tool{Type: "function", Function: toolFunction{
				Name: mcpToolName(name, remote.Name), Description: "MCP " + name + " / " + remote.Name + ": " + remote.Description, Parameters: remote.InputSchema,
			}})
		}
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Function.Name < tools[j].Function.Name })
	return tools
}

func (m *mcpManager) Call(name, rawArgs string) (string, error) {
	if m == nil {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &args); err != nil || args == nil {
		return "", fmt.Errorf("MCP tool arguments must be a JSON object")
	}
	m.mu.RLock()
	var session *mcp.ClientSession
	var serverName, originalName string
	var timeout int
	for serverID, server := range m.servers {
		if server.session == nil {
			continue
		}
		for _, remote := range server.tools {
			if mcpToolName(serverID, remote.Name) == name {
				session, serverName, originalName, timeout = server.session, serverID, remote.Name, server.config.Timeout.Execution
			}
		}
	}
	m.mu.RUnlock()
	if session == nil {
		return "", fmt.Errorf("MCP tool unavailable: %s (check /mcps)", name)
	}
	m.logf("server=%q tool=%q calling arguments=%s", serverName, originalName, rawArgs)
	ctx, cancel := context.WithTimeout(context.Background(), milliseconds(timeout))
	defer cancel()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: originalName, Arguments: args})
	if err != nil {
		if errors.Is(err, errMCPAuthRequired) {
			m.mu.Lock()
			if server := m.servers[serverName]; server != nil && server.session == session {
				server.status, server.authStatus, server.err = "needs_auth", "not authenticated", errMCPAuthRequired.Error()
			}
			m.mu.Unlock()
		}
		m.logf("server=%q tool=%q error=%q", serverName, originalName, err)
		return "", err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	m.logf("server=%q tool=%q result=%s", serverName, originalName, data)
	// Preserve structured content, non-text blocks, and isError for the model.
	return string(data), nil
}
