package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hanoi/internal/tools"
	"hanoi/internal/trace"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
	Tools    []tool    `json:"tools,omitempty"`
	Stream   bool      `json:"stream,omitempty"`
}

type tool struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []toolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type streamResponse struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index *int `json:"index"`
				toolCall
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type chatResultMsg struct {
	text string
	err  error
}

type chatChunkMsg struct {
	text string
}

type streamRenderMsg struct{}

type chatStatusMsg string

// chatTickMsg refreshes the status line while a tool call is running.
type chatTickMsg struct{}

type tuiModel struct {
	cfg            config
	memoryName     string
	messages       []message
	history        []string
	textarea       textarea.Model
	viewport       viewport.Model
	inputHistory   []string
	historyIndex   int
	historyDraft   string
	renderPending  bool
	spinner        spinner.Model
	width          int
	height         int
	loading        bool
	activity       string
	streaming      string
	userScrolled   bool
	activityBase   string
	activityLabel  string
	activitySince  time.Time
	activityTicker bool
	lastScrollKey  string
	streamRendered string
	markdown       *glamour.TermRenderer
	err            string
	system         string
	pendingMessage []message
	mcpOpen        bool
	mcpBusy        bool
	mcpCursor      int
	mcpInfo        string
	providersOpen  bool
	providerCursor int
	traceOpen      bool
	traceEvents    []traceEvent
	chatOffset     int
}

var (
	appStyle      = lipgloss.NewStyle().Padding(1, 2)
	headStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	userStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	botStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	hintStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	boxStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
	streamProgram *tea.Program
)

// Run starts the CLI or interactive application with the given build version.
func Run(version string) error {
	stream := flag.Bool("stream", false, "stream output tokens as they arrive")
	memory := flag.String("memory", "", "conversation id to persist under .hanoi/")
	tui := flag.Bool("tui", false, "interactive chat mode")
	logPath := flag.String("log", "", "API log file (includes prompts and tool output)")
	tracePath := flag.String("trace", "", "structured lifecycle trace file (JSONL)")
	configPath := flag.String("config", "", "config file (default: hanoi.json[c] or opencode.json[c])")
	model := flag.String("model", "", "model to use (provider/model or legacy model name)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("hanoi " + version)
		return nil
	}

	cfg, err := loadConfig(*configPath, *model)
	if err != nil {
		return err
	}

	if *logPath != "" {
		file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open log: %w", err)
		}
		defer file.Close()
		cfg.Logger = log.New(file, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.LUTC)
	}
	if *tracePath != "" {
		file, err := os.OpenFile(*tracePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open trace: %w", err)
		}
		defer file.Close()
		cfg.Trace = trace.New(file)
	}

	cfg.MCP, err = newMCPManager(*configPath, cfg.Logger)
	if err != nil {
		return fmt.Errorf("MCP config: %w", err)
	}
	defer cfg.MCP.Close()

	if *tui {
		return runTUI(cfg, *memory)
	}

	if flag.NArg() < 1 {
		return fmt.Errorf("usage: %s [-stream] [-memory chat1] [-tui] [-log api.log] [-trace trace.jsonl] [-config hanoi.json] [-model provider/model] \"your prompt\"", os.Args[0])
	}

	prompt := strings.Join(flag.Args(), " ")

	messages := []message{}
	if *memory != "" {
		messages, err = loadMemory(*memory)
		if err != nil {
			return fmt.Errorf("load memory: %w", err)
		}
	}
	messages = append(messages, message{Role: "user", Content: prompt})

	assistantText, err := sendChat(cfg, messages, *stream)
	if err != nil {
		return err
	}
	if !*stream {
		fmt.Println(assistantText)
	}

	if *memory != "" {
		messages = append(messages, message{Role: "assistant", Content: assistantText})
		if err := saveMemory(*memory, messages); err != nil {
			return fmt.Errorf("save memory: %w", err)
		}
	}
	return nil
}

