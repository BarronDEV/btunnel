package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
)

const (
	// DefaultSignalingURL is the default public signaling server URL.
	DefaultSignalingURL = "wss://handshake.btunnel.dpdns.org:8443/ws"

	// reconnectBaseDelay is the initial delay for exponential backoff.
	reconnectBaseDelay = 1 * time.Second

	// reconnectMaxDelay is the maximum delay between reconnection attempts.
	reconnectMaxDelay = 30 * time.Second

	// writeTimeout is the maximum time to wait for a write to complete.
	writeTimeout = 10 * time.Second
)

// MessageType represents the type of signaling message.
type MessageType string

const (
	MsgTypeOffer          MessageType = "offer"
	MsgTypeAnswer         MessageType = "answer"
	MsgTypeICECandidate   MessageType = "ice-candidate"
	MsgTypeJoin           MessageType = "join"
	MsgTypeCreate         MessageType = "create"
	MsgTypeSessionCreated MessageType = "session-created"
	MsgTypeError          MessageType = "error"
	MsgTypePeerJoined     MessageType = "peer-joined"
	MsgTypePeerLeft       MessageType = "peer-left"
)

// SignalMessage is the JSON protocol for signaling communication.
type SignalMessage struct {
	Type      MessageType     `json:"type"`
	SessionID string          `json:"sessionid,omitempty"`
	ClientID  string          `json:"clientid,omitempty"`
	Token     string          `json:"token,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp int64           `json:"timestamp"`
	Nonce     string          `json:"nonce,omitempty"`
}

// Client is a WebSocket client for the signaling server.
type Client struct {
	mu   sync.Mutex
	conn *websocket.Conn

	serverURL string
	connected bool

	// Incoming messages channel
	incoming chan *SignalMessage

	// Callbacks for specific message types
	handlers map[MessageType]func(*SignalMessage)
	handlersMu sync.RWMutex

	// Context for lifecycle management
	ctx    context.Context
	cancel context.CancelFunc
}

// NewClient creates a new signaling client.
func NewClient(serverURL string) *Client {
	if serverURL == "" {
		serverURL = DefaultSignalingURL
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		serverURL: serverURL,
		incoming:  make(chan *SignalMessage, 64),
		handlers:  make(map[MessageType]func(*SignalMessage)),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Connect establishes a WebSocket connection to the signaling server.
func (c *Client) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	log.Info().Str("url", c.serverURL).Msg("Connecting to signaling server")

	conn, _, err := websocket.DefaultDialer.Dial(c.serverURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to signaling server: %w", err)
	}

	c.conn = conn
	c.connected = true

	// Start reading messages
	go c.readLoop()

	log.Info().Msg("Connected to signaling server")
	return nil
}

// ConnectWithRetry attempts to connect with exponential backoff.
func (c *Client) ConnectWithRetry(maxAttempts int) error {
	delay := reconnectBaseDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := c.Connect(); err == nil {
			return nil
		}

		if attempt == maxAttempts {
			return fmt.Errorf("failed to connect after %d attempts", maxAttempts)
		}

		log.Warn().
			Int("attempt", attempt).
			Int("max_attempts", maxAttempts).
			Dur("retry_in", delay).
			Msg("Connection failed, retrying")

		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-time.After(delay):
		}

		// Exponential backoff with cap
		delay *= 2
		if delay > reconnectMaxDelay {
			delay = reconnectMaxDelay
		}
	}

	return fmt.Errorf("failed to connect")
}

// Send sends a signaling message to the server.
func (c *Client) Send(msg *SignalMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected || c.conn == nil {
		return fmt.Errorf("not connected to signaling server")
	}

	// Set timestamp
	msg.Timestamp = time.Now().Unix()

	c.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := c.conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	log.Debug().
		Str("type", string(msg.Type)).
		Str("session_id", msg.SessionID).
		Msg("Sent signaling message")

	return nil
}

// CreateSession sends a session creation request and waits for the response.
func (c *Client) CreateSession(mode, target string) (*SignalMessage, error) {
	payload, _ := json.Marshal(map[string]string{
		"mode":   mode,
		"target": target,
	})

	msg := &SignalMessage{
		Type:    MsgTypeCreate,
		Payload: payload,
	}

	if err := c.Send(msg); err != nil {
		return nil, err
	}

	// Wait for session-created response
	select {
	case response := <-c.incoming:
		if response.Type == MsgTypeSessionCreated {
			return response, nil
		}
		if response.Type == MsgTypeError {
			return nil, fmt.Errorf("server error: %s", string(response.Payload))
		}
		return nil, fmt.Errorf("unexpected response type: %s", response.Type)
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for session creation response")
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// JoinSession sends a join request with the given token.
func (c *Client) JoinSession(token string) (*SignalMessage, error) {
	msg := &SignalMessage{
		Type:  MsgTypeJoin,
		Token: token,
	}

	if err := c.Send(msg); err != nil {
		return nil, err
	}

	// Wait for session info response
	select {
	case response := <-c.incoming:
		if response.Type == MsgTypeSessionCreated {
			return response, nil
		}
		if response.Type == MsgTypeError {
			return nil, fmt.Errorf("server error: %s", string(response.Payload))
		}
		return nil, fmt.Errorf("unexpected response type: %s", response.Type)
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for join response")
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}

// SendSDP sends an SDP offer or answer to a specific client.
func (c *Client) SendSDP(sessionID string, clientID string, sdpType string, sdp string) error {
	payload, _ := json.Marshal(map[string]string{
		"type": sdpType,
		"sdp":  sdp,
	})

	msgType := MsgTypeOffer
	if sdpType == "answer" {
		msgType = MsgTypeAnswer
	}

	return c.Send(&SignalMessage{
		Type:      msgType,
		SessionID: sessionID,
		ClientID:  clientID,
		Payload:   payload,
	})
}

// SendICECandidate sends an ICE candidate to a specific client via signaling.
func (c *Client) SendICECandidate(sessionID string, clientID string, candidate string, sdpMid string, sdpMLineIndex *int) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"candidate":     candidate,
		"sdpMid":        sdpMid,
		"sdpMLineIndex": sdpMLineIndex,
	})

	return c.Send(&SignalMessage{
		Type:      MsgTypeICECandidate,
		SessionID: sessionID,
		ClientID:  clientID,
		Payload:   payload,
	})
}

// OnMessage registers a handler for a specific message type.
func (c *Client) OnMessage(msgType MessageType, handler func(*SignalMessage)) {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	c.handlers[msgType] = handler
}

// Incoming returns the channel for all incoming messages.
func (c *Client) Incoming() <-chan *SignalMessage {
	return c.incoming
}

// readLoop continuously reads messages from the WebSocket connection.
func (c *Client) readLoop() {
	defer func() {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_, msgBytes, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Error().Err(err).Msg("WebSocket read error")
			}
			return
		}

		var msg SignalMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Error().Err(err).Msg("Failed to parse incoming message")
			continue
		}

		log.Debug().
			Str("type", string(msg.Type)).
			Str("session_id", msg.SessionID).
			Msg("Received signaling message")

		// Check for registered handler
		c.handlersMu.RLock()
		handler, exists := c.handlers[msg.Type]
		c.handlersMu.RUnlock()

		if exists {
			go handler(&msg)
		}

		// Also send to incoming channel (non-blocking)
		select {
		case c.incoming <- &msg:
		default:
			log.Warn().Msg("Incoming message buffer full")
		}
	}
}

// Close closes the signaling client connection.
func (c *Client) Close() error {
	c.cancel()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		err := c.conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
		if err != nil {
			log.Debug().Err(err).Msg("Error sending close message")
		}
		c.conn.Close()
		c.connected = false
	}

	close(c.incoming)
	log.Info().Msg("Signaling client closed")
	return nil
}

// IsConnected returns whether the client is currently connected.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}
