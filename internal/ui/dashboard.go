package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/barronDEV/btunnel/internal/state"
)

// ─────────────────────────────────────────────────
// Dashboard Styles
// ─────────────────────────────────────────────────

var (
	dashBorderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDim).
			Padding(0, 1)

	dashHeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorAccentLight).
			Align(lipgloss.Center)

	statValueStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText).
			Align(lipgloss.Center)

	statLabelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Align(lipgloss.Center)

	statusConnectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSuccess)

	statusDisconnectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorError)

	tunnelRowStyle = lipgloss.NewStyle().
			Foreground(colorText)

	separatorStyle = lipgloss.NewStyle().
			Foreground(colorDim)
)

// ─────────────────────────────────────────────────
// Dashboard Model
// ─────────────────────────────────────────────────

// DashboardModel is a bubbletea model that shows a live dashboard
// with tunnel statistics, connection status, and bandwidth info.
type DashboardModel struct {
	connManager *state.ConnectionManager
	sigURL      string
	tunnels     []*state.TunnelInfo
	width       int
	height      int
	quitting    bool
	lastUpdate  time.Time
}

// tickMsg triggers periodic updates.
type tickMsg time.Time

// NewDashboardModel creates a new live dashboard.
func NewDashboardModel(connManager *state.ConnectionManager, sigURL string) DashboardModel {
	return DashboardModel{
		connManager: connManager,
		sigURL:      sigURL,
		width:       80,
		height:      24,
		lastUpdate:  time.Now(),
	}
}

// Init starts the dashboard.
func (m DashboardModel) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
	)
}

// tickCmd schedules a periodic update.
func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Update handles messages for the dashboard.
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		// Refresh tunnel data
		m.tunnels = m.connManager.GetAllTunnels()
		m.lastUpdate = time.Now()
		return m, tickCmd()
	}

	return m, nil
}

// View renders the dashboard.
func (m DashboardModel) View() string {
	if m.quitting {
		return ""
	}

	var sections []string

	// ── Header ──
	header := m.renderHeader()
	sections = append(sections, header)

	// ── Summary Stats ──
	summary := m.renderSummaryStats()
	sections = append(sections, summary)

	// ── Tunnel List ──
	tunnelList := m.renderTunnelList()
	sections = append(sections, tunnelList)

	// ── Footer ──
	footer := m.renderFooter()
	sections = append(sections, footer)

	return strings.Join(sections, "\n")
}

// renderHeader renders the dashboard header.
func (m DashboardModel) renderHeader() string {
	logo := logoStyle.Render("btunnel")
	subtitle := mutedStyle.Render(" status dashboard")
	header := logo + subtitle

	activeCount := 0
	for _, t := range m.tunnels {
		if t.State == state.StateConnected {
			activeCount++
		}
	}

	statusText := fmt.Sprintf("%d active tunnel(s)", activeCount)
	var statusRendered string
	if activeCount > 0 {
		statusRendered = statusConnectedStyle.Render("● " + statusText)
	} else {
		statusRendered = statusDisconnectedStyle.Render("○ " + statusText)
	}

	// Pad to align right
	padding := m.width - lipgloss.Width(header) - lipgloss.Width(statusRendered) - 4
	if padding < 1 {
		padding = 1
	}

	headerSection := "\n  " + header + strings.Repeat(" ", padding) + statusRendered + "\n" +
		"  " + separatorStyle.Render(strings.Repeat("─", min(m.width-4, 76))) + "\n"

	// If waiting for client connection, display share credentials directly in the TUI dashboard
	if activeCount == 0 && len(m.tunnels) == 1 {
		t := m.tunnels[0]
		tokenStr := lipgloss.NewStyle().Bold(true).Foreground(colorAccentLight).Render(t.Token)
		
		var modeInfo string
		if t.Mode == "web" {
			var urlStr string
			if strings.Contains(m.sigURL, "localhost") || strings.Contains(m.sigURL, "127.0.0.1") {
				urlStr = fmt.Sprintf("http://localhost:9090/share/%s", t.Token)
			} else {
				urlStr = fmt.Sprintf("https://handshake.btunnel.dpdns.org:8443/share/#%s", t.Token)
			}
			modeInfo = fmt.Sprintf("  Web Mode Access URL: %s", lipgloss.NewStyle().Foreground(colorSuccess).Render(urlStr))
		} else {
			modeInfo = fmt.Sprintf("  Mesh Mode Command:   %s", lipgloss.NewStyle().Foreground(colorSuccess).Render("btunnel join "+t.Token+" -l <local-port>"))
		}

		headerSection += fmt.Sprintf("  Share Token:         %s\n%s\n", tokenStr, modeInfo)
		headerSection += "  " + separatorStyle.Render(strings.Repeat("─", min(m.width-4, 76))) + "\n"
	}

	return headerSection
}