func runTUI(cfg config, memoryName string) error {
	if cfg.Trace == nil {
		cfg.Trace = trace.New(nil)
	}
	messages := []message{}
	var err error
	if memoryName != "" {
		messages, err = loadMemory(memoryName)
		if err != nil {
			return fmt.Errorf("load memory: %w", err)
		}
	}

	ta := textarea.New()
	ta.Placeholder = "Type a message. Enter sends, Alt+Enter adds a newline. /exit quits."
	ta.Focus()
	ta.SetHeight(4)
	ta.ShowLineNumbers = false
	ta.Prompt = "│ "
	ta.CharLimit = 0

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = hintStyle

	m := tuiModel{
		cfg:        cfg,
		memoryName: memoryName,
		messages:   messages,
		textarea:   ta,
		viewport:   viewport.New(70, 10),
		spinner:    sp,
		system:     "",
	}
	for _, msg := range messages {
		if msg.Role == "user" {
			m.rememberInput(msg.Content)
		}
	}
	// Detect the terminal background before Bubble Tea takes over terminal input.
	m.resizeChat()
	m.renderMarkdown("")
	m.rebuildHistory()

	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseAllMotion())
	streamProgram = p
	_, err = p.Run()
	return err
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeChat()
		m.markdown = nil
		m.rebuildHistory()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			return m, tea.Quit
		}

		if m.mcpBusy {
			if msg.String() == "esc" && m.cfg.MCP != nil {
				m.cfg.MCP.CancelAuth()
				m.mcpInfo = "Cancelling OAuth login..."
			}
			return m, nil
		}
		if m.mcpOpen {
			return m.updateMCPMenu(msg)
		}
		if m.providersOpen {
			return m.updateProviderMenu(msg)
		}
		if msg.String() == "ctrl+t" || (m.traceOpen && msg.String() == "esc") {
			m.toggleTrace()
			return m, nil
		}
		if m.scrollChat(msg) {
			m.userScrolled = true
			m.refreshChat()
			return m, nil
		}
		if m.loading {
			return m, nil
		}

		switch msg.String() {
		case "up", "down", "alt+up", "alt+down":
			if m.recallInput(msg.String()) {
				return m, nil
			}
		case "enter":
			prompt := strings.TrimSpace(m.textarea.Value())
			if prompt == "" {
				return m, nil
			}
			m.rememberInput(prompt)
			if strings.HasPrefix(prompt, "/") {
				return m.handleSlashCommand(prompt)
			}

			m.pendingMessage = appendMessage(m.messages, m.system, message{Role: "user", Content: prompt})
			m.messages = append([]message{}, m.pendingMessage...)
			m.textarea.SetValue("")
			m.loading = true
			m.activity = "waiting for model..."
			m.activityBase = m.activity
			m.activityLabel = ""
			m.activitySince = time.Now()
			m.activityTicker = false
			m.streaming = ""
			m.err = ""
			m.userScrolled = false
			m.rebuildHistory()
			m.viewport.GotoBottom()
			return m, tea.Batch(m.spinner.Tick, streamChatCmd(m.cfg, m.pendingMessage))
		case "alt+enter":
			m.textarea.InsertRune('\n')
			return m, nil
		}

		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd

	case chatChunkMsg:
		m.streaming += msg.text
		if !m.renderPending {
			m.renderPending = true
			return m, tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return streamRenderMsg{} })
		}
		return m, nil

	case traceEvent:
		m.traceEvents = append(m.traceEvents, msg)
		if len(m.traceEvents) > 2000 {
			m.traceEvents = append([]traceEvent(nil), m.traceEvents[len(m.traceEvents)-2000:]...)
		}
		if m.traceOpen {
			m.refreshChat()
		}
		return m, nil

	case chatStatusMsg:
		m.activityBase = string(msg)
		m.activity = m.activityBase
		m.activityLabel = ""
		m.activitySince = time.Now()
		return m, m.toolTickCmd()

	case chatTickMsg:
		if !m.toolTickCmdActive() {
			m.activityTicker = false
			return m, nil
		}
		since := time.Since(m.activitySince)
		if since >= time.Second {
			label := m.activityBase + " · " + fmtDuration(since)
			if label != m.activityLabel {
				m.activityLabel = label
				if since >= 30*time.Second {
					m.activity = warnStyle.Render(label)
				} else {
					m.activity = label
				}
			}
		}
		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return chatTickMsg{} })

	case streamRenderMsg:
		m.renderPending = false
		if m.streaming == "" {
			return m, nil
		}
		m.streamRendered = m.renderMarkdown(m.streaming)
		m.refreshChat()
		return m, nil

	case tea.MouseMsg:
		if !m.mcpOpen && !m.providersOpen && !m.mcpBusy {
			// Explicitly handle scroll wheel events for WSL/Touchpad compatibility.
			switch tea.MouseMsg(msg).Button {
			case tea.MouseButtonWheelUp:
				m.userScrolled = true
				m.viewport.LineUp(3)
			case tea.MouseButtonWheelDown:
				m.userScrolled = true
				m.viewport.LineDown(3)
			case tea.MouseButtonWheelLeft:
				m.userScrolled = true
				m.viewport.LineUp(3)
			case tea.MouseButtonWheelRight:
				m.userScrolled = true
				m.viewport.LineDown(3)
			default:
				m.viewport, _ = m.viewport.Update(msg)
			}
		}
		return m, nil

	case chatResultMsg:
		m.loading = false
		m.renderPending = false
		m.activityTicker = false
		m.activity = ""
		m.activityBase = ""
		m.activityLabel = ""
		m.activitySince = time.Time{}
		if !m.traceOpen {
			m.userScrolled = false
		}
		if msg.err != nil {
			m.err = msg.err.Error()
			m.streaming = ""
			m.messages = m.messages[:max(0, len(m.messages)-1)]
			m.rebuildHistory()
			return m, nil
		}

		m.streaming = ""
		m.messages = append(m.messages, message{Role: "assistant", Content: msg.text})
		if m.memoryName != "" {
			if err := saveMemory(m.memoryName, m.messages); err != nil {
				m.err = fmt.Sprintf("save memory: %v", err)
			}
		}
		m.rebuildHistory()
		return m, nil

	case mcpResultMsg:
		m.mcpBusy = false
		m.mcpInfo = msg.text
		if msg.err != nil {
			m.mcpInfo = msg.err.Error()
		}
		return m, nil

	default:
		var cmds []tea.Cmd
		if m.loading || m.mcpBusy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
		m.textarea, _ = m.textarea.Update(msg)
		return m, tea.Batch(cmds...)
	}
}

