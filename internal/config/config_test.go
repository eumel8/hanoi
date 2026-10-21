package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAndSelectModel(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("OPENAI_API_KEY", "existing-key")
	t.Setenv("HANOI_CONFIG_KEY", "")
	if err := os.WriteFile(".env", []byte("OPENAI_API_KEY=ignored\nHANOI_CONFIG_KEY='dotenv-key'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := `{
		// Provider and MCP share workspace discovery and environment expansion.
		"model": "openai/mini",
		"provider": {
			"openai": {"models":{"mini":{}}},
			"local": {"options":{"baseURL":"http://localhost:11434/v1","apiKey":"{env:HANOI_CONFIG_KEY}"},"models":{"alias":{"id":"vendor/model"}}},
		},
	}`
	if err := os.WriteFile("hanoi.jsonc", []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "existing-key" || cfg.ModelReference() != "openai/mini" {
		t.Fatal("startup selection or environment precedence changed")
	}
	selected, err := cfg.SelectModel("local/alias")
	if err != nil {
		t.Fatal(err)
	}
	if selected.APIKey != "dotenv-key" || selected.Model != "vendor/model" || selected.BaseURL != "http://localhost:11434/v1/chat/completions" {
		t.Fatal("provider selection did not resolve settings")
	}
	if cfg.ModelReference() != "openai/mini" {
		t.Fatal("selection mutated original configuration")
	}
	if !reflect.DeepEqual(cfg.ModelChoices(), []string{"local/alias", "openai/mini"}) {
		t.Fatal("model choices not sorted")
	}
	if _, err := selected.SelectModel("local/missing"); err == nil {
		t.Fatal("unknown model accepted")
	}
}

func TestFindFile(t *testing.T) {
	t.Chdir(t.TempDir())
	path, err := FindFile("")
	if err != nil || path != "" {
		t.Fatalf("empty workspace: path=%q err=%v", path, err)
	}
	for _, name := range []string{"opencode.jsonc", "opencode.json", "hanoi.jsonc", "hanoi.json"} {
		if err := os.WriteFile(name, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		path, err = FindFile("")
		if err != nil || path != name {
			t.Fatalf("discovery priority: path=%q want=%q err=%v", path, name, err)
		}
	}
	explicit := filepath.Join("elsewhere", "config.json")
	if path, err := FindFile(explicit); err != nil || path != explicit {
		t.Fatalf("explicit path changed: path=%q err=%v", path, err)
	}
}

func TestExpandEnv(t *testing.T) {
	t.Setenv("HANOI_CONFIG_VALUE", "resolved")
	if value, err := ExpandEnv("prefix/{env:HANOI_CONFIG_VALUE}"); err != nil || value != "prefix/resolved" {
		t.Fatalf("expansion: value=%q err=%v", value, err)
	}
	const missing = "HANOI_CONFIG_UNSET_TEST_VALUE"
	old, existed := os.LookupEnv(missing)
	os.Unsetenv(missing)
	t.Cleanup(func() {
		if existed {
			os.Setenv(missing, old)
		}
	})
	if _, err := ExpandEnv("{env:" + missing + "}"); err == nil {
		t.Fatal("unset variable accepted")
	}
}
