package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

var errMCPAuthRequired = errors.New("OAuth login required; use /mcps auth <name>")

type mcpOAuthConfig struct {
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Scope        string `json:"scope,omitempty"`
	CallbackPort int    `json:"callback_port,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	MetadataURL  string `json:"auth_server_metadata_url,omitempty"`
}

func parseMCPOAuth(raw json.RawMessage) (*mcpOAuthConfig, error) {
	if strings.TrimSpace(string(raw)) == "false" {
		return nil, nil
	}
	config := &mcpOAuthConfig{}
	if len(raw) > 0 {
		if strings.TrimSpace(string(raw)) == "null" || strings.TrimSpace(string(raw)) == "true" {
			return nil, fmt.Errorf("oauth must be an object or false")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, fmt.Errorf("oauth must be an object or false")
		}
		// Accept both current OpenCode names and legacy camelCase names.
		for old, current := range map[string]string{"clientId": "client_id", "clientSecret": "client_secret", "callbackPort": "callback_port", "redirectUri": "redirect_uri"} {
			if value, ok := fields[old]; ok {
				if _, exists := fields[current]; !exists {
					fields[current] = value
				}
				delete(fields, old)
			}
		}
		data, _ := json.Marshal(fields)
		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("invalid oauth configuration: %w", err)
		}
		if value, ok := fields["callback_port"]; ok && (string(value) == "null" || config.CallbackPort < 1 || config.CallbackPort > 65535) {
			return nil, fmt.Errorf("oauth.callback_port must be between 1 and 65535")
		}
	}
	var err error
	for _, field := range []*string{&config.ClientID, &config.ClientSecret, &config.Scope, &config.RedirectURI, &config.MetadataURL} {
		*field, err = expandMCPEnv(*field)
		if err != nil {
			return nil, err
		}
	}
	if config.ClientSecret != "" && config.ClientID == "" {
		return nil, fmt.Errorf("oauth.client_secret requires client_id")
	}
	if config.RedirectURI == "" {
		port := config.CallbackPort
		if port == 0 {
			port = 19876
		}
		config.RedirectURI = fmt.Sprintf("http://127.0.0.1:%d/mcp/oauth/callback", port)
	}
	u, err := url.Parse(config.RedirectURI)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("oauth.redirect_uri must be a loopback HTTP callback URL")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("oauth.redirect_uri must use localhost or a loopback IP")
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid OAuth callback port")
		}
	}
	if config.MetadataURL != "" {
		u, err := url.Parse(config.MetadataURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("invalid oauth.auth_server_metadata_url")
		}
	}
	return config, nil
}

type mcpOAuthRecord struct {
	Config oauth2.Config `json:"config"`
	Token  oauth2.Token  `json:"token"`
}

func (m *mcpManager) oauthPath(name string, entry mcpServerConfig, config *mcpOAuthConfig) (string, error) {
	dir := m.oauthDir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(base, "hanoi", "mcp-oauth")
	}
	data, _ := json.Marshal([]any{name, entry.URL, config})
	hash := sha256.Sum256(data)
	return filepath.Join(dir, hex.EncodeToString(hash[:])+".json"), nil
}

func saveMCPOAuth(path string, config *oauth2.Config, token *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create OAuth storage: %w", err)
	}
	data, err := json.Marshal(mcpOAuthRecord{Config: *config, Token: *token})
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".oauth-*")
	if err != nil {
		return fmt.Errorf("create OAuth token file: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err = file.Write(data); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

type mcpSavingTokenSource struct {
	mu     sync.Mutex
	source oauth2.TokenSource
	config *oauth2.Config
	path   string
	token  *oauth2.Token
}

func (s *mcpSavingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, err := s.source.Token()
	if err != nil {
		return nil, errMCPAuthRequired
	}
	if s.token == nil || token.AccessToken != s.token.AccessToken || token.RefreshToken != s.token.RefreshToken || !token.Expiry.Equal(s.token.Expiry) {
		if err := saveMCPOAuth(s.path, s.config, token); err != nil {
			return nil, fmt.Errorf("save OAuth session: %w", err)
		}
		copy := *token
		s.token = &copy
	}
	return token, nil
}

// No MCP headers are sent to authorization servers or token endpoints.
type mcpMetadataTransport struct {
	base     http.RoundTripper
	override string
}

func (t mcpMetadataTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.override != "" && req.Method == http.MethodGet && (strings.Contains(req.URL.Path, "/.well-known/oauth-authorization-server") || strings.Contains(req.URL.Path, "/.well-known/openid-configuration")) {
		clone := req.Clone(req.Context())
		clone.URL, _ = url.Parse(t.override)
		return t.base.RoundTrip(clone)
	}
	return t.base.RoundTrip(req)
}

type mcpOAuthTransport struct {
	base         http.RoundTripper
	handler      auth.OAuthHandler
	manager      *mcpManager
	name, origin string
	mu           sync.Mutex // One authorization flow at a time, also for parallel HTTP requests.
}

type mcpOAuthChallenge struct {
	URL    string
	Status int
	Header http.Header
}

func (t *mcpOAuthTransport) authorize(ctx context.Context, req *http.Request, resp *http.Response) error {
	t.mu.Lock()
	err := t.handler.Authorize(ctx, req, resp) // Owns and closes resp.Body.
	t.mu.Unlock()
	if err != nil {
		// OAuth responses may contain credentials; retain causes without logging raw bodies.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var local *mcpOAuthLocalError
		if errors.As(err, &local) {
			return local
		}
		return &mcpOAuthFailure{cause: err}
	}
	t.manager.mu.Lock()
	if server := t.manager.servers[t.name]; server != nil {
		server.authChallenge = nil
	}
	t.manager.mu.Unlock()
	return nil
}

func (t *mcpOAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme+"://"+req.URL.Host != t.origin {
		return t.base.RoundTrip(req)
	}
	request := req.Clone(req.Context())
	source, err := t.handler.TokenSource(req.Context())
	if err != nil {
		return nil, err
	}
	if source != nil {
		token, err := source.Token()
		if err != nil && !t.manager.authInteractive(t.name) {
			return nil, err
		}
		if err == nil {
			token.SetAuthHeader(request)
		}
	}
	resp, err := t.base.RoundTrip(request)
	if err != nil || (resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden) {
		return resp, err
	}
	// Only a Bearer challenge can trigger OAuth; ordinary permission errors remain HTTP errors.
	if resp.StatusCode == http.StatusForbidden && !strings.Contains(resp.Header.Get("WWW-Authenticate"), "insufficient_scope") {
		return resp, nil
	}
	t.manager.mu.Lock()
	if server := t.manager.servers[t.name]; server != nil {
		server.authChallenge = &mcpOAuthChallenge{URL: req.URL.String(), Status: resp.StatusCode, Header: resp.Header.Clone()}
	}
	t.manager.mu.Unlock()
	if !t.manager.authInteractive(t.name) {
		resp.Body.Close()
		return nil, errMCPAuthRequired
	}
	if err := t.authorize(req.Context(), request, resp); err != nil {
		return nil, err
	}
	source, err = t.handler.TokenSource(req.Context())
	if err != nil || source == nil {
		return nil, errMCPAuthRequired
	}
	token, err := source.Token()
	if err != nil {
		return nil, err
	}
	retry := req.Clone(req.Context())
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, fmt.Errorf("cannot replay MCP request after OAuth login")
		}
		retry.Body, err = req.GetBody()
		if err != nil {
			return nil, err
		}
	}
	token.SetAuthHeader(retry)
	return t.base.RoundTrip(retry)
}

func (m *mcpManager) oauthTransport(name string, entry mcpServerConfig, base http.RoundTripper) (http.RoundTripper, error) {
	config, err := parseMCPOAuth(entry.OAuth)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return base, nil
	}
	for key := range entry.Headers {
		if strings.EqualFold(key, "Authorization") {
			return base, nil
		}
	}
	path, err := m.oauthPath(name, entry, config)
	if err != nil {
		return nil, err
	}
	authClient := &http.Client{Timeout: 30 * time.Second, Transport: mcpMetadataTransport{base: http.DefaultTransport, override: config.MetadataURL}}
	refreshCtx := context.WithValue(context.Background(), oauth2.HTTPClient, authClient)
	var initial oauth2.TokenSource
	var grantedScopes []string
	if data, readErr := os.ReadFile(path); readErr == nil {
		var record mcpOAuthRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("invalid stored OAuth session; use /mcps logout %s", name)
		}
		initial = &mcpSavingTokenSource{source: record.Config.TokenSource(refreshCtx, &record.Token), config: &record.Config, path: path, token: &record.Token}
		grantedScopes = append([]string{}, record.Config.Scopes...)
		m.setAuthStatus(name, "authenticated")
	} else if !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read OAuth session: %w", readErr)
	}
	handlerConfig := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL: config.RedirectURI,
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			return m.fetchOAuthCode(ctx, name, config.RedirectURI, args.URL)
		},
		RequestRefreshToken: true,
		Client:              authClient,
		InitialTokenSource:  initial,
		NewTokenSource: func(ctx context.Context, oauthConfig *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
			if err := saveMCPOAuth(path, oauthConfig, token); err != nil {
				return nil, err
			}
			m.setAuthStatus(name, "authenticated")
			return &mcpSavingTokenSource{source: oauthConfig.TokenSource(ctx, token), config: oauthConfig, path: path, token: token}, nil
		},
	}
	if config.Scope != "" {
		handlerConfig.ScopeFilter = func([]string) []string { return strings.Fields(config.Scope) }
	} else if len(grantedScopes) > 0 {
		handlerConfig.ScopeFilter = func(discovered []string) []string {
			seen := make(map[string]bool)
			var scopes []string
			for _, scope := range append(append([]string{}, grantedScopes...), discovered...) {
				if !seen[scope] {
					scopes = append(scopes, scope)
					seen[scope] = true
				}
			}
			return scopes
		}
	}
	if config.ClientID != "" {
		handlerConfig.PreregisteredClient = &oauthex.ClientCredentials{ClientID: config.ClientID}
		if config.ClientSecret != "" {
			handlerConfig.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: config.ClientSecret}
		}
	} else {
		handlerConfig.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "Hanoi", RedirectURIs: []string{config.RedirectURI}, TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"}, Scope: config.Scope,
		}}
	}
	handler, err := auth.NewAuthorizationCodeHandler(handlerConfig)
	if err != nil {
		return nil, err
	}
	endpoint, _ := url.Parse(entry.URL)
	transport := &mcpOAuthTransport{base: base, handler: handler, manager: m, name: name, origin: endpoint.Scheme + "://" + endpoint.Host}
	m.mu.RLock()
	server := m.servers[name]
	var challenge *mcpOAuthChallenge
	if server != nil {
		challenge = server.authChallenge
	}
	ctx := m.authCtx
	interactive := m.authCancel != nil && m.authName == name
	m.mu.RUnlock()
	// Reuse the challenge from a failed tool call, including requested step-up scopes.
	if interactive && challenge != nil {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, challenge.URL, nil)
		if err != nil {
			return nil, err
		}
		resp := &http.Response{StatusCode: challenge.Status, Header: challenge.Header.Clone(), Body: http.NoBody}
		if err := transport.authorize(ctx, req, resp); err != nil {
			return nil, err
		}
	}
	return transport, nil
}

func (m *mcpManager) setAuthStatus(name, status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if server := m.servers[name]; server != nil {
		server.authStatus = status
	}
}

func (m *mcpManager) authInteractive(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.authCancel != nil && m.authName == name
}

func (m *mcpManager) CancelAuth() {
	m.mu.RLock()
	cancel := m.authCancel
	m.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (m *mcpManager) Login(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	server := m.servers[name]
	var entry mcpServerConfig
	if server != nil {
		entry = server.config
	}
	m.mu.RUnlock()
	if server == nil {
		return fmt.Errorf("unknown MCP server %q", name)
	}
	if entry.Type != "remote" {
		return fmt.Errorf("OAuth login is only available for remote MCP servers")
	}
	config, err := parseMCPOAuth(entry.OAuth)
	if err != nil {
		return err
	}
	if config == nil {
		return fmt.Errorf("OAuth is disabled; remove oauth: false from this server's config")
	}
	for key := range entry.Headers {
		if strings.EqualFold(key, "Authorization") {
			return fmt.Errorf("remove the static Authorization header before using OAuth")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	m.mu.Lock()
	m.authCtx, m.authCancel, m.authName = ctx, cancel, name
	m.authCallbackDone = nil
	server.authStatus = "logging in"
	m.mu.Unlock()
	defer func() {
		cancel()
		m.mu.Lock()
		m.authCtx, m.authCancel, m.authName = nil, nil, ""
		server.authURL = ""
		if server.authStatus == "logging in" {
			server.authStatus = "not authenticated"
		}
		m.mu.Unlock()
	}()
	m.logf("server=%q OAuth login started", name)
	err = m.connect(name)
	m.mu.RLock()
	callbackDone := m.authCallbackDone
	m.mu.RUnlock()
	if callbackDone != nil {
		<-callbackDone
	}
	if err == nil {
		m.mu.Lock()
		if server.authStatus == "logging in" {
			server.authStatus = "not required"
		}
		m.mu.Unlock()
		m.logf("server=%q OAuth login completed", name)
	} else {
		m.mu.Lock()
		server.status, server.authStatus = "needs_auth", "not authenticated"
		m.mu.Unlock()
	}
	return err
}

func (m *mcpManager) Logout(name string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.RLock()
	server := m.servers[name]
	var entry mcpServerConfig
	if server != nil {
		entry = server.config
	}
	m.mu.RUnlock()
	if server == nil {
		return fmt.Errorf("unknown MCP server %q", name)
	}
	if entry.Type != "remote" {
		return fmt.Errorf("OAuth logout is only available for remote MCP servers")
	}
	entry, err := expandMCPServer(entry)
	if err != nil {
		return err
	}
	config, err := parseMCPOAuth(entry.OAuth)
	if err != nil {
		return err
	}
	if config == nil {
		return fmt.Errorf("OAuth is disabled for this server")
	}
	path, err := m.oauthPath(name, entry, config)
	if err != nil {
		return err
	}
	m.mu.Lock()
	session := server.session
	server.session, server.tools, server.authURL = nil, nil, ""
	server.status, server.authStatus, server.err = "needs_auth", "not authenticated", ""
	m.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete OAuth session: %w", err)
	}
	m.logf("server=%q OAuth logged out", name)
	return nil
}

type mcpOAuthLocalError struct{ message string }

func (e *mcpOAuthLocalError) Error() string { return e.message }

type mcpOAuthFailure struct{ cause error }

func (e *mcpOAuthFailure) Error() string {
	return "OAuth authorization failed; check server metadata, client credentials and redirect URI"
}
func (e *mcpOAuthFailure) Unwrap() error { return e.cause }

func (m *mcpManager) fetchOAuthCode(ctx context.Context, name, redirectURI, authorizationURL string) (*auth.AuthorizationResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	m.mu.Lock()
	m.authCallbackDone = done
	m.mu.Unlock()
	defer close(done)
	redirect, _ := url.Parse(redirectURI)
	authorize, err := url.Parse(authorizationURL)
	if err != nil || (authorize.Scheme != "https" && authorize.Scheme != "http") || authorize.Host == "" {
		return nil, &mcpOAuthLocalError{"invalid OAuth authorization URL"}
	}
	state := authorize.Query().Get("state")
	if state == "" {
		return nil, &mcpOAuthLocalError{"OAuth state is missing"}
	}
	port := redirect.Port()
	if port == "" {
		port = "80"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(redirect.Hostname(), port))
	if err != nil {
		return nil, &mcpOAuthLocalError{fmt.Sprintf("cannot listen on OAuth callback %s: %v", redirect.Host, err)}
	}
	type outcome struct {
		result *auth.AuthorizationResult
		err    error
	}
	result := make(chan outcome, 1)
	path := redirect.Path
	if path == "" {
		path = "/"
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth state.", http.StatusBadRequest)
			return
		}
		var response outcome
		if query.Get("error") != "" {
			response.err = &mcpOAuthLocalError{"OAuth authorization was denied by the user or provider"}
		} else {
			if query.Get("code") == "" {
				http.Error(w, "Missing OAuth code.", http.StatusBadRequest)
				return
			}
			response.result = &auth.AuthorizationResult{Code: query.Get("code"), State: query.Get("state"), Iss: query.Get("iss")}
		}
		select {
		case result <- response:
		default:
		}
		io.WriteString(w, "OAuth response received. You can return to Hanoi.")
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Close()
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	m.mu.Lock()
	if entry := m.servers[name]; entry != nil {
		entry.authURL = authorizationURL
		entry.authStatus = "waiting for browser"
	}
	m.mu.Unlock()
	open := m.openBrowser
	if open == nil {
		open = openOAuthBrowser
	}
	_ = open(authorizationURL) // The URL remains visible for manual opening on headless systems.
	select {
	case response := <-result:
		return response.result, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-serveErr:
		return nil, &mcpOAuthLocalError{"OAuth callback listener stopped"}
	}
}

func openOAuthBrowser(address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", address)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", address)
	default:
		if _, err := exec.LookPath("xdg-open"); err == nil {
			command = exec.Command("xdg-open", address)
		} else {
			command = exec.Command("gio", "open", address)
		}
	}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