// toolTickCmdActive reports whether the elapsed-time ticker should keep running.
func (m *tuiModel) toolTickCmdActive() bool {
	return m.loading && strings.HasPrefix(m.activityBase, "running tool:")
}

// toolTickCmd schedules the elapsed-time ticker for a running tool, once.
func (m *tuiModel) toolTickCmd() tea.Cmd {
	if !m.toolTickCmdActive() {
		m.activityTicker = false
		return nil
	}
	if m.activityTicker {
		return nil
	}
	m.activityTicker = true
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return chatTickMsg{} })
}

func (m tuiModel) View() string {
	header := m.chatHeader()
	body := m.chatViewport()
	if m.mcpOpen {
		body = m.mcpView()
	}
	if m.providersOpen {
		body = m.providerView()
	}

	status := hintStyle.Render("ready")
	if m.loading {
		activity := m.activity
		if activity == "" {
			activity = "waiting for model..."
		}
		status = m.spinner.View() + " " + activity
	}
	if m.mcpBusy {
		status = m.spinner.View() + " updating MCP servers..."
	}
	if m.err != "" {
		status = errStyle.Render(m.err)
	}

	content := strings.Join([]string{
		strings.Join(header, "\n"),
		"",
		boxStyle.Width(max(20, m.width-6)).Render(body),
		"",
		status,
		boxStyle.Width(max(20, m.width-6)).Render(m.textarea.View()),
	}, "\n")

	return appStyle.Render(content)
}

func (m *tuiModel) rebuildHistory() {
	m.resizeChat()
	m.history = nil
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			m.history = append(m.history, userStyle.Render("you")+": "+msg.Content)
		case "assistant":
			m.history = append(m.history, botStyle.Render("assistant")+"\n"+m.renderMarkdown(msg.Content))
		case "system":
			m.history = append(m.history, hintStyle.Render("system: "+msg.Content))
		}
	}
	if m.streaming != "" {
		m.streamRendered = m.renderMarkdown(m.streaming)
	}
	m.refreshChat()
}

func (m *tuiModel) renderMarkdown(text string) string {
	if m.markdown == nil {
		style := "light"
		if lipgloss.HasDarkBackground() {
			style = "dark"
		}
		width := max(1, m.viewport.Width)
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(style),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return text
		}
		m.markdown = renderer
	}
	rendered, err := m.markdown.Render(text)
	if err != nil {
		return text
	}
	return strings.Trim(rendered, "\n")
}

