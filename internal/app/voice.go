package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type voiceOptions struct {
	enabled                                 bool
	input, whisper, piper, debugDir, prompt string
}

type voiceEvent struct {
	Type  string
	Text  string
	Error string
}

type voiceRequest struct {
	Type string
	Text string
}

// voiceClient owns the external audio processes. Only its loop changes the
// recording state; transcription and playback run independently of the TUI.
type voiceClient struct {
	options        voiceOptions
	dir            string
	notify         func(tea.Msg)
	requests       chan voiceRequest
	done           chan struct{}
	mu             sync.Mutex
	closed         bool
	record         *exec.Cmd
	recordIn       io.WriteCloser
	recordErr      *bytes.Buffer
	playStop       context.CancelFunc
	transcribeStop context.CancelFunc
	workers        sync.WaitGroup
}

func startVoice(options voiceOptions) (*voiceClient, error) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		return nil, errors.New("local voice requires the native Windows or macOS binary")
	}
	if strings.TrimSpace(options.input) == "" {
		return nil, errors.New("-voice-input is required (FFmpeg microphone device)")
	}
	for _, tool := range []string{"ffmpeg", "ffplay", "whisper-cli", "piper"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("voice requires %s on PATH: %w", tool, err)
		}
	}
	for _, path := range []string{options.whisper, options.piper, options.piper + ".json"} {
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("voice model %q: %w", path, err)
		}
	}
	if options.debugDir != "" {
		if err := os.MkdirAll(options.debugDir, 0o700); err != nil {
			return nil, fmt.Errorf("create voice debug directory: %w", err)
		}
		info, err := os.Stat(options.debugDir)
		if err != nil || !info.IsDir() {
			return nil, fmt.Errorf("voice debug directory %q is not a directory", options.debugDir)
		}
	}
	dir, err := os.MkdirTemp("", "hanoi-voice-")
	if err != nil {
		return nil, err
	}
	return &voiceClient{options: options, dir: dir, requests: make(chan voiceRequest, 16), done: make(chan struct{})}, nil
}

func (v *voiceClient) listen(notify func(tea.Msg)) {
	v.notify = notify
	go v.loop()
}

func (v *voiceClient) send(kind, text string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.closed {
		select {
		case v.requests <- voiceRequest{Type: kind, Text: text}:
		default:
			go v.notify(voiceEvent{Error: "voice helper is busy"})
		}
	}
}

func (v *voiceClient) loop() {
	defer close(v.done)
	for request := range v.requests {
		switch request.Type {
		case "start":
			if err := v.startRecording(); err != nil {
				v.notify(voiceEvent{Error: err.Error()})
			}
		case "stop":
			if err := v.stopRecording(true); err != nil {
				v.notify(voiceEvent{Error: err.Error()})
			}
		case "speak":
			v.speak(request.Text)
		}
	}
	if v.playStop != nil {
		v.playStop()
	}
	if v.transcribeStop != nil {
		v.transcribeStop()
	}
	_ = v.stopRecording(false)
	v.workers.Wait()
	_ = os.RemoveAll(v.dir)
}

func (v *voiceClient) startRecording() error {
	if v.record != nil {
		return nil
	}
	if v.playStop != nil {
		v.playStop()
		v.playStop = nil
	}
	inputArgs, err := voiceCaptureArgs(runtime.GOOS, v.options.input)
	if err != nil {
		return err
	}
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, inputArgs...)
	args = append(args, "-ar", "16000", "-ac", "1", "-c:a", "pcm_s16le", filepath.Join(v.dir, "input.wav"))
	cmd := exec.Command("ffmpeg", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start microphone: %w", err)
	}
	v.record, v.recordIn, v.recordErr = cmd, stdin, &stderr
	v.notify(voiceEvent{Type: "recording"})
	return nil
}

func voiceCaptureArgs(goos, input string) ([]string, error) {
	switch goos {
	case "windows":
		return []string{"-f", "dshow", "-i", "audio=" + input}, nil
	case "darwin":
		return []string{"-f", "avfoundation", "-i", ":" + input}, nil
	default:
		return nil, errors.New("unsupported voice platform")
	}
}