// renderSummaryStats renders aggregate statistics.
func (m DashboardModel) renderSummaryStats() string {
	var totalSent, totalRecv uint64
	var avgRTT float64
	var connectedCount int

	for _, t := range m.tunnels {
		totalSent += t.Stats.BytesSent
		totalRecv += t.Stats.BytesReceived
		if t.State == state.StateConnected {
			avgRTT += t.Stats.RTT
			connectedCount++
		}
	}

	if connectedCount > 0 {
		avgRTT /= float64(connectedCount)
	}

	// Build stat cards
	cards := []string{
		m.renderStatCard("UPLOAD", formatBytesRate(totalSent), "total"),
		m.renderStatCard("DOWNLOAD", formatBytesRate(totalRecv), "total"),
		m.renderStatCard("AVG RTT", fmt.Sprintf("%.1f ms", avgRTT), "latency"),
		m.renderStatCard("TUNNELS", fmt.Sprintf("%d", len(m.tunnels)), "active"),
	}

	// Arrange in a row
	return "  " + lipgloss.JoinHorizontal(lipgloss.Top, cards...) + "\n"
}

// renderStatCard renders a single statistics card.
func (m DashboardModel) renderStatCard(label, value, sublabel string) string {
	cardWidth := min((m.width-8)/4, 18)

	content := lipgloss.JoinVertical(lipgloss.Center,
		statValueStyle.Width(cardWidth-4).Render(value),
		statLabelStyle.Width(cardWidth-4).Render(label),
	)

	return dashBorderStyle.Width(cardWidth).Render(content) + " "
}

// renderTunnelList renders the list of active tunnels.
func (m DashboardModel) renderTunnelList() string {
	if len(m.tunnels) == 0 {
		return "  " + mutedStyle.Render("No active tunnels. Use 'btunnel share' to start.") + "\n"
	}

	var rows []string

	// Table header
	headerRow := fmt.Sprintf("  %-14s %-8s %-20s %-12s %-10s %-10s %s",
		"SESSION", "MODE", "TARGET", "STATE", "RTT", "↑ SENT", "↓ RECV")
	rows = append(rows, mutedStyle.Render(headerRow))
	rows = append(rows, "  "+separatorStyle.Render(strings.Repeat("─", min(m.width-4, 76))))

	for _, t := range m.tunnels {
		rows = append(rows, m.renderTunnelRow(t))
	}

	return strings.Join(rows, "\n") + "\n"
}

// renderTunnelRow renders a single tunnel info row.
func (m DashboardModel) renderTunnelRow(t *state.TunnelInfo) string {
	// Session ID (truncated)
	sessionID := t.SessionID
	if len(sessionID) > 12 {
		sessionID = sessionID[:12] + ".."
	}

	// State indicator
	var stateStr string
	switch t.State {
	case state.StateConnected:
		stateStr = statusConnectedStyle.Render("● connected")
	case state.StateConnecting:
		stateStr = warningStyle.Render("◉ connecting")
	case state.StateReconnecting:
		stateStr = warningStyle.Render("↻ reconnect")
	default:
		stateStr = statusDisconnectedStyle.Render("○ offline")
	}

	// RTT
	rttStr := "--"
	if t.Stats.RTT > 0 {
		rttStr = fmt.Sprintf("%.0fms", t.Stats.RTT)
	}

	// Target (truncated)
	target := t.Target
	if len(target) > 18 {
		target = target[:18] + ".."
	}

	row := fmt.Sprintf("  %-14s %-8s %-20s %-12s %-10s %-10s %s",
		sessionID,
		t.Mode,
		target,
		stateStr,
		rttStr,
		formatBytesCompact(t.Stats.BytesSent),
		formatBytesCompact(t.Stats.BytesReceived),
	)

	return tunnelRowStyle.Render(row)
}

// renderFooter renders the dashboard footer.
func (m DashboardModel) renderFooter() string {
	elapsed := time.Since(m.lastUpdate).Round(time.Second)
	_ = elapsed

	keys := mutedStyle.Render("  q: quit")
	updated := mutedStyle.Render(fmt.Sprintf("updated %s", m.lastUpdate.Format("15:04:05")))

	padding := m.width - lipgloss.Width(keys) - lipgloss.Width(updated) - 4
	if padding < 1 {
		padding = 1
	}

	return "\n" + keys + strings.Repeat(" ", padding) + updated + "\n"
}