func (m tuiModel) handleSlashCommand(input string) (tea.Model, tea.Cmd) {
	parts := strings.SplitN(strings.TrimSpace(input), " ", 2)
	cmd := parts[0]
	arg := ""
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "/exit", "/quit":
		return m, tea.Quit
	case "/clear":
		m.messages = nil
		m.streaming = ""
		m.rebuildHistory()
		m.err = ""
		if m.memoryName != "" {
			if err := saveMemory(m.memoryName, m.messages); err != nil {
				m.err = fmt.Sprintf("save memory: %v", err)
			}
		}
		return m, nil
	case "/save":
		if m.memoryName == "" {
			m.err = "no memory name set; restart with -memory chat1"
			return m, nil
		}
		if err := saveMemory(m.memoryName, m.messages); err != nil {
			m.err = fmt.Sprintf("save memory: %v", err)
			return m, nil
		}
		m.err = "saved"
		return m, nil
	case "/model":
		if arg == "" {
			m.providersOpen, m.err = true, ""
		} else if cfg, err := m.cfg.selectModel(arg); err != nil {
			m.err = err.Error()
		} else {
			m.cfg, m.err = cfg, ""
		}
		m.textarea.SetValue("")
		return m, nil
	case "/providers", "/models":
		m.providersOpen, m.err = true, ""
		m.textarea.SetValue("")
		return m, nil
	case "/system":
		m.system = arg
		m.err = ""
		return m, nil
	case "/help":
		m.err = "Commands: /clear /save /model [provider/model] /providers /models /system <text> /mcps /trace /exit\nScroll: Ctrl+↑↓ (3 lines) · 2x Ctrl+↑↓ (top/bottom) · Ctrl+T (trace) · ↑/↓ (input history)"
		return m, nil
	case "/trace":
		m.textarea.SetValue("")
		m.toggleTrace()
		return m, nil
	case "/mcps":
		m.textarea.SetValue("")
		return m.handleMCPCommand(arg)
	default:
		m.err = "unknown command"
		return m, nil
	}
}

func appendMessage(messages []message, system string, user message) []message {
	if system == "" {
		return append(messages, user)
	}

	filtered := make([]message, 0, len(messages)+2)
	inserted := false
	for _, msg := range messages {
		if msg.Role == "system" {
			if !inserted {
				filtered = append(filtered, message{Role: "system", Content: system})
				inserted = true
			}
			continue
		}
		filtered = append(filtered, msg)
	}
	if !inserted {
		filtered = append([]message{{Role: "system", Content: system}}, filtered...)
	}
	filtered = append(filtered, user)
	return filtered
}

func streamChatCmd(cfg config, messages []message) tea.Cmd {
	return func() tea.Msg {
		cfg.OnTrace = func(event traceEvent) {
			if streamProgram != nil {
				streamProgram.Send(event)
			}
		}
		cfg.OnStatus = func(status string) {
			if streamProgram != nil {
				streamProgram.Send(chatStatusMsg(status))
			}
		}
		text, err := sendStreamingChat(cfg, messages, func(piece string) {
			if streamProgram != nil {
				streamProgram.Send(chatChunkMsg{text: piece})
			}
		})
		return chatResultMsg{text: text, err: err}
	}
}

// apiLogWriter quotes body chunks so multiline and streamed responses stay readable.
type apiLogWriter struct {
	logger *log.Logger
}

func (w apiLogWriter) Write(p []byte) (int, error) {
	w.logger.Printf("response body_chunk=%q", p)
	return len(p), nil
}

type loggedResponseBody struct {
	io.Reader
	io.Closer
}

func sendChatRequest(cfg config, payload []byte) (*http.Response, error) {
	cfg.reportStatus("waiting for model...")
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range cfg.Headers {
		req.Header.Set(name, value)
	}
	if cfg.Logger != nil {
		cfg.Logger.Printf("request body=%s", payload)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if cfg.Logger != nil {
			// Avoid logging URLs, which may contain credentials or query secrets.
			cfg.Logger.Print("request failed (transport error)")
		}
		return nil, fmt.Errorf("send request: %w", err)
	}
	if cfg.Logger != nil {
		cfg.Logger.Printf("response status=%q", resp.Status)
		resp.Body = loggedResponseBody{
			Reader: io.TeeReader(resp.Body, apiLogWriter{logger: cfg.Logger}),
			Closer: resp.Body,
		}
	}
	cfg.reportStatus("receiving model response...")
	return resp, nil
}

