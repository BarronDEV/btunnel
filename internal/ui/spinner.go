package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─────────────────────────────────────────────────
// Styles
// ─────────────────────────────────────────────────

var (
	// Brand colors
	colorAccent     = lipgloss.Color("#6c5ce7")
	colorAccentLight = lipgloss.Color("#a29bfe")
	colorSuccess    = lipgloss.Color("#00b894")
	colorWarning    = lipgloss.Color("#fdcb6e")
	colorError      = lipgloss.Color("#e17055")
	colorMuted      = lipgloss.Color("#636e72")
	colorDim        = lipgloss.Color("#2d3436")
	colorText       = lipgloss.Color("#dfe6e9")

	// Logo style
	logoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccentLight)

	// Title style
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText).
			MarginBottom(1)

	// Spinner label style
	spinnerLabelStyle = lipgloss.NewStyle().
				Foreground(colorText)

	// Success style
	successStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorSuccess)

	// Error style
	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorError)

	// Warning style
	warningStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWarning)

	// Muted style
	mutedStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// Step styles
	stepDoneStyle = lipgloss.NewStyle().
			Foreground(colorSuccess)

	stepActiveStyle = lipgloss.NewStyle().
			Foreground(colorWarning)

	stepPendingStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	// Box style for cards
	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDim).
			Padding(0, 1)
)

// ─────────────────────────────────────────────────
// Connection Spinner Model
// ─────────────────────────────────────────────────

// SpinnerStep represents one step in the connection process.
type SpinnerStep struct {
	Label    string
	Status   StepStatus
}

// StepStatus represents the current status of a step.
type StepStatus int

const (
	StepPending  StepStatus = iota
	StepActive
	StepDone
	StepError
)

// SpinnerModel is a bubbletea model that shows a connection spinner
// with multiple steps (signaling, WebRTC, DataChannel, etc.)
type SpinnerModel struct {
	spinner  spinner.Model
	steps    []SpinnerStep
	message  string
	done     bool
	err      error
	quitting bool
}

// SpinnerConfig holds configuration for the spinner.
type SpinnerConfig struct {
	Steps   []string
	Title   string
}

// NewSpinnerModel creates a new connection spinner.
func NewSpinnerModel(config SpinnerConfig) SpinnerModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(colorAccent)

	steps := make([]SpinnerStep, len(config.Steps))
	for i, label := range config.Steps {
		steps[i] = SpinnerStep{Label: label, Status: StepPending}
	}

	// First step is active
	if len(steps) > 0 {
		steps[0].Status = StepActive
	}

	title := config.Title
	if title == "" {
		title = "Establishing connection..."
	}

	return SpinnerModel{
		spinner: s,
		steps:   steps,
		message: title,
	}
}

// DefaultConnectionSpinner creates a spinner with default connection steps.
func DefaultConnectionSpinner() SpinnerModel {
	return NewSpinnerModel(SpinnerConfig{
		Title: "Establishing P2P tunnel...",
		Steps: []string{
			"Connecting to signaling server",
			"Exchanging SDP offer/answer",
			"UDP hole punching (ICE)",
			"Opening encrypted data channels",
			"Tunnel active",
		},
	})
}

// ─── Messages ───────────────────────────────────

// StepCompleteMsg signals that the current step is done.
type StepCompleteMsg struct{}

// StepErrorMsg signals that the current step failed.
type StepErrorMsg struct{ Err error }

// AllDoneMsg signals that all steps are complete.
type AllDoneMsg struct{}

// ─── Tea Interface ──────────────────────────────

// Init initializes the spinner model.
func (m SpinnerModel) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update handles messages for the spinner model.
func (m SpinnerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case StepCompleteMsg:
		return m.advanceStep()

	case StepErrorMsg:
		return m.failCurrentStep(msg.Err)

	case AllDoneMsg:
		m.done = true
		return m, tea.Quit
	}

	return m, nil
}

