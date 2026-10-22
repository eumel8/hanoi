package main

import (
	"log"

	appconfig "hanoi/internal/config"
)

// config combines model settings with application-owned runtime services.
type config struct {
	appconfig.Config
	Logger   *log.Logger
	MCP      *mcpManager
	OnStatus func(string)
}

func (cfg config) reportStatus(status string) {
	if cfg.OnStatus != nil {
		cfg.OnStatus(status)
	}
}

func loadConfig(explicit, selection string) (config, error) {
	settings, err := appconfig.Load(explicit, selection)
	return config{Config: settings}, err
}

func (cfg config) selectModel(selection string) (config, error) {
	settings, err := cfg.Config.SelectModel(selection)
	if err != nil {
		return cfg, err
	}
	cfg.Config = settings
	return cfg, nil
}