func sendChat(cfg config, messages []message, stream bool) (text string, err error) {
	cfg, finish := cfg.beginTrace(messages)
	defer func() { err = finish(err) }()
	if stream {
		return sendStreamingChat(cfg, messages, func(piece string) {
			fmt.Print(piece)
		})
	}

	msg, err := requestModel(cfg, messages, false, nil)
	if err != nil {
		return "", err
	}
	if len(msg.ToolCalls) == 0 {
		return msg.Content, nil
	}

	toolMessages, err := executeToolCalls(msg.ToolCalls, cfg)
	if err != nil {
		return "", err
	}

	followupMessages := append(append([]message{}, messages...), message{
		Role:      msg.Role,
		Content:   msg.Content,
		ToolCalls: msg.ToolCalls,
	})
	followupMessages = append(followupMessages, toolMessages...)
	return sendChat(cfg, followupMessages, false)
}

func sendStreamingChat(cfg config, messages []message, onChunk func(string)) (text string, err error) {
	cfg, finish := cfg.beginTrace(messages)
	defer func() { err = finish(err) }()
	messages = append([]message{}, messages...)
	var out strings.Builder
	for {
		msg, err := requestModel(cfg, messages, true, onChunk)
		if err != nil {
			return "", err
		}
		out.WriteString(msg.Content)
		if len(msg.ToolCalls) == 0 {
			if onChunk != nil {
				onChunk("\n")
			}
			return out.String(), nil
		}

		toolMessages, err := executeToolCalls(msg.ToolCalls, cfg)
		if err != nil {
			return "", err
		}
		messages = append(messages, msg)
		messages = append(messages, toolMessages...)
	}
}

func requestModel(cfg config, messages []message, stream bool, onChunk func(string)) (msg message, err error) {
	started := time.Now()
	cfg.emitTrace(traceEvent{Type: "model.started"})
	defer func() {
		e := traceEvent{Type: "model.completed", Status: "completed", DurationMS: trace.Duration(started), ToolCalls: len(msg.ToolCalls), OutputBytes: len(msg.Content)}
		if err != nil {
			e.Status, e.Error = "error", err.Error()
		}
		cfg.emitTrace(e)
	}()
	payload, err := json.Marshal(chatRequest{Model: cfg.Model, Messages: messages, Tools: availableTools(cfg), Stream: stream})
	if err != nil {
		return msg, fmt.Errorf("marshal request: %w", err)
	}
	resp, err := sendChatRequest(cfg, payload)
	if err != nil {
		return msg, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return msg, fmt.Errorf("api error (%s)", resp.Status)
		}
		return msg, fmt.Errorf("api error (%s): %s", resp.Status, body)
	}
	if stream {
		return handleStream(resp.Body, onChunk)
	}
	var result chatResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return msg, fmt.Errorf("read response: %w", err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return msg, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return msg, fmt.Errorf("no choices returned")
	}
	choice := result.Choices[0].Message
	return message{Role: choice.Role, Content: choice.Content, ToolCalls: choice.ToolCalls}, nil
}

func handleStream(body io.Reader, onChunk func(string)) (message, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	var out strings.Builder
	calls := make(map[int]*toolCall)
	finishReason := ""

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			msg := message{Role: "assistant", Content: out.String()}
			if len(calls) > 0 {
				if finishReason != "tool_calls" {
					return message{}, fmt.Errorf("incomplete tool-call stream (finish_reason=%q)", finishReason)
				}
				indices := make([]int, 0, len(calls))
				for index := range calls {
					indices = append(indices, index)
				}
				sort.Ints(indices)
				for _, index := range indices {
					call := calls[index]
					if call.ID == "" || call.Type != "function" || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
						return message{}, fmt.Errorf("invalid streamed tool call at index %d", index)
					}
					msg.ToolCalls = append(msg.ToolCalls, *call)
				}
			}
			return msg, nil
		}

		var chunk streamResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return message{}, fmt.Errorf("decode stream chunk: %w", err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
			piece := choice.Delta.Content
			if piece != "" {
				if onChunk != nil {
					onChunk(piece)
				}
				out.WriteString(piece)
			}
			for _, delta := range choice.Delta.ToolCalls {
				if delta.Index == nil || *delta.Index < 0 {
					return message{}, fmt.Errorf("streamed tool call has missing or invalid index")
				}
				call := calls[*delta.Index]
				if call == nil {
					call = &toolCall{}
					calls[*delta.Index] = call
				}
				call.ID += delta.ID
				if delta.Type != "" {
					if call.Type != "" && call.Type != delta.Type {
						return message{}, fmt.Errorf("conflicting streamed tool call type at index %d", *delta.Index)
					}
					call.Type = delta.Type
				}
				call.Function.Name += delta.Function.Name
				call.Function.Arguments += delta.Function.Arguments
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return message{}, fmt.Errorf("read stream: %w", err)
	}

	return message{}, fmt.Errorf("read stream: %w (missing [DONE])", io.ErrUnexpectedEOF)
}