// View renders the spinner model.
func (m SpinnerModel) View() string {
	if m.quitting {
		return mutedStyle.Render("Cancelled.") + "\n"
	}

	var s string

	// Logo
	s += logoStyle.Render("  btunnel") + "\n"
	s += mutedStyle.Render("  Secure P2P Tunneling Engine") + "\n\n"

	// Steps
	for _, step := range m.steps {
		var icon string
		var style lipgloss.Style

		switch step.Status {
		case StepDone:
			icon = "  ✓"
			style = stepDoneStyle
		case StepActive:
			icon = "  " + m.spinner.View()
			style = stepActiveStyle
		case StepError:
			icon = "  ✗"
			style = errorStyle
		default:
			icon = "  ○"
			style = stepPendingStyle
		}

		s += icon + " " + style.Render(step.Label) + "\n"
	}

	// Error message
	if m.err != nil {
		s += "\n" + errorStyle.Render("  Error: "+m.err.Error()) + "\n"
	}

	// Done message
	if m.done {
		s += "\n" + successStyle.Render("  ✓ Tunnel established successfully!") + "\n"
	}

	// Footer hint
	if !m.done && m.err == nil {
		s += "\n" + mutedStyle.Render("  Press q to cancel") + "\n"
	}

	return s
}

// advanceStep marks current step as done and activates the next.
func (m SpinnerModel) advanceStep() (tea.Model, tea.Cmd) {
	for i := range m.steps {
		if m.steps[i].Status == StepActive {
			m.steps[i].Status = StepDone
			if i+1 < len(m.steps) {
				m.steps[i+1].Status = StepActive
			}
			break
		}
	}
	return m, m.spinner.Tick
}

// failCurrentStep marks the current step as failed.
func (m SpinnerModel) failCurrentStep(err error) (tea.Model, tea.Cmd) {
	m.err = err
	for i := range m.steps {
		if m.steps[i].Status == StepActive {
			m.steps[i].Status = StepError
			break
		}
	}
	return m, tea.Quit
}

// ─────────────────────────────────────────────────
// Simple Spinner (for quick operations)
// ─────────────────────────────────────────────────

// SimpleSpinner shows a single-line spinner with a message.
type SimpleSpinner struct {
	spinner spinner.Model
	message string
	done    bool
	result  string
}

// NewSimpleSpinner creates a simple one-line spinner.
func NewSimpleSpinner(message string) SimpleSpinner {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = lipgloss.NewStyle().Foreground(colorAccent)

	return SimpleSpinner{
		spinner: s,
		message: message,
	}
}

// Init starts the spinner.
func (m SimpleSpinner) Init() tea.Cmd {
	return m.spinner.Tick
}

// Update handles messages.
func (m SimpleSpinner) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case AllDoneMsg:
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

// View renders the spinner.
func (m SimpleSpinner) View() string {
	if m.done {
		return successStyle.Render("✓") + " " + m.message + " " + mutedStyle.Render(m.result) + "\n"
	}
	return m.spinner.View() + " " + m.message + "\n"
}

// ─────────────────────────────────────────────────
// Token Display
// ─────────────────────────────────────────────────

// RenderToken formats a share token for terminal display.
func RenderToken(token string, mode string, target string) string {
	tokenStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(colorAccentLight).
		Background(lipgloss.Color("#1a1a2e")).
		Padding(0, 1)

	modeLabel := "web"
	modeColor := colorSuccess
	if mode == "mesh" {
		modeLabel = "mesh"
		modeColor = colorAccent
	}

	modeBadge := lipgloss.NewStyle().
		Bold(true).
		Foreground(modeColor).
		Render("[" + modeLabel + "]")

	var s string
	s += "\n"
	s += successStyle.Render("  ✓ Share active!") + "\n\n"
	s += "  Token:  " + tokenStyle.Render(token) + "\n"
	s += "  Mode:   " + modeBadge + "\n"
	s += "  Target: " + lipgloss.NewStyle().Foreground(colorText).Render(target) + "\n\n"

	if mode == "mesh" {
		s += mutedStyle.Render("  Client command:") + "\n"
		s += lipgloss.NewStyle().
			Foreground(colorText).
			Background(lipgloss.Color("#1a1a2e")).
			Padding(0, 1).
			Render(fmt.Sprintf("btunnel join %s", token)) + "\n"
	} else {
		s += mutedStyle.Render("  Share this link:") + "\n"
		s += lipgloss.NewStyle().
			Foreground(colorAccentLight).
			Underline(true).
			Render(fmt.Sprintf("  https://btunnel.live/share/%s", token)) + "\n"
	}

	s += "\n" + mutedStyle.Render("  Waiting for peer... (Ctrl+C to stop)") + "\n"
	return s
}

// RenderShareCreated outputs a quick summary after share is created (non-TUI mode).
func RenderShareCreated(token string, mode string, target string, signalingURL string) string {
	_ = time.Now() // ensure time is imported for potential future use
	return RenderToken(token, mode, target)
}
