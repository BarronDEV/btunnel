package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"

	btwebrtc "github.com/barronDEV/btunnel/internal/webrtc"
	"github.com/rs/zerolog/log"
)

// TCPProxy forwards TCP connections through the WebRTC DataChannel.
type TCPProxy struct {
	// dcManager handles multiplexed DataChannel communication
	dcManager *btwebrtc.DataChannelManager

	// targetAddr is the local address to proxy to (e.g., "localhost:25565")
	targetAddr string

	// listener is the local TCP listener (client side)
	listener net.Listener

	// requestIDCounter generates unique request IDs
	requestIDCounter atomic.Uint32

	// activeConns tracks active TCP connections by request ID
	activeConns sync.Map // requestID -> net.Conn

	// done signals shutdown
	done chan struct{}
}

// TCPMessage represents a TCP data packet sent over DataChannel.
type TCPMessage struct {
	Type      string `json:"type"`       // "data", "connect", "disconnect", "error"
	RequestID uint32 `json:"request_id"`
	Data      []byte `json:"data,omitempty"`
	Address   string `json:"address,omitempty"`
}

// NewTCPProxy creates a new TCP proxy.
func NewTCPProxy(dcManager *btwebrtc.DataChannelManager, targetAddr string) *TCPProxy {
	return &TCPProxy{
		dcManager:  dcManager,
		targetAddr: targetAddr,
		done:       make(chan struct{}),
	}
}

// StartHost runs the host-side proxy that forwards DataChannel packets to local TCP targets.
func (p *TCPProxy) StartHost() error {
	log.Info().Str("target", p.targetAddr).Msg("Starting TCP proxy (host mode)")

	messages := make(chan *btwebrtc.ReassembledMessage, 64)
	go p.dcManager.ProcessIncoming(messages)

	// Map to store channels for active request IDs
	var reqChans sync.Map // requestID (uint32) -> chan TCPMessage

	for {
		select {
		case <-p.done:
			// Close all request channels
			reqChans.Range(func(key, value interface{}) bool {
				close(value.(chan TCPMessage))
				return true
			})
			return nil
		case msg, ok := <-messages:
			if !ok {
				return nil
			}

			var tcpMsg TCPMessage
			if err := json.Unmarshal(msg.Data, &tcpMsg); err != nil {
				log.Error().Err(err).Msg("Failed to parse TCP message")
				continue
			}

			if tcpMsg.Type == "connect" {
				ch := make(chan TCPMessage, 64)
				reqChans.Store(tcpMsg.RequestID, ch)
				go p.handleConnectionLifecycle(tcpMsg.RequestID, ch, &reqChans)
			}

			if chVal, ok := reqChans.Load(tcpMsg.RequestID); ok {
				ch := chVal.(chan TCPMessage)
				select {
				case ch <- tcpMsg:
				default:
					log.Warn().Uint32("request_id", tcpMsg.RequestID).Msg("Host request message queue full")
				}
			}
		}
	}
}

// handleConnectionLifecycle processes packets sequentially for a single connection request.
func (p *TCPProxy) handleConnectionLifecycle(requestID uint32, ch chan TCPMessage, reqChans *sync.Map) {
	defer func() {
		reqChans.Delete(requestID)
	}()

	// Wait for the first message which must be "connect"
	msg, ok := <-ch
	if !ok || msg.Type != "connect" {
		return
	}

	conn, err := net.Dial("tcp", p.targetAddr)
	if err != nil {
		log.Error().
			Err(err).
			Uint32("request_id", requestID).
			Str("target", p.targetAddr).
			Msg("Failed to connect to target")

		// Send error back to client
		errMsg, _ := json.Marshal(TCPMessage{
			Type:      "error",
			RequestID: requestID,
			Data:      []byte(err.Error()),
		})
		p.dcManager.SendMessage(requestID, errMsg)
		return
	}
	defer conn.Close()

	p.activeConns.Store(requestID, conn)
	defer p.activeConns.Delete(requestID)

	log.Info().
		Uint32("request_id", requestID).
		Str("target", p.targetAddr).
		Msg("TCP connection established to target")

	// Start reading from target and forwarding to DataChannel
	go p.forwardFromTarget(requestID, conn)

	// Process subsequent data/disconnect messages sequentially
	for msg := range ch {
		switch msg.Type {
		case "data":
			if _, err := conn.Write(msg.Data); err != nil {
				log.Error().Err(err).Uint32("request_id", requestID).Msg("Failed to write to target")
				return
			}
		case "disconnect":
			return
		}
	}
}

