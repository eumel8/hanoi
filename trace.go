package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Trace records lifecycle metadata, not API payloads or complete tool output.
type traceEvent struct {
	Sequence    int64     `json:"sequence"`
	Time        time.Time `json:"time"`
	RunID       string    `json:"run_id"`
	Turn        int       `json:"turn"`
	Model       string    `json:"model"`
	Type        string    `json:"type"`
	Status      string    `json:"status,omitempty"`
	Text        string    `json:"text,omitempty"`
	ToolCallID  string    `json:"tool_call_id,omitempty"`
	Tool        string    `json:"tool,omitempty"`
	Arguments   string    `json:"arguments,omitempty"`
	DurationMS  float64   `json:"duration_ms,omitempty"`
	OutputBytes int       `json:"output_bytes,omitempty"`
	ResultBytes int       `json:"result_bytes,omitempty"`
	ToolCalls   int       `json:"tool_calls,omitempty"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type traceRecorder struct {
	mu       sync.Mutex
	writer   io.Writer
	sequence int64
	turn     int
	err      error
}

type traceRun struct {
	recorder *traceRecorder
	id       string
	turn     int
}

func (cfg config) beginTrace(messages []message) (config, func(error) error) {
	if cfg.traceRun != nil || cfg.Trace == nil {
		return cfg, func(err error) error { return err }
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return cfg, func(error) error { return fmt.Errorf("trace run ID: %w", err) }
	}
	cfg.Trace.mu.Lock()
	cfg.Trace.turn++
	cfg.traceRun = &traceRun{recorder: cfg.Trace, id: hex.EncodeToString(id[:]), turn: cfg.Trace.turn}
	cfg.Trace.mu.Unlock()
	text := ""
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			text = messages[i].Content
			break
		}
	}
	started := time.Now()
	cfg.emitTrace(traceEvent{Type: "turn.started", Text: text})
	return cfg, func(err error) error {
		e := traceEvent{Type: "turn.completed", Status: "completed", DurationMS: traceDuration(started)}
		if err != nil {
			e.Status, e.Error = "error", err.Error()
		}
		cfg.emitTrace(e)
		cfg.Trace.mu.Lock()
		defer cfg.Trace.mu.Unlock()
		if cfg.Trace.err != nil && err == nil {
			return fmt.Errorf("write trace: %w", cfg.Trace.err)
		}
		return err
	}
}

func (cfg config) emitTrace(e traceEvent) {
	if cfg.traceRun == nil {
		return
	}
	r := cfg.traceRun
	e.Time, e.RunID, e.Turn, e.Model = time.Now().UTC(), r.id, r.turn, cfg.ModelReference()
	r.recorder.mu.Lock()
	r.recorder.sequence++
	e.Sequence = r.recorder.sequence
	if r.recorder.writer != nil && r.recorder.err == nil {
		r.recorder.err = json.NewEncoder(r.recorder.writer).Encode(e)
	}
	r.recorder.mu.Unlock()
	if cfg.OnTrace != nil {
		cfg.OnTrace(e)
	}
}

func traceDuration(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
