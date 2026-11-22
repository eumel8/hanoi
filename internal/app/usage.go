package app

import "fmt"

// Usage values come from provider responses; never infer billing from text length.
type tokenUsage struct {
	Input           int `json:"prompt_tokens"`
	Output          int `json:"completion_tokens"`
	InputResponses  int `json:"input_tokens"`
	OutputResponses int `json:"output_tokens"`
}

func (u tokenUsage) counts() (int, int) {
	if u.InputResponses != 0 || u.OutputResponses != 0 {
		return u.InputResponses, u.OutputResponses
	}
	return u.Input, u.Output
}

type usageEvent struct {
	Model string
	Usage tokenUsage
	Child bool
}

type sessionUsage struct {
	Input     int     `json:"input"`
	Output    int     `json:"output"`
	LastInput int     `json:"last_input"`
	LastModel string  `json:"last_model,omitempty"`
	Cost      float64 `json:"cost"`
	Priced    bool    `json:"priced"`
	Unpriced  bool    `json:"unpriced"`
	Known     bool    `json:"known"`
	Missing   bool    `json:"missing"`
}

func loadSessionUsage(name string) sessionUsage {
	if name == "" {
		return sessionUsage{}
	}
	archive, err := readMemoryArchive(name)
	if err != nil {
		return sessionUsage{}
	}
	return archive.Usage
}

func (u *sessionUsage) add(event usageEvent, cfg config) {
	in, out := event.Usage.counts()
	if in == 0 && out == 0 {
		u.Missing = true
		u.Unpriced = true
		if !event.Child {
			u.LastModel = ""
		}
		return
	}
	u.Input += in
	u.Output += out
	if !event.Child {
		u.LastInput = in
		u.LastModel = event.Model
	}
	u.Known = true
	model := cfg.SelectedModel()
	if event.Model != cfg.ModelReference() || (model.Cost.Input == 0 && model.Cost.Output == 0) {
		u.Unpriced = true
		return
	}
	u.Priced = true
	u.Cost += (float64(in)*model.Cost.Input + float64(out)*model.Cost.Output) / 1e6
}

func (u sessionUsage) summary(cfg config) string {
	window := "context: ?"
	if size := cfg.SelectedModel().ContextWindow; size > 0 {
		if u.LastModel == cfg.ModelReference() {
			window = fmt.Sprintf("context: %d/%d", u.LastInput, size)
		} else {
			window = fmt.Sprintf("context: ?/%d", size)
		}
	} else if u.LastModel == cfg.ModelReference() {
		window = fmt.Sprintf("context: %d/?", u.LastInput)
	}
	tokens := "session: ? tokens"
	if u.Known {
		tokens = fmt.Sprintf("session: %d in + %d out tokens", u.Input, u.Output)
		if u.Missing {
			tokens += " (partial)"
		}
	}
	cost := "cost: ?"
	if u.Priced && !u.Unpriced {
		cost = fmt.Sprintf("cost: $%.4f", u.Cost)
	}
	return window + " · " + tokens + " · " + cost
}

func (cfg config) recordUsage(usage tokenUsage) {
	if cfg.OnUsage != nil {
		cfg.OnUsage(usageEvent{Model: cfg.ModelReference(), Usage: usage})
	}
}
