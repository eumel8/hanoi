package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/tailscale/hujson"
)

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Provider struct {
	Name    string `json:"name"`
	NPM     string `json:"npm"`
	Options struct {
		BaseURL string            `json:"baseURL"`
		APIKey  string            `json:"apiKey"`
		Headers map[string]string `json:"headers"`
	} `json:"options"`
	Models map[string]Model `json:"models"`
}

// Config contains model settings only; runtime services are owned by the application.
type Config struct {
	APIKey    string
	Model     string
	BaseURL   string
	Headers   map[string]string
	Provider  string
	ModelKey  string
	Providers map[string]Provider
}

// Load reads provider settings using the same config discovery as MCP.
func Load(explicit, selection string) (Config, error) {
	loadDotEnv(".env")
	path, err := FindFile(explicit)
	if err != nil {
		return Config{}, err
	}
	var root struct {
		Model     string              `json:"model"`
		Providers map[string]Provider `json:"provider"`
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		data, err = hujson.Standardize(data)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := json.Unmarshal(data, &root); err != nil {
			return Config{}, fmt.Errorf("config %s: %w", path, err)
		}
	}
	cfg := Config{Providers: root.Providers}
	if len(cfg.Providers) != 0 {
		for id, provider := range cfg.Providers {
			if strings.TrimSpace(id) == "" || strings.Contains(id, "/") {
				return Config{}, fmt.Errorf("provider ID must be nonempty and contain no slash")
			}
			for model := range provider.Models {
				if strings.TrimSpace(model) == "" {
					return Config{}, fmt.Errorf("provider %s: model ID is empty", id)
				}
			}
		}
		if selection == "" {
			selection = root.Model
		}
		if selection == "" {
			models := cfg.ModelChoices()
			if len(models) != 1 {
				return Config{}, fmt.Errorf("set model to provider/model in the config or use -model provider/model")
			}
			selection = models[0]
		}
		return cfg.SelectModel(selection)
	}
	// Preserve the original environment-only configuration and full endpoint URL.
	cfg.APIKey, cfg.Model, cfg.BaseURL = os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_MODEL"), os.Getenv("OPENAI_BASE_URL")
	if cfg.APIKey == "" {
		return Config{}, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	if selection != "" {
		cfg.Model = selection
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o-mini"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1/chat/completions"
	}
	return cfg, nil
}

// SelectModel resolves a selection without mutating the original configuration.
func (cfg Config) SelectModel(selection string) (Config, error) {
	if len(cfg.Providers) == 0 {
		cfg.Model = selection
		return cfg, nil
	}
	providerID, modelID := cfg.Provider, selection
	// Split only the provider prefix; model IDs may themselves contain slashes.
	if before, after, qualified := strings.Cut(selection, "/"); qualified {
		if _, known := cfg.Providers[before]; known {
			providerID, modelID = before, after
		} else if _, local := cfg.Providers[providerID].Models[selection]; !local {
			return cfg, fmt.Errorf("unknown provider %q", before)
		}
	}
	provider, ok := cfg.Providers[providerID]
	if !ok {
		return cfg, fmt.Errorf("unknown provider; use provider/model from the provider section")
	}
	model, ok := provider.Models[modelID]
	if !ok {
		return cfg, fmt.Errorf("provider %s: unknown model %q", providerID, modelID)
	}
	// Hanoi implements the OpenAI Chat Completions protocol, not AI SDK adapters.
	if provider.NPM != "" && provider.NPM != "@ai-sdk/openai-compatible" && provider.NPM != "@ai-sdk/openai" {
		return cfg, fmt.Errorf("provider %s: unsupported adapter; use an OpenAI-compatible provider", providerID)
	}
	baseURL, err := ExpandEnv(provider.Options.BaseURL)
	if err != nil {
		return cfg, fmt.Errorf("provider %s baseURL: %w", providerID, err)
	}
	if baseURL == "" && providerID == "openai" {
		baseURL = "https://api.openai.com/v1"
	}
	endpoint, err := chatEndpoint(baseURL)
	if err != nil {
		return cfg, fmt.Errorf("provider %s: %w", providerID, err)
	}
	key, err := ExpandEnv(provider.Options.APIKey)
	if err != nil {
		return cfg, fmt.Errorf("provider %s apiKey: %w", providerID, err)
	}
	if providerID == "openai" && provider.Options.APIKey == "" {
		key = os.Getenv("OPENAI_API_KEY")
	}
	headers := make(map[string]string, len(provider.Options.Headers))
	for name, value := range provider.Options.Headers {
		expanded, err := ExpandEnv(value)
		if err != nil {
			return cfg, fmt.Errorf("provider %s headers: %w", providerID, err)
		}
		headers[name] = expanded
	}
	if model.ID != "" {
		cfg.Model = model.ID
	} else {
		cfg.Model = modelID
	}
	cfg.Provider, cfg.ModelKey = providerID, modelID
	cfg.APIKey, cfg.BaseURL, cfg.Headers = key, endpoint, headers
	return cfg, nil
}

func chatEndpoint(baseURL string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("baseURL must be an http:// or https:// URL without userinfo or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(u.Path, "/chat/completions") && !strings.HasSuffix(u.Path, "/responses") {
		u.Path += "/chat/completions"
	}
	return u.String(), nil
}

// ModelReference returns the configured provider/model alias for display.
func (cfg Config) ModelReference() string {
	if cfg.Provider != "" {
		return cfg.Provider + "/" + cfg.ModelKey
	}
	return cfg.Model
}

// ModelChoices returns configured provider/model references in sorted order.
func (cfg Config) ModelChoices() []string {
	var choices []string
	for provider, entry := range cfg.Providers {
		for model := range entry.Models {
			choices = append(choices, provider+"/"+model)
		}
	}
	sort.Strings(choices)
	return choices
}
