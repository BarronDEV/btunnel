package webrtc

import (
	"fmt"
	"sync"

	"github.com/pion/webrtc/v4"
	"github.com/rs/zerolog/log"
)

// PeerConfig holds configuration for creating a new peer connection.
type PeerConfig struct {
	// ICEServers is the list of STUN/TURN servers to use.
	ICEServers []webrtc.ICEServer

	// IsHost indicates if this peer is the host (offerer) or client (answerer).
	IsHost bool

	// OnICECandidate is called when a new ICE candidate is gathered.
	OnICECandidate func(candidate *webrtc.ICECandidate)

	// OnConnectionStateChange is called when the connection state changes.
	OnConnectionStateChange func(state webrtc.PeerConnectionState)

	// OnDataChannel is called when a new DataChannel is opened (client side).
	OnDataChannel func(dc *webrtc.DataChannel)
}

// Peer wraps a pion WebRTC PeerConnection with btunnel-specific logic.
type Peer struct {
	mu   sync.Mutex
	conn *webrtc.PeerConnection

	// DataChannels for multiplexed communication
	dataChannels []*webrtc.DataChannel

	// Channel for incoming messages from all data channels
	incomingMessages chan []byte

	// Configuration
	config PeerConfig

	// State
	isConnected bool
}

// NewPeer creates a new WebRTC peer connection.
func NewPeer(config PeerConfig) (*Peer, error) {
	// Build ICE server configuration
	iceServers := config.ICEServers

	// Default STUN servers if none provided
	if len(iceServers) == 0 {
		iceServers = []webrtc.ICEServer{
			{URLs: []string{"stun:stun.l.google.com:19302"}},
			{URLs: []string{"stun:stun1.l.google.com:19302"}},
		}
	}

	// Create PeerConnection configuration
	pcConfig := webrtc.Configuration{
		ICEServers: iceServers,
	}

	// Create the PeerConnection
	pc, err := webrtc.NewPeerConnection(pcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create peer connection: %w", err)
	}

	peer := &Peer{
		conn:             pc,
		config:           config,
		incomingMessages: make(chan []byte, 256),
		dataChannels:     make([]*webrtc.DataChannel, 0),
	}

	// Register ICE candidate callback
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil {
			return // ICE gathering complete
		}
		log.Debug().
			Str("candidate", candidate.String()).
			Msg("New ICE candidate gathered")

		if config.OnICECandidate != nil {
			config.OnICECandidate(candidate)
		}
	})

	// Register connection state callback
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Info().
			Str("state", state.String()).
			Msg("Peer connection state changed")

		peer.mu.Lock()
		peer.isConnected = (state == webrtc.PeerConnectionStateConnected)
		peer.mu.Unlock()

		if config.OnConnectionStateChange != nil {
			config.OnConnectionStateChange(state)
		}
	})

	// Register ICE connection state callback for debugging
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Debug().
			Str("state", state.String()).
			Msg("ICE connection state changed")
	})

	// If client, register data channel handler
	if !config.IsHost {
		pc.OnDataChannel(func(dc *webrtc.DataChannel) {
			log.Info().
				Str("label", dc.Label()).
				Uint16("id", *dc.ID()).
				Msg("Data channel received")

			peer.registerDataChannel(dc)

			if config.OnDataChannel != nil {
				config.OnDataChannel(dc)
			}
		})
	}

	return peer, nil
}

// CreateOffer creates an SDP offer (host side).
func (p *Peer) CreateOffer() (webrtc.SessionDescription, error) {
	offer, err := p.conn.CreateOffer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("failed to create offer: %w", err)
	}

	if err := p.conn.SetLocalDescription(offer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("failed to set local description: %w", err)
	}

	log.Debug().Msg("SDP offer created and set as local description")
	return offer, nil
}

