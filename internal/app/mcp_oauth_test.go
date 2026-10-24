package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func oauthTestRedirect(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return "http://" + address + "/mcp/oauth/callback"
}

type oauthFixture struct {
	registrations atomic.Int32
	logins        atomic.Int32
	refreshes     atomic.Int32
	deny          atomic.Bool
	failRefresh   atomic.Bool
	stepUp        atomic.Bool
	extraScopes   atomic.Bool
	mu            sync.Mutex
	challenge     string
	redirect      string
}

func (f *oauthFixture) handler(t *testing.T, protocol string) http.Handler {
	t.Helper()
	fixture := newMCPTestServer()
	var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, nil)
	if protocol == "sse" {
		handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return fixture }, nil)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		resource := base + "/mcp"
		if strings.HasPrefix(r.URL.Path, "/.well-known/") || r.URL.Path == "/custom-metadata" {
			w.Header().Set("Content-Type", "application/json")
		}
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/mcp":
			json.NewEncoder(w).Encode(map[string]any{"resource": resource, "authorization_servers": []string{base}, "scopes_supported": []string{"read"}})
		case "/.well-known/oauth-protected-resource":
			json.NewEncoder(w).Encode(map[string]any{"resource": base, "authorization_servers": []string{base}})
		case "/.well-known/oauth-authorization-server", "/custom-metadata":
			json.NewEncoder(w).Encode(map[string]any{
				"issuer": base, "authorization_endpoint": base + "/authorize", "token_endpoint": base + "/token", "registration_endpoint": base + "/register",
				"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"read", "offline_access"},
				"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post"}, "authorization_response_iss_parameter_supported": true,
			})
		case "/register":
			f.registrations.Add(1)
			var request struct {
				RedirectURIs []string `json:"redirect_uris"`
				GrantTypes   []string `json:"grant_types"`
				AuthMethod   string   `json:"token_endpoint_auth_method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			if len(request.RedirectURIs) != 1 || request.AuthMethod != "none" || !strings.Contains(strings.Join(request.GrantTypes, " "), "refresh_token") {
				t.Errorf("invalid DCR request: %#v", request)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"client_id": "dynamic-client", "redirect_uris": request.RedirectURIs, "token_endpoint_auth_method": "none"})
		case "/authorize":
			f.logins.Add(1)
			query := r.URL.Query()
			if strings.Contains(query.Get("scope"), "admin") && !f.deny.Load() {
				f.extraScopes.Store(true)
			}
			if query.Get("state") == "" || query.Get("code_challenge_method") != "S256" || query.Get("resource") != resource || !strings.Contains(query.Get("scope"), "read") {
				t.Errorf("invalid authorization request: %v", query)
			}
			redirect, _ := url.Parse(query.Get("redirect_uri"))
			f.mu.Lock()
			f.challenge, f.redirect = query.Get("code_challenge"), redirect.String()
			f.mu.Unlock()
			callback := redirect.Query()
			callback.Set("state", query.Get("state"))
			callback.Set("iss", base)
			if f.deny.Load() {
				callback.Set("error", "access_denied")
			} else {
				callback.Set("code", "private-auth-code")
			}
			redirect.RawQuery = callback.Encode()
			http.Redirect(w, r, redirect.String(), http.StatusFound)
		case "/token":
			r.ParseForm()
			w.Header().Set("Content-Type", "application/json")
			if r.Form.Get("grant_type") == "refresh_token" {
				f.refreshes.Add(1)
				if f.failRefresh.Load() {
					w.WriteHeader(400)
					io.WriteString(w, `{"error":"invalid_grant","error_description":"private-refresh-token"}`)
					return
				}
				if r.Form.Get("refresh_token") != "private-refresh-token" {
					t.Error("wrong refresh token")
				}
				io.WriteString(w, `{"access_token":"private-refreshed-token","refresh_token":"private-rotated-token","token_type":"Bearer","expires_in":3600}`)
				return
			}
			f.mu.Lock()
			challenge, redirect := f.challenge, f.redirect
			f.mu.Unlock()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Form.Get("code") != "private-auth-code" || r.Form.Get("redirect_uri") != redirect || r.Form.Get("resource") != resource {
				t.Errorf("invalid token exchange: %v", r.Form)
			}
			if r.Form.Get("client_id") == "fixed-client" && r.Form.Get("client_secret") != "private-client-secret" {
				t.Error("missing client secret")
			}
			io.WriteString(w, `{"access_token":"private-access-token","refresh_token":"private-refresh-token","token_type":"Bearer","expires_in":3600}`)
		default:
			if token := r.Header.Get("Authorization"); token != "Bearer private-access-token" && token != "Bearer private-refreshed-token" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if f.stepUp.Load() && !f.extraScopes.Load() && r.Method == http.MethodPost {
				data, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(data))
				if bytes.Contains(data, []byte(`"method":"tools/call"`)) {
					w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="insufficient_scope", scope="read admin", resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, base))
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
			handler.ServeHTTP(w, r)
		}
	})
}

func finishOAuthMenuAction(t *testing.T, model tea.Model, command tea.Cmd) tuiModel {
	t.Helper()
	m := model.(tuiModel)
	if command == nil || !m.mcpBusy {
		t.Fatal("expected asynchronous MCP menu action")
	}
	for _, command := range command().(tea.BatchMsg) {
		if result, ok := command().(mcpResultMsg); ok {
			if result.err != nil {
				t.Fatal(result.err)
			}
			updated, _ := m.Update(result)
			m = updated.(tuiModel)
		}
	}
	if m.mcpBusy {
		t.Fatal("menu action never completed")
	}
	return m
}

func TestMCPOAuthStepUpAndMenu(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	fixture := &oauthFixture{}
	server := httptest.NewServer(fixture.handler(t, "http"))
	defer server.Close()
	raw, _ := json.Marshal(mcpOAuthConfig{RedirectURI: oauthTestRedirect(t)})
	path := filepath.Join(t.TempDir(), "hanoi.json")
	writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: server.URL + "/mcp", OAuth: raw})
	manager, err := newMCPManager(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	manager.openBrowser = func(address string) error {
		response, err := http.Get(address)
		if err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		return err
	}
	model, command := (tuiModel{cfg: config{MCP: manager}, textarea: textarea.New()}).handleSlashCommand("/mcps auth test")
	m := finishOAuthMenuAction(t, model, command)
	fixture.stepUp.Store(true)
	if _, err := manager.Call(mcpToolName("test", "echo"), `{"text":"step up"}`); !errors.Is(err, errMCPAuthRequired) {
		t.Fatalf("expected scope challenge: %v", err)
	}
	if !strings.Contains(manager.rows()[0], "needs_auth") {
		t.Fatal("scope challenge not shown in menu")
	}
	// Enter on needs_auth must prompt using the failed tool call's scope challenge,
	// even though initialization and tools/list still accept the old token.
	model, command = m.updateMCPMenu(tea.KeyMsg{Type: tea.KeyEnter})
	m = finishOAuthMenuAction(t, model, command)
	if fixture.logins.Load() != 2 || !fixture.extraScopes.Load() {
		t.Fatal("step-up login did not request missing scopes")
	}
	if _, err := manager.Call(mcpToolName("test", "echo"), `{"text":"step up"}`); err != nil {
		t.Fatalf("new scopes not usable: %v", err)
	}
	model, command = m.updateMCPMenu(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	finishOAuthMenuAction(t, model, command)
	if len(manager.Tools()) != 0 || !strings.Contains(manager.rows()[0], "needs_auth") {
		t.Fatal("menu logout did not disconnect")
	}
}

func TestMCPInteractiveOAuth(t *testing.T) {
	for _, tc := range []struct {
		name, transport string
		preregistered   bool
	}{{"DCR HTTP", "http", false}, {"preregistered HTTP", "http", true}, {"DCR SSE", "sse", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			fixture := &oauthFixture{}
			server := httptest.NewServer(fixture.handler(t, tc.transport))
			defer server.Close()
			redirect := oauthTestRedirect(t)
			config := mcpOAuthConfig{RedirectURI: redirect, Scope: "read"}
			if tc.preregistered {
				config.ClientID, config.ClientSecret = "fixed-client", "private-client-secret"
				config.MetadataURL = server.URL + "/custom-metadata"
			}
			raw, _ := json.Marshal(config)
			entry := mcpServerConfig{Type: "remote", URL: server.URL + "/mcp", OAuth: raw}
			path := filepath.Join(t.TempDir(), "hanoi.json")
			writeMCPTestConfig(t, path, entry)
			var output bytes.Buffer
			manager, err := newMCPManager(path, log.New(&output, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			if !strings.Contains(manager.rows()[0], "needs_auth") || fixture.logins.Load() != 0 {
				t.Fatalf("startup should request login without opening browser: %v", manager.rows())
			}
			manager.openBrowser = func(address string) error {
				// A callback with incorrect state must not end the legitimate login.
				authorization, _ := url.Parse(address)
				callback, _ := url.Parse(redirect)
				callback.RawQuery = url.Values{"state": {"wrong-state"}, "code": {"attacker-code"}}.Encode()
				response, err := http.Get(callback.String())
				if err != nil {
					return err
				}
				response.Body.Close()
				if response.StatusCode != 400 {
					t.Error("invalid state was accepted")
				}
				if authorization.Query().Get("state") == "" {
					t.Error("missing state")
				}
				response, err = http.Get(address)
				if err == nil {
					io.Copy(io.Discard, response.Body)
					response.Body.Close()
				}
				return err
			}
			if err := manager.Login("test"); err != nil {
				var failure *mcpOAuthFailure
				if errors.As(err, &failure) {
					t.Logf("OAuth cause: %v", failure.cause)
				}
				t.Fatalf("login failed: %v; %v", err, manager.rows())
			}
			if len(manager.Tools()) != 2 || !strings.Contains(manager.rows()[0], "authenticated") {
				t.Fatalf("OAuth did not connect: %v", manager.rows())
			}
			if _, err := manager.Call(mcpToolName("test", "echo"), `{"text":"authorized"}`); err != nil {
				t.Fatal(err)
			}
			if fixture.logins.Load() != 1 {
				t.Fatal("expected exactly one browser login")
			}
			wantRegistrations := int32(1)
			if tc.preregistered {
				wantRegistrations = 0
			}
			if fixture.registrations.Load() != wantRegistrations {
				t.Fatalf("registrations=%d", fixture.registrations.Load())
			}
			tokenPath, err := manager.oauthPath("test", entry, &config)
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(tokenPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("token permissions: %o", info.Mode().Perm())
			}
			info, _ = os.Stat(filepath.Dir(tokenPath))
			if info.Mode().Perm() != 0o700 {
				t.Fatal("OAuth directory must be owner-only")
			}
			manager.Close()
			data, _ := os.ReadFile(tokenPath)
			var record mcpOAuthRecord
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			record.Token.Expiry = time.Now().Add(-time.Hour)
			if err := saveMCPOAuth(tokenPath, &record.Config, &record.Token); err != nil {
				t.Fatal(err)
			}
			// A new process/session must refresh using the persisted client registration.
			restored, err := newMCPManager(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if len(restored.Tools()) != 2 || fixture.refreshes.Load() != 1 || fixture.logins.Load() != 1 {
				t.Fatalf("restore/refresh failed: %v", restored.rows())
			}
			data, _ = os.ReadFile(tokenPath)
			if !bytes.Contains(data, []byte("private-rotated-token")) {
				t.Fatal("rotated refresh token not persisted")
			}
			restored.Close()
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			record.Token.Expiry = time.Now().Add(-time.Hour)
			if err := saveMCPOAuth(tokenPath, &record.Config, &record.Token); err != nil {
				t.Fatal(err)
			}
			fixture.failRefresh.Store(true)
			expired, err := newMCPManager(path, log.New(&output, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			defer expired.Close()
			if len(expired.Tools()) != 0 || !strings.Contains(expired.rows()[0], "needs_auth") || fixture.logins.Load() != 1 {
				t.Fatalf("invalid refresh token should require explicit login: %v", expired.rows())
			}
			if err := expired.Logout("test"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(tokenPath); !os.IsNotExist(err) || len(expired.Tools()) != 0 {
				t.Fatal("logout did not delete tokens and disconnect")
			}
			if err := expired.Enable("test"); !errors.Is(err, errMCPAuthRequired) {
				t.Fatalf("logged-out server should require authentication: %v", err)
			}
			for _, secret := range []string{"private-access-token", "private-refresh-token", "private-client-secret", "private-auth-code", "code_verifier"} {
				if strings.Contains(output.String(), secret) {
					t.Errorf("OAuth secret %q leaked into debug log", secret)
				}
			}
		})
	}
}

func TestMCPOAuthDenialAndCancellation(t *testing.T) {
	for _, cancelLogin := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancelLogin), func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			fixture := &oauthFixture{}
			fixture.deny.Store(true)
			server := httptest.NewServer(fixture.handler(t, "http"))
			defer server.Close()
			redirect := oauthTestRedirect(t)
			raw, _ := json.Marshal(mcpOAuthConfig{RedirectURI: redirect})
			path := filepath.Join(t.TempDir(), "hanoi.json")
			writeMCPTestConfig(t, path, mcpServerConfig{Type: "remote", URL: server.URL + "/mcp", OAuth: raw})
			manager, err := newMCPManager(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			opened := make(chan string, 1)
			manager.openBrowser = func(address string) error {
				opened <- address
				if !cancelLogin {
					response, err := http.Get(address)
					if err == nil {
						response.Body.Close()
					}
					return err
				}
				return fmt.Errorf("no browser available")
			}
			done := make(chan error, 1)
			go func() { done <- manager.Login("test") }()
			select {
			case <-opened:
			case <-time.After(5 * time.Second):
				t.Fatal("browser login did not start")
			}
			if cancelLogin {
				if !strings.Contains(strings.Join(manager.rows(), "\n"), "Open in browser:") {
					t.Error("missing manual login URL")
				}
				manager.CancelAuth()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("denied/cancelled login succeeded")
				}
				if !cancelLogin && !strings.Contains(err.Error(), "denied") {
					t.Fatalf("denial not shown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cancelled login did not stop")
			}
			parsed, _ := url.Parse(redirect)
			listener, err := net.Listen("tcp", parsed.Host)
			if err != nil {
				t.Fatalf("callback listener leaked: %v", err)
			}
			listener.Close()
			if len(manager.Tools()) != 0 || !strings.Contains(manager.rows()[0], "needs_auth") || strings.Contains(manager.rows()[0], "Open in browser:") {
				t.Fatalf("bad cancelled status: %v", manager.rows())
			}
		})
	}
}

func TestMCPOAuthConfiguration(t *testing.T) {
	t.Setenv("HANOI_OAUTH_CLIENT", "fixed-client")
	config, err := parseMCPOAuth(json.RawMessage(`{"clientId":"{env:HANOI_OAUTH_CLIENT}","clientSecret":"secret","callbackPort":23456,"scope":"read write"}`))
	if err != nil || config.ClientID != "fixed-client" || config.RedirectURI != "http://127.0.0.1:23456/mcp/oauth/callback" {
		t.Fatalf("legacy/env config: %#v %v", config, err)
	}
	for _, raw := range []string{`true`, `null`, `{"callback_port":0}`, `{"callback_port":65536}`, `{"redirect_uri":"https://example.com/callback"}`, `{"redirect_uri":"http://0.0.0.0:19876/callback"}`, `{"client_secret":"secret"}`} {
		if _, err := parseMCPOAuth(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid OAuth config: %s", raw)
		}
	}
	if disabled, err := parseMCPOAuth(json.RawMessage(`false`)); err != nil || disabled != nil {
		t.Fatal("oauth:false should disable authorization")
	}
	// Expiring a context must release a callback listener without invoking a browser.
	redirect := oauthTestRedirect(t)
	manager := &mcpManager{servers: make(map[string]*mcpServerState), openBrowser: func(string) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = manager.fetchOAuthCode(ctx, "test", redirect, "https://example.test/auth?state=random-state")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected callback timeout: %v", err)
	}
}
