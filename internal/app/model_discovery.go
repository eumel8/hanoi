package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	appconfig "hanoi/internal/config"
)

type discoveredModelsMsg struct {
	models map[string]appconfig.Model
	err    error
}

func discoverCopilotModels(ctx context.Context, cfg config) (map[string]appconfig.Model, error) {
	settings, err := cfg.Config.SelectModel("github-copilot/gpt-4.1")
	if err != nil {
		return nil, err
	}
	cfg.Config = settings
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid model discovery endpoint")
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/chat/completions"), "/responses") + "/models"
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid model discovery request")
	}
	if err := authorizeProviderRequest(cfg, req, nil); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Copilot model discovery connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot model discovery: HTTP %d", resp.StatusCode)
	}
	var result struct {
		Data []struct {
			ID        string   `json:"id"`
			Name      string   `json:"name"`
			Picker    *bool    `json:"model_picker_enabled"`
			Endpoints []string `json:"supported_endpoints"`
			Policy    struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Tools *bool `json:"tool_calls"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid Copilot model catalog")
	}
	models := make(map[string]appconfig.Model)
	for _, remote := range result.Data {
		if strings.TrimSpace(remote.ID) == "" || remote.Policy.State == "disabled" || (remote.Picker != nil && !*remote.Picker) || (remote.Capabilities.Type != "" && remote.Capabilities.Type != "chat") || (remote.Capabilities.Supports.Tools != nil && !*remote.Capabilities.Supports.Tools) {
			continue
		}
		endpoint := ""
		for _, candidate := range remote.Endpoints {
			if candidate == "/responses" {
				endpoint = candidate
				break
			}
			if candidate == "/chat/completions" {
				endpoint = candidate
			}
		}
		if len(remote.Endpoints) > 0 && endpoint == "" {
			continue
		}
		models[remote.ID] = appconfig.Model{Name: remote.Name, Endpoint: endpoint}
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("Copilot returned no compatible, enabled tool-calling models")
	}
	return models, nil
}

func (cfg config) withDiscoveredModels(models map[string]appconfig.Model) config {
	providers := make(map[string]appconfig.Provider, len(cfg.Providers)+1)
	for id, provider := range cfg.Providers {
		providers[id] = provider
	}
	provider := providers["github-copilot"]
	merged := make(map[string]appconfig.Model, len(models))
	for id, model := range models {
		merged[id] = model
	}
	for alias, local := range provider.Models {
		id := local.ID
		if id == "" {
			id = alias
		}
		if remote, ok := models[id]; ok {
			remote.ID = local.ID
			if local.Name != "" {
				remote.Name = local.Name
			}
			merged[alias] = remote
		}
	}
	provider.Models = merged
	if provider.Name == "" {
		provider.Name = "GitHub Copilot"
	}
	providers["github-copilot"] = provider
	cfg.Providers = providers
	if cfg.Provider == "github-copilot" {
		if selected, err := cfg.selectModel(cfg.ModelReference()); err == nil {
			cfg = selected
		}
	}
	return cfg
}

func (m tuiModel) startModelDiscovery() (tea.Model, tea.Cmd) {
	if m.discoveryBusy {
		return m, nil
	}
	m.discoveryBusy = true
	m.discoveryInfo = "Loading available Copilot models…"
	cfg := m.cfg
	return m, func() tea.Msg {
		models, err := discoverCopilotModels(context.Background(), cfg)
		return discoveredModelsMsg{models: models, err: err}
	}
}
