// Package trace records provider-independent execution lifecycle events as JSONL.
package trace

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
type Event struct {
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

type Recorder struct {
	mu       sync.Mutex
	writer   io.Writer
	sequence int64
	turn     int
	err      error
}

type Run struct {
	recorder *Recorder
	id       string
	turn     int
	model    string
	onEvent  func(Event)
}

// New creates a recorder. A nil writer enables callback-only recording.
func New(writer io.Writer) *Recorder {
	return &Recorder{writer: writer}
}

// Begin starts a turn. Its finish function must be called with the turn outcome.
// A nil recorder disables tracing.
func (recorder *Recorder) Begin(model, text string, onEvent func(Event)) (*Run, func(error) error) {
	if recorder == nil {
		return nil, func(err error) error { return err }
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, func(error) error { return fmt.Errorf("trace run ID: %w", err) }
	}
	recorder.mu.Lock()
	recorder.turn++
	run := &Run{recorder: recorder, id: hex.EncodeToString(id[:]), turn: recorder.turn, model: model, onEvent: onEvent}
	recorder.mu.Unlock()
	started := time.Now()
	run.Emit(Event{Type: "turn.started", Text: text})
	return run, func(err error) error {
		e := Event{Type: "turn.completed", Status: "completed", DurationMS: Duration(started)}
		if err != nil {
			e.Status, e.Error = "error", err.Error()
		}
		run.Emit(e)
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		if recorder.err != nil && err == nil {
			return fmt.Errorf("write trace: %w", recorder.err)
		}
		return err
	}
}

// Emit records an event in this turn. A nil run is a no-op.
func (r *Run) Emit(e Event) {
	if r == nil {
		return
	}
	e.Time, e.RunID, e.Turn, e.Model = time.Now().UTC(), r.id, r.turn, r.model
	r.recorder.mu.Lock()
	r.recorder.sequence++
	e.Sequence = r.recorder.sequence
	if r.recorder.writer != nil && r.recorder.err == nil {
		r.recorder.err = json.NewEncoder(r.recorder.writer).Encode(e)
	}
	r.recorder.mu.Unlock()
	if r.onEvent != nil {
		r.onEvent(e)
	}
}

// Duration returns elapsed milliseconds since start.
func Duration(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
