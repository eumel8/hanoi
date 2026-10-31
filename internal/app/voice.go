package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type voiceOptions struct {
	enabled                        bool
	helper, python, whisper, piper string
}

type voiceEvent struct {
	Type  string `json:"type"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

type voiceRequest struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type voiceClient struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   io.ReadCloser
	mu       sync.Mutex
	closed   bool
	requests chan voiceRequest
	notify   func(tea.Msg)
}

func defaultVoicePython() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func startVoice(options voiceOptions) (*voiceClient, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("local voice is currently supported on Windows and macOS; run the native Hanoi binary")
	}
	for _, path := range []string{options.helper, options.whisper, options.piper} {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("voice file %q: %w", path, err)
		}
	}
	cmd := exec.Command(options.python, "-u", options.helper, "--whisper-model", options.whisper, "--piper-model", options.piper)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start voice helper: %w", err)
	}
	return &voiceClient{cmd: cmd, stdin: stdin, stdout: stdout, requests: make(chan voiceRequest, 16)}, nil
}

func (v *voiceClient) listen(notify func(tea.Msg)) {
	v.notify = notify
	go func() {
		for request := range v.requests {
			if err := json.NewEncoder(v.stdin).Encode(request); err != nil {
				notify(voiceEvent{Error: err.Error()})
				return
			}
		}
		_ = v.stdin.Close()
	}()
	go func() {
		scanner := bufio.NewScanner(v.stdout)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		for scanner.Scan() {
			var event voiceEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err == nil {
				notify(event)
			}
		}
		if err := scanner.Err(); err != nil {
			notify(voiceEvent{Error: err.Error()})
		} else {
			notify(voiceEvent{Error: "helper stopped"})
		}
	}()
}

func (v *voiceClient) send(kind, text string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.closed {
		select {
		case v.requests <- voiceRequest{Type: kind, Text: text}:
		default:
			if v.notify != nil {
				go v.notify(voiceEvent{Error: "voice helper is busy"})
			}
		}
	}
}

func (v *voiceClient) close() {
	v.mu.Lock()
	v.closed = true
	close(v.requests)
	v.mu.Unlock()
	done := make(chan struct{})
	go func() { _ = v.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = v.cmd.Process.Kill()
		<-done
	}
}
