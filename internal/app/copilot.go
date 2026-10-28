package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Public OAuth application ID used by OpenCode's GitHub Copilot integration.
const copilotClientID = "Ov23li8tweQw6odWQebz"

type copilotSession struct {
	AccessToken string `json:"access_token"`
	APIEndpoint string `json:"api_endpoint,omitempty"`
}

type copilotDevice struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

func copilotSessionPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hanoi", "provider-oauth", "github-copilot.json"), nil
}

func loadCopilotSession() (copilotSession, error) {
	path, err := copilotSessionPath()
	if err != nil {
		return copilotSession{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return copilotSession{}, fmt.Errorf("GitHub Copilot is not connected; run hanoi -auth github-copilot or /connect github-copilot")
	}
	if err != nil {
		return copilotSession{}, fmt.Errorf("read Copilot session: %w", err)
	}
	var session copilotSession
	if json.Unmarshal(data, &session) != nil || session.AccessToken == "" {
		return session, fmt.Errorf("invalid Copilot session; log in again")
	}
	return session, nil
}

func saveCopilotSession(session copilotSession) error {
	path, err := copilotSessionPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".copilot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func logoutCopilot() error {
	path, err := copilotSessionPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// OAuth responses never enter API logs or error messages.
func copilotJSON(ctx context.Context, method, endpoint, token string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("invalid Copilot endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "hanoi")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-GitHub-Api-Version", "2025-04-01")
	}
	client := *http.DefaultClient
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("GitHub Copilot connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub Copilot: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("invalid GitHub Copilot response")
	}
	return nil
}

func startCopilotLogin(ctx context.Context) (copilotDevice, error) {
	var device copilotDevice
	err := copilotJSON(ctx, http.MethodPost, "https://github.com/login/device/code", "", map[string]string{"client_id": copilotClientID, "scope": "read:user"}, &device)
	if err == nil && (device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI != "https://github.com/login/device" || device.ExpiresIn <= 0) {
		err = fmt.Errorf("invalid GitHub device authorization response")
	}
	return device, err
}

func finishCopilotLogin(ctx context.Context, device copilotDevice) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(device.ExpiresIn)*time.Second)
	defer cancel()
	interval := time.Duration(max(1, device.Interval)) * time.Second
	for {
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		var token struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Interval    int    `json:"interval"`
		}
		err := copilotJSON(ctx, http.MethodPost, "https://github.com/login/oauth/access_token", "", map[string]string{"client_id": copilotClientID, "device_code": device.DeviceCode, "grant_type": "urn:ietf:params:oauth:grant-type:device_code"}, &token)
		if err != nil {
			return err
		}
		if token.AccessToken != "" {
			session := copilotSession{AccessToken: token.AccessToken}
			var user struct {
				ChatEnabled *bool `json:"chat_enabled"`
				Endpoints   struct {
					API string `json:"api"`
				} `json:"endpoints"`
			}
			if err := copilotJSON(ctx, http.MethodGet, "https://api.github.com/copilot_internal/user", token.AccessToken, nil, &user); err == nil {
				if user.ChatEnabled != nil && !*user.ChatEnabled {
					return fmt.Errorf("this GitHub account has no active Copilot access; enable Copilot and log in again")
				}
				if user.Endpoints.API != "" {
					if !validCopilotEndpoint(user.Endpoints.API) {
						return fmt.Errorf("invalid Copilot API endpoint")
					}
					session.APIEndpoint = strings.TrimRight(user.Endpoints.API, "/")
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return saveCopilotSession(session)
		}
		switch token.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
			if next := time.Duration(token.Interval) * time.Second; next > interval {
				interval = next
			}
		case "access_denied":
			return fmt.Errorf("GitHub Copilot authorization denied")
		case "expired_token":
			return fmt.Errorf("GitHub Copilot device code expired; log in again")
		default:
			return fmt.Errorf("GitHub Copilot device authorization failed")
		}
	}
}

func validCopilotEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "https" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Port() == "" && (u.Hostname() == "api.githubcopilot.com" || strings.HasSuffix(u.Hostname(), ".githubcopilot.com"))
}

func authorizeProviderRequest(cfg config, req *http.Request, payload []byte) error {
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	for name, value := range cfg.Headers {
		req.Header.Set(name, value)
	}
	if cfg.Provider != "github-copilot" {
		return nil
	}
	session, err := loadCopilotSession()
	if err != nil {
		return err
	}
	// An explicitly configured base URL wins over the account's discovered endpoint.
	if cfg.Providers[cfg.Provider].Options.BaseURL == "" && session.APIEndpoint != "" {
		if !validCopilotEndpoint(session.APIEndpoint) {
			return fmt.Errorf("invalid stored Copilot endpoint; log in again")
		}
		base, _ := url.Parse(session.APIEndpoint)
		req.URL.Scheme, req.URL.Host = base.Scheme, base.Host
		req.URL.Path = strings.TrimRight(base.Path, "/") + req.URL.Path
	}
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	req.Header.Set("User-Agent", "hanoi")
	req.Header.Set("X-GitHub-Api-Version", "2026-08-01")
	req.Header.Set("Openai-Intent", "conversation-edits")
	req.Header.Set("X-Interaction-Type", "conversation-agent")
	req.Header.Set("X-Initiator", copilotInitiator(payload))
	return nil
}

func copilotInitiator(payload []byte) string {
	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(payload, &body) == nil {
		if len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Role == "user" {
			return "user"
		}
		var input []struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(body.Input, &input) == nil && len(input) > 0 && input[len(input)-1].Role == "user" {
			return "user"
		}
		var text string
		if json.Unmarshal(body.Input, &text) == nil && text != "" {
			return "user"
		}
	}
	return "agent"
}
