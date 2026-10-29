package app

import (
	"context"
	"errors"
	"log"
	"os"

	appconfig "hanoi/internal/config"
	"hanoi/internal/trace"
)

// config combines model settings with application-owned runtime services.
type config struct {
	appconfig.Config
	Logger     *log.Logger
	MCP        *mcpManager
	OnStatus   func(string)
	Trace      *trace.Recorder
	OnTrace    func(traceEvent)
	traceRun   *trace.Run
	ConfigPath string
	LogPath    string
	TracePath  string
	Context    context.Context
}

func (cfg config) context() context.Context {
	if cfg.Context != nil {
		return cfg.Context
	}
	return context.Background()
}

func (cfg config) reportStatus(status string) {
	if cfg.OnStatus != nil {
		cfg.OnStatus(status)
	}
}

type traceEvent = trace.Event

func (cfg config) beginTrace(messages []message) (config, func(error) error) {
	if cfg.traceRun != nil {
		return cfg, func(err error) error { return err }
	}
	text := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			text = messages[i].Content
			break
		}
	}
	var finish func(error) error
	cfg.traceRun, finish = cfg.Trace.Begin(cfg.ModelReference(), text, cfg.OnTrace)
	return cfg, finish
}

func (cfg config) emitTrace(event traceEvent) {
	cfg.traceRun.Emit(event)
}

func loadConfig(explicit, selection string) (config, error) {
	settings, err := appconfig.Load(explicit, selection)
	// A saved Copilot login is the default when no provider or legacy model
	// settings were selected. Explicit configuration always takes precedence.
	if errors.Is(err, appconfig.ErrMissingAPIKey) && selection == "" && os.Getenv("OPENAI_MODEL") == "" && os.Getenv("OPENAI_BASE_URL") == "" {
		if _, sessionErr := loadCopilotSession(); sessionErr == nil {
			settings, err = appconfig.Load(explicit, "github-copilot/gpt-4.1")
		}
	}
	if err != nil {
		return config{}, err
	}
	path, err := appconfig.FindFile(explicit)
	return config{Config: settings, ConfigPath: path}, err
}

func (cfg config) selectModel(selection string) (config, error) {
	settings, err := cfg.Config.SelectModel(selection)
	if err != nil {
		return cfg, err
	}
	cfg.Config = settings
	return cfg, nil
}
