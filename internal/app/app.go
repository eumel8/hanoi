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

	"hanoi/internal/skills"
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
	Timestamp      *time.Time        `json:"timestamp,omitempty"`
	Role           string            `json:"role"`
	Content        string            `json:"content"`
	Name           string            `json:"name,omitempty"`
	ToolCallID     string            `json:"tool_call_id,omitempty"`
	ToolCalls      []toolCall        `json:"tool_calls,omitempty"`
	ResponseOutput []json.RawMessage `json:"-"`
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []message `json:"messages"`
	Tools           []tool    `json:"tools,omitempty"`
	Stream          bool      `json:"stream,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
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
	cfg              config
	memoryName       string
	messages         []message
	history          []string
	textarea         textarea.Model
	viewport         viewport.Model
	inputHistory     []string
	historyIndex     int
	historyDraft     string
	completionPrefix string
	completionIndex  int
	renderPending    bool
	spinner          spinner.Model
	width            int
	height           int
	loading          bool
	chatCancel       context.CancelFunc
	cancelling       bool
	lastEscape       time.Time
	activity         string
	streaming        string
	userScrolled     bool
	activityBase     string
	activityLabel    string
	activitySince    time.Time
	activityTicker   bool
	lastScrollKey    string
	lastScrollAt     time.Time
	streamRendered   string
	markdown         *glamour.TermRenderer
	err              string
	system           string
	pendingMessage   []message
	mcpOpen          bool
	mcpBusy          bool
	mcpCursor        int
	mcpInfo          string
	providersOpen    bool
	providerCursor   int
	traceOpen        bool
	traceEvents      []traceEvent
	chatOffset       int
	doctorOpen       bool
	doctorBusy       bool
	doctorText       string

	providerAuthCancel context.CancelFunc
	providerAuthInfo   string
	discoveryBusy      bool
	discoveryInfo      string
	streamDisplayFull  bool

	sessionsOpen  bool
	sessionCursor int
	sessions      []memorySession
	autoSave      bool
	skillsOpen    bool
	skillCursor   int
	skills        []skills.Skill
	skillWarnings []string
}

var (
	appStyle      = lipgloss.NewStyle().Padding(1, 2)
	headStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#93c5fd"})
	userStyle     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#166534", Dark: "#86efac"})
	botStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "#0e7490", Dark: "#67e8f9"})
	errStyle      = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#fca5a5"})
	hintStyle     = lipgloss.NewStyle()
	warnStyle     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#854d0e", Dark: "#fde047"})
	boxStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
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
	authProvider := flag.String("auth", "", "log in to a provider (github-copilot)")
	logoutProvider := flag.String("logout", "", "delete a provider login (github-copilot)")
	listModels := flag.Bool("models", false, "discover and list available GitHub Copilot models")
	theme := flag.String("theme", "auto", "terminal theme: auto, light or dark")
	flag.Parse()

	if *showVersion {
		fmt.Println("hanoi " + version)
		return nil
	}
	if err := configureTerminalTheme(*theme); err != nil {
		return err
	}
	if *authProvider != "" || *logoutProvider != "" {
		return runProviderAuth(*authProvider, *logoutProvider)
	}
	if *listModels {
		cfg, err := loadConfig(*configPath, "github-copilot/gpt-4.1")
		if err != nil {
			return err
		}
		models, err := discoverCopilotModels(context.Background(), cfg)
		if err != nil {
			return err
		}
		cfg = cfg.withDiscoveredModels(models)
		for _, choice := range cfg.ModelChoices() {
			if strings.HasPrefix(choice, "github-copilot/") {
				fmt.Printf("%s\t%s\n", choice, cfg.Providers["github-copilot"].Models[strings.TrimPrefix(choice, "github-copilot/")].Name)
			}
		}
		return nil
	}

	cfg, err := loadConfig(*configPath, *model)
	if err != nil {
		return err
	}
	cfg.LogPath, cfg.TracePath = *logPath, *tracePath
	workspace, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	cfg.AgentInstructions, cfg.AgentInstructionsPath, err = loadAgentInstructions(workspace)
	if err != nil {
		return err
	}
	cfg.SystemPrompt, err = loadSystemPrompt()
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
	messages = appendMessage(messages, sessionSystem(messages, cfg.SystemPrompt), message{Role: "user", Content: prompt})

	assistantText, err := sendChat(cfg, messages, *stream)
	if err != nil {
		return err
	}
	if !*stream {
		fmt.Println(assistantText)
	}

	if *memory != "" {
		messages = append(messages, datedMessage(message{Role: "assistant", Content: assistantText}))
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
	configureTextareaColors(&ta)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = hintStyle

	m := tuiModel{
		cfg:        cfg,
		memoryName: memoryName,
		autoSave:   true,
		messages:   messages,
		textarea:   ta,
		viewport:   viewport.New(70, 10),
		spinner:    sp,
		system:     sessionSystem(messages, cfg.SystemPrompt),
	}
	for _, msg := range messages {
		if msg.Role == "user" {
			m.rememberInput(msg.Content)
		}
	}
	// Detect the terminal background before Bubble Tea takes over terminal input.
	m.resizeChat()
	m.rebuildHistory()

	// Leave mouse reporting disabled so the terminal can select and copy text,
	// including in WSL. Chat scrolling is available through keyboard shortcuts.
	p := tea.NewProgram(m, tea.WithAltScreen())
	streamProgram = p
	final, runErr := p.Run()
	if model, ok := final.(tuiModel); ok && model.chatCancel != nil {
		model.chatCancel()
	}
	if model, ok := final.(tuiModel); ok && model.providerAuthCancel != nil {
		model.providerAuthCancel()
	}
	return runErr
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case copilotLoginStartedMsg:
		if msg.err != nil {
			m.endProviderAuth(msg.err)
			return m, nil
		}
		m.providerAuthInfo = "Open " + msg.device.VerificationURI + " and enter code: " + msg.device.UserCode + "\nWaiting for GitHub authorization… Esc cancels."
		return m, finishCopilotLoginCmd(msg.ctx, msg.device)
	case copilotLoginFinishedMsg:
		m.endProviderAuth(msg.err)
		if msg.err == nil {
			return m.startModelDiscovery()
		}
		return m, nil
	case discoveredModelsMsg:
		m.discoveryBusy = false
		if msg.err != nil {
			m.discoveryInfo = "Model discovery: " + msg.err.Error() + ". Press r to retry."
		} else {
			m.cfg = m.cfg.withDiscoveredModels(msg.models)
			m.providerCursor = 0
			m.discoveryInfo = fmt.Sprintf("%d available Copilot models. Press r to refresh.", len(msg.models))
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resizeChat()
		m.markdown = nil
		m.rebuildHistory()
		return m, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.providerAuthCancel != nil {
				m.providerAuthCancel()
			}
			if m.chatCancel != nil {
				m.chatCancel()
			}
			return m, tea.Quit
		}
		if m.loading && msg.Type == tea.KeyEsc {
			now := time.Now()
			// Some terminals deliver two adjacent escapes as a single Alt+Esc.
			if msg.Alt || (!m.lastEscape.IsZero() && now.Sub(m.lastEscape) <= time.Second) {
				if m.chatCancel != nil {
					m.chatCancel()
				}
				m.cancelling = true
				m.activity = "stopping..."
				m.activityBase = m.activity
				m.lastEscape = time.Time{}
				return m, nil
			}
			m.lastEscape = now
		} else if msg.Type != tea.KeyEsc {
			m.lastEscape = time.Time{}
		}

		if m.providerAuthCancel != nil {
			if msg.String() == "esc" {
				m.providerAuthCancel()
			}
			return m, nil
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
		if m.sessionsOpen {
			return m.updateSessionMenu(msg)
		}
		if m.skillsOpen {
			return m.updateSkillMenu(msg)
		}
		if m.doctorOpen {
			if msg.String() == "esc" {
				m.doctorOpen = false
				m.resizeChat()
				m.refreshChat()
				m.viewport.SetYOffset(m.chatOffset)
				m.userScrolled = !m.viewport.AtBottom()
			} else if m.scrollChat(msg) {
				m.userScrolled = !m.viewport.AtBottom()
			}
			return m, nil
		}
		if msg.String() == "ctrl+t" || (m.traceOpen && msg.String() == "esc") {
			m.toggleTrace()
			return m, nil
		}
		if m.scrollChat(msg) {
			m.userScrolled = !m.viewport.AtBottom()
			m.refreshChat()
			return m, nil
		}
		if m.loading {
			return m, nil
		}

		switch msg.String() {
		case "up", "down", "alt+up", "alt+down":
			if m.recallInput(msg.String()) {
				m.completionPrefix = ""
				return m, nil
			}
		case "tab", "shift+tab":
			if m.completeSlashCommand(msg.String() == "shift+tab") {
				return m, nil
			}
		case "enter":
			m.completionPrefix = ""
			prompt := strings.TrimSpace(m.textarea.Value())
			if prompt == "" {
				return m, nil
			}
			m.rememberInput(prompt)
			if strings.HasPrefix(prompt, "/") {
				return m.handleSlashCommand(prompt)
			}

			if m.autoSave && m.memoryName == "" {
				name, err := newMemoryName()
				if err != nil {
					m.err = err.Error()
					return m, nil
				}
				m.memoryName = name
			}
			m.pendingMessage = appendMessage(m.messages, m.system, message{Role: "user", Content: prompt})
			if m.memoryName != "" {
				if err := saveMemory(m.memoryName, m.pendingMessage); err != nil {
					m.err = fmt.Sprintf("save session: %v", err)
					return m, nil
				}
			}
			m.messages = append([]message{}, m.pendingMessage...)
			m.textarea.SetValue("")
			m.loading = true
			m.cancelling = false
			m.lastEscape = time.Time{}
			ctx, cancel := context.WithCancel(context.Background())
			m.chatCancel = cancel
			m.activity = "waiting for model..."
			m.activityBase = m.activity
			m.activityLabel = ""
			m.activitySince = time.Now()
			m.activityTicker = false
			m.streaming = ""
			m.streamDisplayFull = false
			m.err = ""
			m.userScrolled = false
			m.rebuildHistory()
			m.viewport.GotoBottom()
			cfg := m.cfg
			cfg.Context = ctx
			return m, tea.Batch(m.spinner.Tick, streamChatCmd(cfg, m.pendingMessage))
		case "alt+enter":
			m.completionPrefix = ""
			m.textarea.InsertRune('\n')
			return m, nil
		}

		var cmd tea.Cmd
		m.completionPrefix = ""
		m.textarea, cmd = m.textarea.Update(msg)
		return m, cmd

	case chatChunkMsg:
		if !m.loading || m.cancelling {
			return m, nil
		}
		if m.streamDisplayFull {
			return m, nil
		}
		remaining := maxTUIDisplayBytes + 1 - len(m.streaming)
		if remaining > 0 {
			piece := msg.text
			if len(piece) > remaining {
				piece = piece[:remaining]
			}
			m.streaming += piece
		}
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

	case doctorResultMsg:
		m.doctorBusy = false
		m.doctorText = string(msg)
		if m.doctorOpen {
			m.refreshChat()
			m.viewport.GotoTop()
		}
		return m, nil

	case chatStatusMsg:
		if !m.loading || m.cancelling {
			return m, nil
		}
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
		m.streamRendered = m.renderStreamMarkdown(m.streaming)
		m.streamDisplayFull = len(m.streaming) >= maxTUIDisplayBytes
		m.refreshChat()
		return m, nil

	case tea.MouseMsg:
		if !m.mcpOpen && !m.providersOpen && !m.sessionsOpen && !m.skillsOpen && !m.mcpBusy {
			// Explicitly handle scroll wheel events for WSL/Touchpad compatibility.
			switch tea.MouseMsg(msg).Button {
			case tea.MouseButtonWheelUp, tea.MouseButtonWheelLeft:
				m.viewport.LineUp(3)
				m.userScrolled = !m.viewport.AtBottom()
			case tea.MouseButtonWheelDown, tea.MouseButtonWheelRight:
				m.viewport.LineDown(3)
				m.userScrolled = !m.viewport.AtBottom()
			default:
				m.viewport, _ = m.viewport.Update(msg)
			}
		}
		return m, nil

	case chatResultMsg:
		if m.chatCancel != nil {
			m.chatCancel()
			m.chatCancel = nil
		}
		cancelled := m.cancelling || errors.Is(msg.err, context.Canceled)
		m.cancelling = false
		m.lastEscape = time.Time{}
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
		if cancelled {
			m.err = "Stopped."
			msg.text = m.streaming
		} else if msg.err != nil {
			m.err = msg.err.Error()
			m.streaming = ""
			m.messages = m.messages[:max(0, len(m.messages)-1)]
			if m.memoryName != "" {
				if err := saveMemory(m.memoryName, m.messages); err != nil {
					m.err += fmt.Sprintf("; save session: %v", err)
				}
			}
			m.rebuildHistory()
			return m, nil
		}

		m.streaming = ""
		if msg.text != "" {
			m.messages = append(m.messages, datedMessage(message{Role: "assistant", Content: msg.text}))
		}
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
		if m.loading || m.mcpBusy || m.doctorBusy {
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
	if m.sessionsOpen {
		body = m.sessionView()
	} else if m.providersOpen {
		body = m.providerView()
	} else if m.skillsOpen {
		body = m.skillView()
	}

	status := hintStyle.Render("ready")
	if m.loading {
		activity := m.activity
		if activity == "" {
			activity = "waiting for model..."
		}
		status = m.spinner.View() + " " + tuiStatusText(activity, max(1, m.width-10))
	}
	if m.mcpBusy {
		status = m.spinner.View() + " updating MCP servers..."
	}
	if m.doctorBusy {
		status = m.spinner.View() + " running diagnostics..."
	}
	if m.err != "" {
		if strings.HasPrefix(m.err, "Commands:") {
			status = errStyle.Render(m.err)
		} else {
			status = errStyle.Render(tuiStatusText(m.err, max(1, m.width-6)))
		}
	}
	if !m.loading && !m.mcpOpen && !m.providersOpen && !m.sessionsOpen && !m.skillsOpen && !m.doctorOpen {
		if suggestions := m.slashSuggestions(); len(suggestions) > 0 {
			status = hintStyle.Render(tuiStatusText("Commands: "+strings.Join(suggestions, "  ")+" · Tab to complete", max(1, m.width-6)))
		}
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
			m.history = append(m.history, userStyle.Render("you")+": "+tuiDisplayText(msg.Content))
		case "assistant":
			m.history = append(m.history, botStyle.Render("assistant")+"\n"+m.renderMarkdown(msg.Content))
		case "system":
			m.history = append(m.history, hintStyle.Render("system: "+tuiDisplayText(msg.Content)))
		}
	}
	if m.streaming != "" {
		m.streamRendered = m.renderStreamMarkdown(m.streaming)
	}
	m.refreshChat()
}

func (m *tuiModel) renderMarkdown(text string) string {
	return m.renderMarkdownLimit(text, maxTUIMarkdownBytes)
}

func (m *tuiModel) renderStreamMarkdown(text string) string {
	return m.renderMarkdownLimit(text, maxTUIStreamMarkdownBytes)
}

func (m *tuiModel) renderMarkdownLimit(text string, limit int) string {
	text = tuiDisplayText(text)
	if text == "" {
		return ""
	}
	if len(text) > limit {
		return text
	}
	if m.markdown == nil {
		width := max(1, m.viewport.Width)
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStyles(terminalMarkdownStyle(lipgloss.HasDarkBackground())),
			glamour.WithColorProfile(lipgloss.ColorProfile()),
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
	case "/new":
		m.textarea.SetValue("")
		if err := m.newSession(arg); err != nil {
			m.err = err.Error()
		}
		return m, nil
	case "/sessions":
		m.textarea.SetValue("")
		if m.loading {
			m.err = "Stop the current response before switching sessions."
			return m, nil
		}
		if arg != "" {
			if err := m.switchSession(arg); err != nil {
				m.err = err.Error()
			}
			return m, nil
		}
		var err error
		m.sessions, err = listMemorySessions()
		if err != nil {
			m.err = err.Error()
			return m, nil
		}
		m.sessionsOpen, m.sessionCursor, m.err = true, 0, ""
		return m, nil
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
			name, err := newMemoryName()
			if err != nil {
				m.err = err.Error()
				return m, nil
			}
			m.memoryName = name
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
			m.textarea.SetValue("")
			return m.startModelDiscovery()
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
		return m.startModelDiscovery()
	case "/connect":
		m.textarea.SetValue("")
		if arg != "" && arg != "github-copilot" {
			m.err = "usage: /connect github-copilot"
			return m, nil
		}
		return m.startProviderAuth()
	case "/logout":
		m.textarea.SetValue("")
		if arg != "github-copilot" {
			m.err = "usage: /logout github-copilot"
			return m, nil
		}
		m.err = "GitHub Copilot logged out locally."
		if err := logoutCopilot(); err != nil {
			m.err = err.Error()
		}
		return m, nil
	case "/system":
		m.textarea.SetValue("")
		if err := saveSystemPrompt(arg); err != nil {
			m.err = fmt.Sprintf("save system prompt: %v", err)
			return m, nil
		}
		m.system = arg
		m.cfg.SystemPrompt = arg
		// Update stored context immediately, including when clearing the prompt.
		m.messages = appendMessage(m.messages, m.system)
		if arg == "" && (len(m.messages) == 0 || m.messages[0].Role != "system") {
			// An explicit empty prompt prevents another workspace default from
			// reappearing when this session is resumed later.
			m.messages = append([]message{{Role: "system", Content: ""}}, m.messages...)
		}
		m.err = "System prompt saved to .hanoi/system.md."
		if arg == "" {
			m.err = "System prompt cleared in this session and .hanoi/system.md."
		}
		if m.memoryName != "" {
			if err := saveMemory(m.memoryName, m.messages); err != nil {
				m.err = fmt.Sprintf("save session: %v", err)
			}
		}
		m.rebuildHistory()
		return m, nil
	case "/skills":
		m.textarea.SetValue("")
		return m.handleSkillsCommand(arg)
	case "/help":
		m.err = "Commands: /new [name] /sessions [name] /clear /save /model [provider/model] /providers /models /connect github-copilot /logout github-copilot /system <text> /skills [name] /mcps /trace /doctor [provider] /exit\nEsc Esc: stop model/tools (within 1s)\nScroll: Ctrl+↑↓ (3 lines) · 2x Ctrl+↑↓ (top/bottom) · Ctrl+T (trace) · ↑/↓ (input history)"
		return m, nil
	case "/doctor":
		if arg != "" && arg != "provider" {
			m.err = "usage: /doctor [provider]"
			return m, nil
		}
		if m.doctorBusy {
			m.err = "diagnostics already running"
			return m, nil
		}
		if m.traceOpen {
			m.toggleTrace()
		}
		m.chatOffset = m.viewport.YOffset
		m.doctorOpen, m.doctorBusy, m.err = true, true, ""
		m.doctorText = "Checking local runtime..."
		if arg == "provider" {
			m.doctorText += "\nTesting provider with a model request (20s timeout)..."
		}
		m.textarea.SetValue("")
		m.resizeChat()
		m.refreshChat()
		m.viewport.GotoTop()
		return m, tea.Batch(m.spinner.Tick, doctorCmd(m.cfg, arg == "provider"))
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

func appendMessage(messages []message, system string, user ...message) []message {
	filtered := make([]message, 0, len(messages)+2)
	inserted := false
	keepEmpty := false
	for _, msg := range messages {
		if msg.Role == "system" {
			keepEmpty = true
			if !inserted && system != "" {
				filtered = append(filtered, message{Role: "system", Content: system})
				inserted = true
			}
			continue
		}
		filtered = append(filtered, msg)
	}
	if !inserted && system != "" {
		filtered = append([]message{{Role: "system", Content: system}}, filtered...)
	}
	if system == "" && keepEmpty {
		filtered = append([]message{{Role: "system", Content: ""}}, filtered...)
	}
	for _, msg := range user {
		filtered = append(filtered, datedMessage(msg))
	}
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
		if cfg.context().Err() != nil {
			err = cfg.context().Err()
		}
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
	req, err := http.NewRequestWithContext(cfg.context(), http.MethodPost, cfg.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if err := authorizeProviderRequest(cfg, req, payload); err != nil {
		return nil, err
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
		Role:           msg.Role,
		Content:        msg.Content,
		ToolCalls:      msg.ToolCalls,
		ResponseOutput: msg.ResponseOutput,
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
		if len(msg.ToolCalls) > 0 && msg.Content != "" {
			// Keep text from different tool rounds apart so Markdown blocks such as
			// tables do not attach to the last line of the previous round.
			out.WriteString("\n\n")
			if onChunk != nil {
				onChunk("\n\n")
			}
		}
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
	// Some provider chat templates accept only one system message at index 0.
	// Merge project and session instructions without changing persisted history.
	input := make([]message, 0, len(messages)+1)
	var instructions []string
	if cfg.AgentInstructions != "" {
		instructions = append(instructions, cfg.AgentInstructions)
	}
	for _, msg := range messages {
		// Local archive metadata is not part of either provider API.
		msg.Timestamp = nil
		if msg.Role == "system" {
			// Empty system messages are session persistence markers only.
			if msg.Content != "" {
				instructions = append(instructions, msg.Content)
			}
			continue
		}
		input = append(input, msg)
	}
	if len(instructions) > 0 {
		input = append([]message{{Role: "system", Content: strings.Join(instructions, "\n\n")}}, input...)
	}
	messages = input
	started := time.Now()
	cfg.emitTrace(traceEvent{Type: "model.started"})
	defer func() {
		e := traceEvent{Type: "model.completed", Status: "completed", DurationMS: trace.Duration(started), ToolCalls: len(msg.ToolCalls), OutputBytes: len(msg.Content)}
		if err != nil {
			e.Status, e.Error = "error", err.Error()
		}
		cfg.emitTrace(e)
	}()
	if useResponses(cfg) {
		return requestResponse(cfg, messages, availableTools(cfg), stream, onChunk)
	}
	return requestModelAttempt(cfg, chatRequest{Model: cfg.Model, Messages: messages, Tools: availableTools(cfg), Stream: stream}, onChunk)
}

func requestModelAttempt(cfg config, request chatRequest, onChunk func(string)) (msg message, err error) {
	payload, err := json.Marshal(request)
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
		if request.ReasoningEffort == "" && len(request.Tools) > 0 && requiresToolReasoningNone(resp.StatusCode, body) {
			resp.Body.Close()
			cfg.reportStatus("provider requires reasoning_effort=none for tools; retrying...")
			if cfg.Logger != nil {
				cfg.Logger.Print("retrying tool request with reasoning_effort=none as required by provider")
			}
			request.ReasoningEffort = "none"
			return requestModelAttempt(cfg, request, onChunk)
		}
		return msg, fmt.Errorf("api error (%s): %s", resp.Status, body)
	}
	if request.Stream {
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

func requiresToolReasoningNone(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	var response struct {
		Error struct {
			Message string `json:"message"`
			Param   string `json:"param"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	return response.Error.Type == "invalid_request_error" && response.Error.Param == "reasoning_effort" &&
		strings.Contains(response.Error.Message, "Function tools with reasoning_effort are not supported") &&
		strings.Contains(response.Error.Message, "set reasoning_effort to 'none'")
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
	if err := validateMemoryName(name); err != nil {
		return nil, err
	}
	archive, err := readMemoryArchive(name)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return archive.Messages, err
}

func saveMemory(name string, messages []message) error {
	if err := validateMemoryName(name); err != nil {
		return err
	}
	path := memoryPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	archive, err := readMemoryArchive(name)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	now := time.Now()
	if archive.CreatedAt.IsZero() {
		archive.CreatedAt = now
	}
	archive.Version, archive.UpdatedAt, archive.Messages = 1, now, messages
	data, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
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
	tools = append(tools, memoryTools()...)
	return append(tools, cfg.MCP.Tools()...)
}

func executeToolCalls(calls []toolCall, cfg config) ([]message, error) {
	toolMessages := make([]message, 0, len(calls))
	for _, call := range calls {
		if err := cfg.context().Err(); err != nil {
			return nil, err
		}
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
		case "memory_search", "memory_read":
			output, err = callMemoryTool(cfg.context(), call.Function.Name, call.Function.Arguments)
		case "bash":
			ctx, cancel := context.WithTimeout(cfg.context(), 30*time.Second)
			output, err = tools.RunBashContext(ctx, call.Function.Arguments)
			cancel()
		default:
			output, err = cfg.MCP.CallContext(cfg.context(), call.Function.Name, call.Function.Arguments)
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
		if err := cfg.context().Err(); err != nil {
			return nil, err
		}
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
