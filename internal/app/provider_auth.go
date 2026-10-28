package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func runProviderAuth(login, logout string) error {
	if (login != "" && login != "github-copilot") || (logout != "" && logout != "github-copilot") || (login != "" && logout != "") {
		return fmt.Errorf("use -auth github-copilot or -logout github-copilot")
	}
	if logout != "" {
		if err := logoutCopilot(); err != nil {
			return err
		}
		fmt.Println("GitHub Copilot logged out locally.")
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, 15*time.Minute)
	defer timeout()
	device, err := startCopilotLogin(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("Open %s and enter code: %s\nWaiting for GitHub authorization…\n", device.VerificationURI, device.UserCode)
	_ = openOAuthBrowser(device.VerificationURI)
	if err := finishCopilotLogin(ctx, device); err != nil {
		return err
	}
	fmt.Println("GitHub Copilot connected.")
	return nil
}

type copilotLoginStartedMsg struct {
	ctx    context.Context
	device copilotDevice
	err    error
}
type copilotLoginFinishedMsg struct{ err error }

func (m tuiModel) startProviderAuth() (tea.Model, tea.Cmd) {
	if m.providerAuthCancel != nil {
		return m, nil
	}
	m.cfg.Config = m.cfg.Config.WithCopilot()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	m.providerAuthCancel = cancel
	m.providersOpen = true
	m.providerAuthInfo = "Requesting GitHub device code… Esc cancels."
	m.err = ""
	return m, func() tea.Msg {
		device, err := startCopilotLogin(ctx)
		return copilotLoginStartedMsg{ctx: ctx, device: device, err: err}
	}
}

func finishCopilotLoginCmd(ctx context.Context, device copilotDevice) tea.Cmd {
	return func() tea.Msg {
		if ctx.Err() != nil {
			return copilotLoginFinishedMsg{err: ctx.Err()}
		}
		_ = openOAuthBrowser(device.VerificationURI)
		return copilotLoginFinishedMsg{err: finishCopilotLogin(ctx, device)}
	}
}

func (m *tuiModel) endProviderAuth(err error) {
	if m.providerAuthCancel != nil {
		m.providerAuthCancel()
	}
	m.providerAuthCancel = nil
	m.providerAuthInfo = "GitHub Copilot connected. Select a Copilot model to use it."
	if err != nil {
		m.providerAuthInfo = "GitHub Copilot login: " + err.Error()
	}
}
