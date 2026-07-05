package state

import (
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ConnectionState represents the current state of a tunnel connection.
type ConnectionState int

const (
	// StateDisconnected means no active connection.
	StateDisconnected ConnectionState = iota

	// StateConnecting means connection is being established.
	StateConnecting

	// StateConnected means P2P connection is active.
	StateConnected

	// StateReconnecting means connection was lost and reconnection is in progress.
	StateReconnecting
)

// String returns a human-readable state name.
func (s ConnectionState) String() string {
	switch s {
	case StateDisconnected:
		return "disconnected"
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateReconnecting:
		return "reconnecting"
	default:
		return "unknown"
	}
}

// TunnelStats holds real-time statistics for a tunnel.
type TunnelStats struct {
	// RTT is the round-trip time in milliseconds.
	RTT float64

	// BytesSent is the total bytes sent through the tunnel.
	BytesSent uint64

	// BytesReceived is the total bytes received through the tunnel.
	BytesReceived uint64

	// PacketLoss is the packet loss percentage (0.0 - 1.0).
	PacketLoss float64

	// Uptime is how long the tunnel has been connected.
	Uptime time.Duration

	// CurrentBandwidth is the current bandwidth usage in bytes/sec.
	CurrentBandwidthUp   float64
	CurrentBandwidthDown float64
}

// TunnelInfo holds all information about an active tunnel.
type TunnelInfo struct {
	// SessionID is the signaling session identifier.
	SessionID string

	// Token is the connection token.
	Token string

	// Mode is "mesh" or "web".
	Mode string

	// Target is the Docker network or port being shared.
	Target string

	// State is the current connection state.
	State ConnectionState

	// Stats holds real-time statistics.
	Stats TunnelStats

	// ConnectedAt is when the P2P connection was established.
	ConnectedAt time.Time

	// RemoteAddr is the remote peer's address (if known).
	RemoteAddr string
}

// ConnectionManager tracks all active tunnel connections and their states.
type ConnectionManager struct {
	mu      sync.RWMutex
	tunnels map[string]*TunnelInfo // keyed by session ID

	// State change callback
	onStateChange func(sessionID string, oldState, newState ConnectionState)
}

// NewConnectionManager creates a new connection manager.
func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{
		tunnels: make(map[string]*TunnelInfo),
	}
}

// OnStateChange registers a callback for state changes.
func (m *ConnectionManager) OnStateChange(cb func(sessionID string, oldState, newState ConnectionState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStateChange = cb
}

// AddTunnel registers a new tunnel.
func (m *ConnectionManager) AddTunnel(info *TunnelInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.tunnels[info.SessionID] = info

	log.Info().
		Str("session_id", info.SessionID).
		Str("mode", info.Mode).
		Str("target", info.Target).
		Msg("Tunnel registered")
}

// SetState updates the connection state of a tunnel.
func (m *ConnectionManager) SetState(sessionID string, state ConnectionState) {
	m.mu.Lock()
	defer m.mu.Unlock()

	tunnel, ok := m.tunnels[sessionID]
	if !ok {
		return
	}

	oldState := tunnel.State
	tunnel.State = state

	if state == StateConnected {
		tunnel.ConnectedAt = time.Now()
	}

	log.Info().
		Str("session_id", sessionID).
		Str("old_state", oldState.String()).
		Str("new_state", state.String()).
		Msg("Tunnel state changed")

	if m.onStateChange != nil {
		go m.onStateChange(sessionID, oldState, state)
	}
}

// UpdateStats updates the statistics for a tunnel.
func (m *ConnectionManager) UpdateStats(sessionID string, stats TunnelStats) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if tunnel, ok := m.tunnels[sessionID]; ok {
		if tunnel.State == StateConnected {
			stats.Uptime = time.Since(tunnel.ConnectedAt)
		}
		tunnel.Stats = stats
	}
}

// GetTunnel returns information about a specific tunnel.
func (m *ConnectionManager) GetTunnel(sessionID string) *TunnelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if tunnel, ok := m.tunnels[sessionID]; ok {
		// Return a copy
		copy := *tunnel
		if copy.State == StateConnected {
			copy.Stats.Uptime = time.Since(copy.ConnectedAt)
		}
		return &copy
	}
	return nil
}

// GetAllTunnels returns all active tunnels.
func (m *ConnectionManager) GetAllTunnels() []*TunnelInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*TunnelInfo, 0, len(m.tunnels))
	for _, tunnel := range m.tunnels {
		copy := *tunnel
		if copy.State == StateConnected {
			copy.Stats.Uptime = time.Since(copy.ConnectedAt)
		}
		result = append(result, &copy)
	}
	return result
}

// RemoveTunnel removes a tunnel from tracking.
func (m *ConnectionManager) RemoveTunnel(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.tunnels, sessionID)
	log.Info().Str("session_id", sessionID).Msg("Tunnel removed")
}

// ActiveCount returns the number of active tunnels.
func (m *ConnectionManager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tunnels)
}
