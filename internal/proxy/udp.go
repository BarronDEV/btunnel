package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

	btwebrtc "github.com/barronDEV/btunnel/internal/webrtc"
	"github.com/rs/zerolog/log"
)

// UDPProxy forwards UDP datagrams through the WebRTC DataChannel.
// Used for gaming (Minecraft, etc.) and other UDP-based protocols.
type UDPProxy struct {
	dcManager  *btwebrtc.DataChannelManager
	targetAddr string

	// Client-side UDP listener
	listener *net.UDPConn

	// Request ID counter
	requestIDCounter atomic.Uint32

	// Active UDP "sessions" by client address
	sessions sync.Map // clientAddr string -> *udpSession

	done chan struct{}
}

// udpSession tracks a UDP "connection" (source address mapping).
type udpSession struct {
	requestID  uint32
	clientAddr *net.UDPAddr
	targetConn *net.UDPConn
}

// UDPMessage represents a UDP datagram sent over DataChannel.
type UDPMessage struct {
	Type      string `json:"type"`       // "data", "error"
	RequestID uint32 `json:"request_id"`
	Data      []byte `json:"data,omitempty"`
	Address   string `json:"address,omitempty"`
}

// NewUDPProxy creates a new UDP proxy.
func NewUDPProxy(dcManager *btwebrtc.DataChannelManager, targetAddr string) *UDPProxy {
	return &UDPProxy{
		dcManager:  dcManager,
		targetAddr: targetAddr,
		done:       make(chan struct{}),
	}
}

// StartHost runs the host-side UDP proxy.
func (p *UDPProxy) StartHost() error {
	log.Info().Str("target", p.targetAddr).Msg("Starting UDP proxy (host mode)")

	targetUDPAddr, err := net.ResolveUDPAddr("udp", p.targetAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve target address: %w", err)
	}

	messages := make(chan *btwebrtc.ReassembledMessage, 128)
	go p.dcManager.ProcessIncoming(messages)

	for {
		select {
		case <-p.done:
			return nil
		case msg, ok := <-messages:
			if !ok {
				return nil
			}

			var udpMsg UDPMessage
			if err := json.Unmarshal(msg.Data, &udpMsg); err != nil {
				continue
			}

			if udpMsg.Type == "data" {
				go p.forwardToTarget(udpMsg.RequestID, udpMsg.Data, targetUDPAddr)
			}
		}
	}
}

// forwardToTarget sends a UDP datagram to the local target service.
func (p *UDPProxy) forwardToTarget(requestID uint32, data []byte, targetAddr *net.UDPAddr) {
	// Get or create target connection for this request
	var targetConn *net.UDPConn

	sessRaw, exists := p.sessions.Load(requestID)
	if exists {
		targetConn = sessRaw.(*udpSession).targetConn
	} else {
		conn, err := net.DialUDP("udp", nil, targetAddr)
		if err != nil {
			log.Error().Err(err).Uint32("request_id", requestID).Msg("Failed to dial UDP target")
			return
		}

		sess := &udpSession{
			requestID:  requestID,
			targetConn: conn,
		}
		p.sessions.Store(requestID, sess)
		targetConn = conn

		// Start reading responses from target
		go p.readFromTarget(requestID, conn)
	}

	if _, err := targetConn.Write(data); err != nil {
		log.Error().Err(err).Uint32("request_id", requestID).Msg("Failed to write to UDP target")
	}
}

// readFromTarget reads UDP responses from the target and sends back via DataChannel.
func (p *UDPProxy) readFromTarget(requestID uint32, conn *net.UDPConn) {
	buf := make([]byte, 65535) // Max UDP datagram size

	for {
		select {
		case <-p.done:
			return
		default:
		}

		n, err := conn.Read(buf)
		if err != nil {
			log.Debug().Err(err).Uint32("request_id", requestID).Msg("UDP read error")
			return
		}

		msg, _ := json.Marshal(UDPMessage{
			Type:      "data",
			RequestID: requestID,
			Data:      buf[:n],
		})

		p.dcManager.SendMessage(requestID, msg)
	}
}

// StartClient runs the client-side UDP proxy that listens on a local port.
func (p *UDPProxy) StartClient(listenAddr string) error {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to resolve listen address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}
	p.listener = conn

	log.Info().Str("listen", listenAddr).Msg("UDP proxy listening (client mode)")

	// Process incoming messages from DataChannel
	messages := make(chan *btwebrtc.ReassembledMessage, 128)
	go p.dcManager.ProcessIncoming(messages)
	go p.handleClientMessages(messages)

	// Read from local UDP socket
	buf := make([]byte, 65535)
	for {
		select {
		case <-p.done:
			return nil
		default:
		}

		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-p.done:
				return nil
			default:
				log.Error().Err(err).Msg("Failed to read from local UDP")
				continue
			}
		}

		// Get or create session for this client
		addrKey := remoteAddr.String()
		var requestID uint32

		sessRaw, exists := p.sessions.Load(addrKey)
		if exists {
			requestID = sessRaw.(*udpSession).requestID
		} else {
			requestID = p.requestIDCounter.Add(1)
			p.sessions.Store(addrKey, &udpSession{
				requestID:  requestID,
				clientAddr: remoteAddr,
			})
		}

		// Forward to host via DataChannel
		msg, _ := json.Marshal(UDPMessage{
			Type:      "data",
			RequestID: requestID,
			Data:      buf[:n],
		})
		p.dcManager.SendMessage(requestID, msg)
	}
}

// handleClientMessages processes incoming DataChannel messages on the client side.
func (p *UDPProxy) handleClientMessages(messages <-chan *btwebrtc.ReassembledMessage) {
	for msg := range messages {
		var udpMsg UDPMessage
		if err := json.Unmarshal(msg.Data, &udpMsg); err != nil {
			continue
		}

		if udpMsg.Type == "data" {
			// Find the session by request ID and send to the local client
			p.sessions.Range(func(key, value interface{}) bool {
				sess := value.(*udpSession)
				if sess.requestID == udpMsg.RequestID && sess.clientAddr != nil {
					p.listener.WriteToUDP(udpMsg.Data, sess.clientAddr)
					return false // stop iteration
				}
				return true
			})
		}
	}
}

// Stop gracefully stops the UDP proxy.
func (p *UDPProxy) Stop() {
	close(p.done)

	if p.listener != nil {
		p.listener.Close()
	}

	// Close all target connections
	p.sessions.Range(func(key, value interface{}) bool {
		sess := value.(*udpSession)
		if sess.targetConn != nil {
			sess.targetConn.Close()
		}
		p.sessions.Delete(key)
		return true
	})

	log.Info().Msg("UDP proxy stopped")
}
