package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	appconfig "hanoi/internal/config"
)

func TestCopilotModelDiscovery(t *testing.T) {
	isolatedCopilotStore(t)
	if err := saveCopilotSession(copilotSession{AccessToken: "private-token"}); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.URL.Query().Get("api-version") != "test" || r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("X-GitHub-Api-Version") == "" {
			t.Error("incorrect catalog request")
		}
		io.WriteString(w, `{"data":[
			{"id":"new-model","name":"New Model","model_picker_enabled":true,"supported_endpoints":["/responses"],"capabilities":{"type":"chat","supports":{"tool_calls":true}}},
			{"id":"gpt-6-chat","name":"Chat Model","supported_endpoints":["/chat/completions"]},
			{"id":"disabled","policy":{"state":"disabled"}},
			{"id":"hidden","model_picker_enabled":false},
			{"id":"embedding","capabilities":{"type":"embeddings"}},
			{"id":"no-tools","capabilities":{"supports":{"tool_calls":false}}},
			{"id":"native-only","supported_endpoints":["/v1/messages"]},
			{"name":"Malformed"}
		]}`)
	}))
	defer api.Close()
	provider := appconfig.Provider{Name: "Custom Copilot", Models: map[string]appconfig.Model{"alias": {ID: "new-model", Name: "My Alias"}, "old-model": {}}}
	provider.Options.BaseURL = api.URL + "/v1?api-version=test"
	cfg := config{Config: appconfig.Config{Providers: map[string]appconfig.Provider{"github-copilot": provider}}}
	models, err := discoverCopilotModels(context.Background(), cfg)
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%v err=%v", models, err)
	}
	cfg = cfg.withDiscoveredModels(models)
	if _, ok := cfg.Providers["github-copilot"].Models["old-model"]; ok {
		t.Fatal("unavailable model retained")
	}
	selected, err := cfg.selectModel("github-copilot/alias")
	if err != nil || selected.Model != "new-model" || !strings.Contains(selected.BaseURL, "/v1/responses?api-version=test") {
		t.Fatalf("alias routing=%s %v", selected.BaseURL, err)
	}
	selected, err = cfg.selectModel("github-copilot/gpt-6-chat")
	if err != nil || !strings.Contains(selected.BaseURL, "/v1/chat/completions?") {
		t.Fatalf("advertised chat route ignored: %s %v", selected.BaseURL, err)
	}
	if cfg.Providers["github-copilot"].Models["alias"].Name != "My Alias" {
		t.Fatal("lost alias name")
	}
	if len(provider.Models) != 2 {
		t.Fatal("mutated original configuration")
	}
}

func TestCopilotDiscoveryFailurePreservesModels(t *testing.T) {
	isolatedCopilotStore(t)
	if err := saveCopilotSession(copilotSession{AccessToken: "private-token"}); err != nil {
		t.Fatal(err)
	}
	for _, response := range []struct {
		status int
		body   string
	}{
		{401, "private-token server error"}, {200, "not-json private-token"}, {200, `{"data":[]}`},
	} {
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(response.status)
			io.WriteString(w, response.body)
		}))
		cfg := config{Config: appconfig.Config{}.WithCopilot()}
		provider := cfg.Providers["github-copilot"]
		provider.Options.BaseURL = api.URL
		cfg.Providers["github-copilot"] = provider
		m := tuiModel{cfg: cfg, textarea: textarea.New()}
		updated, cmd := m.handleSlashCommand("/models")
		m = updated.(tuiModel)
		if cmd == nil || !m.discoveryBusy || !m.providersOpen {
			t.Fatal("discovery not started")
		}
		result := cmd()
		updated, _ = m.Update(result)
		m = updated.(tuiModel)
		if m.discoveryBusy || len(m.cfg.ModelChoices()) != 2 || !strings.Contains(m.discoveryInfo, "Model discovery:") || strings.Contains(m.discoveryInfo, "private-token") {
			t.Fatalf("failed discovery changed catalog or leaked credentials: %s", m.discoveryInfo)
		}
		api.Close()
	}
}

func TestCopilotDiscoveredPicker(t *testing.T) {
	m := tuiModel{cfg: config{Config: appconfig.Config{}.WithCopilot()}, textarea: textarea.New(), height: 20}
	models := make(map[string]appconfig.Model)
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		models[id] = appconfig.Model{Name: "Discovered " + id, Endpoint: "/responses"}
	}
	updated, _ := m.Update(discoveredModelsMsg{models: models})
	m = updated.(tuiModel)
	m.providerCursor = 9
	if view := m.providerView(); !strings.Contains(view, "> github-copilot/j") || !strings.Contains(view, "↑ more models") {
		t.Fatalf("selected model not visible: %s", view)
	}
	updated, _ = m.updateProviderMenu(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(tuiModel)
	if m.cfg.Model != "j" || m.cfg.BaseURL != "https://api.githubcopilot.com/responses" {
		t.Fatal("discovered model activation failed")
	}
}