// ─────────────────────────────────────────────────
// Static Renderers (non-TUI, for single output)
// ─────────────────────────────────────────────────

// RenderStatus renders a static status view (for `btunnel status` without TUI).
func RenderStatus(tunnels []*state.TunnelInfo) string {
	if len(tunnels) == 0 {
		return renderEmptyStatus()
	}

	var s string
	s += "\n"
	s += logoStyle.Render("  btunnel") + " " + mutedStyle.Render("status") + "\n"
	s += "  " + separatorStyle.Render(strings.Repeat("─", 60)) + "\n\n"

	for i, t := range tunnels {
		s += renderTunnelDetail(t)
		if i < len(tunnels)-1 {
			s += "  " + separatorStyle.Render(strings.Repeat("─", 40)) + "\n"
		}
	}

	return s
}

// renderEmptyStatus shows when no tunnels are active.
func renderEmptyStatus() string {
	var s string
	s += "\n"
	s += logoStyle.Render("  btunnel") + " " + mutedStyle.Render("status") + "\n"
	s += "  " + separatorStyle.Render(strings.Repeat("─", 60)) + "\n\n"
	s += mutedStyle.Render("  No active tunnels.") + "\n\n"
	s += "  " + lipgloss.NewStyle().Foreground(colorText).Render("Start sharing:") + "\n"
	s += "  " + lipgloss.NewStyle().Foreground(colorAccentLight).Render("  btunnel share my-network --mesh") + "\n"
	s += "  " + lipgloss.NewStyle().Foreground(colorAccentLight).Render("  btunnel share localhost:8080 --web") + "\n\n"
	return s
}

// renderTunnelDetail renders detailed info for one tunnel.
func renderTunnelDetail(t *state.TunnelInfo) string {
	var stateIcon, stateText string
	switch t.State {
	case state.StateConnected:
		stateIcon = statusConnectedStyle.Render("●")
		stateText = statusConnectedStyle.Render("Connected")
	case state.StateConnecting:
		stateIcon = warningStyle.Render("◉")
		stateText = warningStyle.Render("Connecting")
	case state.StateReconnecting:
		stateIcon = warningStyle.Render("↻")
		stateText = warningStyle.Render("Reconnecting")
	default:
		stateIcon = statusDisconnectedStyle.Render("○")
		stateText = statusDisconnectedStyle.Render("Disconnected")
	}

	var s string
	s += fmt.Sprintf("  %s %s  %s\n", stateIcon, stateText, mutedStyle.Render(t.SessionID))
	s += fmt.Sprintf("    Mode:     %s\n", lipgloss.NewStyle().Foreground(colorText).Render(t.Mode))
	s += fmt.Sprintf("    Target:   %s\n", lipgloss.NewStyle().Foreground(colorText).Render(t.Target))

	if t.State == state.StateConnected {
		s += fmt.Sprintf("    Uptime:   %s\n", lipgloss.NewStyle().Foreground(colorText).Render(formatDuration(t.Stats.Uptime)))
		s += fmt.Sprintf("    RTT:      %s\n", lipgloss.NewStyle().Foreground(colorText).Render(fmt.Sprintf("%.1f ms", t.Stats.RTT)))
		s += fmt.Sprintf("    Sent:     %s\n", lipgloss.NewStyle().Foreground(colorText).Render(formatBytesRate(t.Stats.BytesSent)))
		s += fmt.Sprintf("    Received: %s\n", lipgloss.NewStyle().Foreground(colorText).Render(formatBytesRate(t.Stats.BytesReceived)))
	}

	s += "\n"
	return s
}

// ─────────────────────────────────────────────────
// Formatting Helpers
// ─────────────────────────────────────────────────

// formatBytesRate formats bytes into human-readable format.
func formatBytesRate(bytes uint64) string {
	if bytes == 0 {
		return "0 B"
	}

	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}

	suffix := []string{"KB", "MB", "GB", "TB"}
	return fmt.Sprintf("%.1f %s", float64(bytes)/float64(div), suffix[exp])
}

// formatBytesCompact formats bytes in a compact form for table columns.
func formatBytesCompact(bytes uint64) string {
	if bytes == 0 {
		return "0B"
	}
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.0fK", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1fM", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.1fG", float64(bytes)/(1024*1024*1024))
}

// formatDuration formats a duration into a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