func (v *voiceClient) stopRecording(transcribe bool) error {
	if v.record == nil {
		return nil
	}
	cmd, stdin, stderr := v.record, v.recordIn, v.recordErr
	v.record, v.recordIn, v.recordErr = nil, nil, nil
	_, _ = io.WriteString(stdin, "q\n")
	_ = stdin.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("microphone did not stop in time")
	}
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			if len(detail) > 400 {
				detail = detail[len(detail)-400:]
			}
			return fmt.Errorf("record microphone: %w: %s", err, detail)
		}
		return fmt.Errorf("record microphone: %w (check -voice-input)", err)
	}
	if transcribe {
		audio := filepath.Join(v.dir, "input.wav")
		var debugBase string
		if v.options.debugDir != "" {
			debugBase = filepath.Join(v.options.debugDir, fmt.Sprintf("voice-%d", time.Now().UnixNano()))
			if err := copyVoiceDebugFile(audio, debugBase+".wav"); err != nil {
				return fmt.Errorf("save voice debug recording: %w", err)
			}
		}
		v.notify(voiceEvent{Type: "transcribing"})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		v.transcribeStop = cancel
		v.workers.Add(1)
		go func() {
			defer v.workers.Done()
			defer cancel()
			v.transcribe(ctx, audio, debugBase)
		}()
	}
	return nil
}

func (v *voiceClient) transcribe(ctx context.Context, audio, debugBase string) {
	output := filepath.Join(v.dir, "transcript")
	cmd := exec.CommandContext(ctx, "whisper-cli", whisperArgs(v.options, audio, output)...)
	if result, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == nil || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			v.notify(voiceEvent{Error: voiceCommandError("transcription", err, result)})
		}
		return
	}
	text, err := os.ReadFile(output + ".txt")
	if err != nil {
		v.notify(voiceEvent{Error: fmt.Sprintf("read transcription: %v", err)})
		return
	}
	if debugBase != "" {
		if err := os.WriteFile(debugBase+".txt", text, 0o600); err != nil {
			v.notify(voiceEvent{Error: fmt.Sprintf("save voice debug transcript: %v", err)})
			return
		}
	}
	v.notify(voiceEvent{Type: "transcript", Text: strings.TrimSpace(string(text))})
}

func whisperArgs(options voiceOptions, audio, output string) []string {
	args := []string{"-m", options.whisper, "-f", audio, "-l", "de", "-nt", "-otxt", "-of", output}
	if prompt := strings.TrimSpace(options.prompt); prompt != "" {
		args = append(args, "--prompt", prompt)
	}
	return args
}

func copyVoiceDebugFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(target)
		return errors.Join(copyErr, closeErr)
	}
	return nil
}

func (v *voiceClient) speak(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if v.playStop != nil {
		v.playStop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.playStop = cancel
	v.workers.Add(1)
	go func() {
		defer v.workers.Done()
		audio := filepath.Join(v.dir, fmt.Sprintf("speech-%d.wav", time.Now().UnixNano()))
		defer os.Remove(audio)
		cmd := exec.CommandContext(ctx, "piper", "--model", v.options.piper, "--output_file", audio)
		cmd.Stdin = strings.NewReader(text)
		if result, err := cmd.CombinedOutput(); err != nil {
			if ctx.Err() == nil {
				v.notify(voiceEvent{Error: voiceCommandError("speech synthesis", err, result)})
			}
			return
		}
		cmd = exec.CommandContext(ctx, "ffplay", "-nodisp", "-autoexit", "-loglevel", "error", audio)
		if result, err := cmd.CombinedOutput(); err != nil && ctx.Err() == nil {
			v.notify(voiceEvent{Error: voiceCommandError("speech playback", err, result)})
		}
	}()
}

func voiceCommandError(operation string, err error, output []byte) string {
	detail := strings.TrimSpace(string(output))
	if len(detail) > 400 {
		detail = detail[len(detail)-400:]
	}
	if detail != "" {
		return fmt.Sprintf("%s: %v: %s", operation, err, detail)
	}
	return fmt.Sprintf("%s: %v", operation, err)
}

func (v *voiceClient) close() {
	v.mu.Lock()
	v.closed = true
	close(v.requests)
	v.mu.Unlock()
	<-v.done
}