// CreateAnswer creates an SDP answer (client side).
func (p *Peer) CreateAnswer() (webrtc.SessionDescription, error) {
	answer, err := p.conn.CreateAnswer(nil)
	if err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("failed to create answer: %w", err)
	}

	if err := p.conn.SetLocalDescription(answer); err != nil {
		return webrtc.SessionDescription{}, fmt.Errorf("failed to set local description: %w", err)
	}

	log.Debug().Msg("SDP answer created and set as local description")
	return answer, nil
}

// SetRemoteDescription sets the remote peer's SDP.
func (p *Peer) SetRemoteDescription(sdp webrtc.SessionDescription) error {
	if err := p.conn.SetRemoteDescription(sdp); err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}
	log.Debug().Str("type", sdp.Type.String()).Msg("Remote description set")
	return nil
}

// AddICECandidate adds a remote ICE candidate.
func (p *Peer) AddICECandidate(candidate webrtc.ICECandidateInit) error {
	if err := p.conn.AddICECandidate(candidate); err != nil {
		return fmt.Errorf("failed to add ICE candidate: %w", err)
	}
	log.Debug().Msg("Remote ICE candidate added")
	return nil
}

// CreateDataChannels creates multiple data channels for multiplexed communication.
// Using multiple channels prevents head-of-line blocking.
func (p *Peer) CreateDataChannels(count int) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for i := 0; i < count; i++ {
		label := fmt.Sprintf("btunnel-dc-%d", i)
		ordered := false // Unordered for lower latency
		dc, err := p.conn.CreateDataChannel(label, &webrtc.DataChannelInit{
			Ordered: &ordered,
		})
		if err != nil {
			return fmt.Errorf("failed to create data channel %d: %w", i, err)
		}

		p.registerDataChannelLocked(dc)
		log.Debug().Str("label", label).Msg("Data channel created")
	}

	return nil
}

// registerDataChannel sets up handlers for a data channel with locking.
func (p *Peer) registerDataChannel(dc *webrtc.DataChannel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registerDataChannelLocked(dc)
}

func (p *Peer) registerDataChannelLocked(dc *webrtc.DataChannel) {
	p.dataChannels = append(p.dataChannels, dc)

	dc.OnOpen(func() {
		log.Info().
			Str("label", dc.Label()).
			Msg("Data channel opened")
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		// Send to incoming messages channel (non-blocking)
		select {
		case p.incomingMessages <- msg.Data:
		default:
			log.Warn().Msg("Incoming message buffer full, dropping message")
		}
	})

	dc.OnClose(func() {
		log.Info().
			Str("label", dc.Label()).
			Msg("Data channel closed")
	})

	dc.OnError(func(err error) {
		log.Error().
			Err(err).
			Str("label", dc.Label()).
			Msg("Data channel error")
	})
}

// Send sends data over a data channel using round-robin distribution.
func (p *Peer) Send(data []byte) error {
	p.mu.Lock()
	channels := p.dataChannels
	p.mu.Unlock()

	if len(channels) == 0 {
		return fmt.Errorf("no data channels available")
	}

	// Round-robin channel selection
	// Simple approach: use first available open channel
	for _, dc := range channels {
		if dc.ReadyState() == webrtc.DataChannelStateOpen {
			return dc.Send(data)
		}
	}

	return fmt.Errorf("no open data channels available")
}

// Receive returns the incoming messages channel.
func (p *Peer) Receive() <-chan []byte {
	return p.incomingMessages
}

// IsConnected returns whether the peer is currently connected.
func (p *Peer) IsConnected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isConnected
}

// GetDataChannels returns the active data channels.
func (p *Peer) GetDataChannels() []*webrtc.DataChannel {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dataChannels
}

// GetStats returns WebRTC statistics for monitoring.
func (p *Peer) GetStats() webrtc.StatsReport {
	return p.conn.GetStats()
}

// Close closes the peer connection and all data channels.
func (p *Peer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, dc := range p.dataChannels {
		dc.Close()
	}

	close(p.incomingMessages)

	if err := p.conn.Close(); err != nil {
		return fmt.Errorf("failed to close peer connection: %w", err)
	}

	log.Info().Msg("Peer connection closed")
	return nil
}