func loadMemory(name string) ([]message, error) {
	path := memoryPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var messages []message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func saveMemory(name string, messages []message) error {
	path := memoryPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(messages, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func memoryPath(name string) string {
	return filepath.Join(".hanoi", name+".json")
}

func availableTools(cfg config) []tool {
	tools := []tool{
		{
			Type: "function",
			Function: toolFunction{
				Name:        "bash",
				Description: fmt.Sprintf("Run a local bash command and return stdout and stderr. Commands time out after 30 seconds. Working directory: %q. Saved tool results are in %q; check that directory directly, never search the entire filesystem. For MCP JSON files, parse content[0].text as JSON. Reuse saved results for follow-up questions.", tools.WorkDir(), tools.ResultsDir()),
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command": map[string]any{
							"type":        "string",
							"description": "The bash command to execute locally.",
						},
					},
					"required":             []string{"command"},
					"additionalProperties": false,
				},
			},
		},
	}
	return append(tools, cfg.MCP.Tools()...)
}

func executeToolCalls(calls []toolCall, cfg config) ([]message, error) {
	toolMessages := make([]message, 0, len(calls))
	for _, call := range calls {
		if call.Type != "function" {
			return nil, fmt.Errorf("unsupported tool call type: %s", call.Type)
		}

		var output string
		var err error
		cfg.reportStatus("running tool: " + call.Function.Name + " " + toolArgsSummary(call))
		started := time.Now()
		cfg.emitTrace(traceEvent{Type: "tool.started", Tool: call.Function.Name, ToolCallID: call.ID, Arguments: call.Function.Arguments})
		if cfg.Logger != nil {
			cfg.Logger.Printf("tool name=%q args=%q started", call.Function.Name, call.Function.Arguments)
		}
		switch call.Function.Name {
		case "bash":
			output, err = tools.RunBash(call.Function.Arguments)
		default:
			output, err = cfg.MCP.Call(call.Function.Name, call.Function.Arguments)
		}
		if cfg.Logger != nil {
			cfg.Logger.Printf("tool name=%q finished duration=%s failed=%t", call.Function.Name, time.Since(started), err != nil)
		}
		event := traceEvent{Type: "tool.completed", Tool: call.Function.Name, ToolCallID: call.ID, DurationMS: trace.Duration(started), OutputBytes: len(output), Status: "completed"}
		if call.Function.Name != "bash" && err == nil {
			var result struct {
				IsError bool `json:"isError"`
			}
			if json.Unmarshal([]byte(output), &result) == nil && result.IsError {
				event.Status, event.Error = "error", "MCP tool returned isError=true"
			}
		}
		if err != nil {
			event.Status, event.Error = "error", err.Error()
			if errors.Is(err, context.DeadlineExceeded) {
				event.Status = "timeout"
			}
		}
		if call.Function.Name == "bash" {
			var exitErr *exec.ExitError
			if err == nil {
				code := 0
				event.ExitCode = &code
			} else if errors.As(err, &exitErr) {
				code := exitErr.ExitCode()
				event.ExitCode = &code
			}
		}
		cfg.emitTrace(event)
		if err != nil {
			output = err.Error() + "\n" + output
		}
		output, err = tools.PrepareOutput(output)
		if err != nil {
			return nil, fmt.Errorf("prepare tool result for %s: %w", call.Function.Name, err)
		}
		toolMessages = append(toolMessages, message{
			Role: "tool", Name: call.Function.Name, ToolCallID: call.ID, Content: output,
		})
		cfg.emitTrace(traceEvent{Type: "tool.result", Tool: call.Function.Name, ToolCallID: call.ID, ResultBytes: len(output)})
	}
	return toolMessages, nil
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