// forwardFromTarget reads from the local TCP connection and sends to DataChannel.
func (p *TCPProxy) forwardFromTarget(requestID uint32, conn net.Conn) {
	buf := make([]byte, 32*1024) // 32KB read buffer

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			// Send data to client via DataChannel
			msg, _ := json.Marshal(TCPMessage{
				Type:      "data",
				RequestID: requestID,
				Data:      buf[:n],
			})

			if sendErr := p.dcManager.SendMessage(requestID, msg); sendErr != nil {
				log.Error().Err(sendErr).Uint32("request_id", requestID).Msg("Failed to send data to client")
				break
			}
		}

		if err != nil {
			if err != io.EOF {
				log.Error().Err(err).Uint32("request_id", requestID).Msg("Error reading from target")
			}

			// Notify client of disconnect
			disconnectMsg, _ := json.Marshal(TCPMessage{
				Type:      "disconnect",
				RequestID: requestID,
			})
			p.dcManager.SendMessage(requestID, disconnectMsg)

			p.activeConns.Delete(requestID)
			break
		}
	}
}

// StartClient runs the client-side proxy that listens on a local port.
func (p *TCPProxy) StartClient(listenAddr string) error {
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to start TCP listener on %s: %w", listenAddr, err)
	}

	p.listener = listener
	log.Info().Str("listen", listenAddr).Msg("TCP proxy listening (client mode)")

	// Process incoming messages from DataChannel
	messages := make(chan *btwebrtc.ReassembledMessage, 64)
	go p.dcManager.ProcessIncoming(messages)
	go p.handleClientMessages(messages)

	for {
		select {
		case <-p.done:
			return nil
		default:
		}

		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-p.done:
				return nil
			default:
				log.Error().Err(err).Msg("Failed to accept TCP connection")
				continue
			}
		}

		requestID := p.requestIDCounter.Add(1)
		p.activeConns.Store(requestID, conn)

		log.Info().
			Uint32("request_id", requestID).
			Str("remote", conn.RemoteAddr().String()).
			Msg("Accepted local TCP connection")

		// Send connect message to host
		connectMsg, _ := json.Marshal(TCPMessage{
			Type:      "connect",
			RequestID: requestID,
			Address:   p.targetAddr,
		})
		p.dcManager.SendMessage(requestID, connectMsg)

		// Start forwarding from local conn to DataChannel
		go p.forwardFromLocal(requestID, conn)
	}
}

// handleClientMessages processes incoming messages on the client side.
func (p *TCPProxy) handleClientMessages(messages <-chan *btwebrtc.ReassembledMessage) {
	for msg := range messages {
		var tcpMsg TCPMessage
		if err := json.Unmarshal(msg.Data, &tcpMsg); err != nil {
			continue
		}

		switch tcpMsg.Type {
		case "data":
			p.handleData(tcpMsg.RequestID, tcpMsg.Data)
		case "disconnect":
			p.handleDisconnect(tcpMsg.RequestID)
		case "error":
			log.Error().
				Uint32("request_id", tcpMsg.RequestID).
				Str("error", string(tcpMsg.Data)).
				Msg("Remote connection error")
			p.handleDisconnect(tcpMsg.RequestID)
		}
	}
}

// handleData forwards data from DataChannel to the active TCP connection.
func (p *TCPProxy) handleData(requestID uint32, data []byte) {
	connRaw, ok := p.activeConns.Load(requestID)
	if !ok {
		log.Warn().Uint32("request_id", requestID).Msg("No active connection for data")
		return
	}

	conn := connRaw.(net.Conn)
	if _, err := conn.Write(data); err != nil {
		log.Error().Err(err).Uint32("request_id", requestID).Msg("Failed to write to connection")
		p.handleDisconnect(requestID)
	}
}

// handleDisconnect closes a TCP connection.
func (p *TCPProxy) handleDisconnect(requestID uint32) {
	connRaw, ok := p.activeConns.LoadAndDelete(requestID)
	if !ok {
		return
	}

	conn := connRaw.(net.Conn)
	conn.Close()

	log.Debug().Uint32("request_id", requestID).Msg("TCP connection closed")
}

// forwardFromLocal reads from a local TCP connection and sends to DataChannel.
func (p *TCPProxy) forwardFromLocal(requestID uint32, conn net.Conn) {
	buf := make([]byte, 32*1024)

	for {
		n, err := conn.Read(buf)
		if n > 0 {
			msg, _ := json.Marshal(TCPMessage{
				Type:      "data",
				RequestID: requestID,
				Data:      buf[:n],
			})
			p.dcManager.SendMessage(requestID, msg)
		}

		if err != nil {
			if err != io.EOF {
				log.Error().Err(err).Uint32("request_id", requestID).Msg("Error reading from local")
			}

			disconnectMsg, _ := json.Marshal(TCPMessage{
				Type:      "disconnect",
				RequestID: requestID,
			})
			p.dcManager.SendMessage(requestID, disconnectMsg)

			p.activeConns.Delete(requestID)
			break
		}
	}
}

// Stop gracefully stops the TCP proxy.
func (p *TCPProxy) Stop() {
	close(p.done)

	if p.listener != nil {
		p.listener.Close()
	}

	// Close all active connections
	p.activeConns.Range(func(key, value interface{}) bool {
		conn := value.(net.Conn)
		conn.Close()
		p.activeConns.Delete(key)
		return true
	})

	log.Info().Msg("TCP proxy stopped")
}
