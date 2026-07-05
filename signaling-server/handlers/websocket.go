package handlers

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/barronDEV/btunnel/internal/crypto"
	"github.com/barronDEV/btunnel/signaling-server/store"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
)

var (
	activeConnections = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "btunnel_signaling_active_connections",
		Help: "Number of active WebSocket connections to the signaling server.",
	})

	sessionsCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "btunnel_signaling_sessions_created_total",
		Help: "Total number of sessions created.",
	})

	sessionJoins = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "btunnel_signaling_session_joins_total",
		Help: "Total number of successful client joins.",
	})

	errorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "btunnel_signaling_errors_total",
		Help: "Total number of signaling errors.",
	}, []string{"code"})
)

func init() {
	prometheus.MustRegister(activeConnections)
	prometheus.MustRegister(sessionsCreated)
	prometheus.MustRegister(sessionJoins)
	prometheus.MustRegister(errorsTotal)
}

const (
	// Session TTL: 5 minutes
	sessionTTL = 5 * time.Minute

	// Timestamp tolerance: ±30 seconds
	timestampTolerance = 30 * time.Second

	// Rate limiting: max tokens per IP per minute
	maxTokensPerMinute = 10

	// WebSocket settings
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// TODO: In production, restrict origins
		return true
	},
}

// Handler manages WebSocket signaling connections.
type Handler struct {
	store store.Store

	// Active connections: connection ID -> *websocket.Conn
	connections sync.Map

	// Session to connection mapping: session ID -> peer connections
	sessionPeers sync.Map

	// Rate limiting: IP -> []timestamp
	rateLimiter sync.Map
}

// peerPair holds the host connection and multiple client connections in a session.
type peerPair struct {
	mu               sync.Mutex
	host             *websocket.Conn
	clients          map[string]*websocket.Conn
	queuedCandidates []*SignalMessage
}

// NewHandler creates a new signaling handler.
func NewHandler(s store.Store) *Handler {
	return &Handler{
		store: s,
	}
}

// HandleHealth is a simple health check endpoint.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"service": "btunnel-signaling",
	})
}

// HandleWebSocket upgrades HTTP to WebSocket and manages signaling.
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("WebSocket upgrade failed")
		return
	}

	var currentSessionID string
	var currentClientID string
	var isHost bool
	var isClient bool

	defer func() {
		activeConnections.Dec()
		conn.Close()

		if currentSessionID != "" {
			if isHost {
				log.Info().Str("session_id", currentSessionID).Msg("Host disconnected, cleaning up session")
				// Close all clients
				if peersRaw, ok := h.sessionPeers.Load(currentSessionID); ok {
					peers := peersRaw.(*peerPair)
					peers.mu.Lock()
					for cid, cConn := range peers.clients {
						log.Debug().Str("client_id", cid).Msg("Closing client connection due to host disconnect")
						cConn.Close()
					}
					peers.clients = make(map[string]*websocket.Conn)
					peers.mu.Unlock()
				}
				h.sessionPeers.Delete(currentSessionID)
				h.store.DeleteSession(currentSessionID)
			} else if isClient && currentClientID != "" {
				log.Info().Str("session_id", currentSessionID).Str("client_id", currentClientID).Msg("Client disconnected")
				if peersRaw, ok := h.sessionPeers.Load(currentSessionID); ok {
					peers := peersRaw.(*peerPair)
					peers.mu.Lock()
					delete(peers.clients, currentClientID)
					peers.mu.Unlock()
				}
			}
		}
	}()

	activeConnections.Inc()

	clientIP := r.RemoteAddr
	log.Info().Str("ip", clientIP).Msg("New WebSocket connection")

	// Set read deadline for pong
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Start ping ticker
	done := make(chan struct{})
	go h.pingLoop(conn, done)
	defer close(done)

	// Read messages
	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.Error().Err(err).Msg("WebSocket read error")
			}
			break
		}

		var msg SignalMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Error().Err(err).Msg("Failed to parse signaling message")
			h.sendError(conn, "", "PARSE_ERROR", "Invalid message format")
			continue
		}

		// Validate timestamp (±30s tolerance for replay prevention)
		if !h.validateTimestamp(msg.Timestamp) {
			h.sendError(conn, msg.SessionID, "TIMESTAMP_ERROR", "Message timestamp out of range")
			continue
		}

		// Route message by type
		switch msg.Type {
		case MsgTypeCreate:
			if sessionID, ok := h.handleCreate(conn, &msg, clientIP); ok {
				currentSessionID = sessionID
				isHost = true
			}
		case MsgTypeJoin:
			if sessionID, clientID, ok := h.handleJoin(conn, &msg, clientIP); ok {
				currentSessionID = sessionID
				currentClientID = clientID
				isClient = true
			}
		case MsgTypeOffer:
			h.handleRelay(conn, &msg, currentClientID, true) // relay to client
		case MsgTypeAnswer:
			h.handleRelay(conn, &msg, currentClientID, false) // relay to host
		case MsgTypeICECandidate:
			h.handleICECandidate(conn, &msg, currentClientID, isHost)
		default:
			h.sendError(conn, msg.SessionID, "UNKNOWN_TYPE", "Unknown message type")
		}
	}
}

// handleCreate processes a session creation request from the host.
func (h *Handler) handleCreate(conn *websocket.Conn, msg *SignalMessage, clientIP string) (string, bool) {
	// Rate limiting check
	if !h.checkRateLimit(clientIP) {
		h.sendError(conn, "", "RATE_LIMIT", "Too many session creation requests")
		return "", false
	}

	// Parse create payload
	var payload CreatePayload
	if msg.Payload != nil {
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			h.sendError(conn, "", "PARSE_ERROR", "Invalid create payload")
			return "", false
		}
	}

	// Generate cryptographically secure token
	token, err := crypto.GenerateToken()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate token")
		h.sendError(conn, "", "INTERNAL_ERROR", "Failed to generate session token")
		return "", false
	}

	// Create session in store
	session, err := h.store.CreateSession(token, payload.Mode, payload.Target, sessionTTL)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create session")
		h.sendError(conn, "", "INTERNAL_ERROR", "Failed to create session")
		return "", false
	}

	sessionsCreated.Inc()

	// Register host connection
	peers := &peerPair{host: conn, clients: make(map[string]*websocket.Conn)}
	h.sessionPeers.Store(session.ID, peers)

	// Send session-created response
	responsePayload := SessionCreatedPayload{
		Token:      token,
		SessionID:  session.ID,
		ICEServers: h.getICEServers(),
	}

	response, err := NewSignalMessage(MsgTypeSessionCreated, session.ID, responsePayload)
	if err != nil {
		log.Error().Err(err).Msg("Failed to create response message")
		return "", false
	}

	h.sendMessage(conn, response)

	log.Info().
		Str("session_id", session.ID).
		Str("token", token[:8]+"...").
		Str("mode", payload.Mode).
		Msg("Session created")

	return session.ID, true
}

// handleJoin processes a client joining an existing session.
func (h *Handler) handleJoin(conn *websocket.Conn, msg *SignalMessage, clientIP string) (string, string, bool) {
	token := msg.Token
	if token == "" {
		h.sendError(conn, "", "MISSING_TOKEN", "Token is required to join")
		return "", "", false
	}

	// Look up session by token
	session, err := h.store.GetSessionByToken(token)
	if err != nil {
		if err == store.ErrSessionNotFound {
			h.sendError(conn, "", "INVALID_TOKEN", "Session not found for this token")
		} else if err == store.ErrSessionExpired {
			h.sendError(conn, "", "TOKEN_EXPIRED", "Session has expired")
		}
		return "", "", false
	}

	// Generate a unique client ID
	clientID, err := crypto.GenerateToken()
	if err != nil {
		log.Error().Err(err).Msg("Failed to generate client ID")
		h.sendError(conn, session.ID, "INTERNAL_ERROR", "Failed to generate client ID")
		return "", "", false
	}

	// Register client connection
	peersRaw, ok := h.sessionPeers.Load(session.ID)
	if !ok {
		h.sendError(conn, session.ID, "NO_HOST", "Host is not connected")
		return "", "", false
	}

	peers := peersRaw.(*peerPair)
	peers.mu.Lock()
	peers.clients[clientID] = conn
	queued := peers.queuedCandidates
	// Don't clear queued candidates, other clients joining later might need them
	peers.mu.Unlock()

	// Notify host that a peer has joined with clientID
	notification, _ := NewSignalMessage(MsgTypePeerJoined, session.ID, nil)
	notification.ClientID = clientID
	h.sendMessage(peers.host, notification)

	// Send ICE server list to client along with their assigned clientID
	joinResponse, _ := NewSignalMessage(MsgTypeSessionCreated, session.ID, SessionCreatedPayload{
		SessionID:  session.ID,
		ICEServers: h.getICEServers(),
	})
	joinResponse.ClientID = clientID
	h.sendMessage(conn, joinResponse)

	// Flush queued host candidates to the newly connected client
	for _, candidate := range queued {
		candMsg := *candidate
		candMsg.ClientID = clientID
		h.sendMessage(conn, &candMsg)
	}

	sessionJoins.Inc()

	log.Info().
		Str("session_id", session.ID).
		Str("ip", clientIP).
		Msg("Client joined session")

	return session.ID, clientID, true
}

// handleRelay forwards SDP offer/answer between host and client.
func (h *Handler) handleRelay(conn *websocket.Conn, msg *SignalMessage, currentClientID string, toClient bool) {
	peersRaw, ok := h.sessionPeers.Load(msg.SessionID)
	if !ok {
		h.sendError(conn, msg.SessionID, "SESSION_NOT_FOUND", "Session not found")
		return
	}

	peers := peersRaw.(*peerPair)
	peers.mu.Lock()
	defer peers.mu.Unlock()

	var target *websocket.Conn
	if toClient {
		target = peers.clients[msg.ClientID]
	} else {
		target = peers.host
		msg.ClientID = currentClientID
	}

	if target == nil {
		h.sendError(conn, msg.SessionID, "PEER_NOT_CONNECTED", "Target peer is not connected")
		return
	}

	h.sendMessage(target, msg)

	log.Debug().
		Str("session_id", msg.SessionID).
		Str("client_id", msg.ClientID).
		Str("type", string(msg.Type)).
		Bool("to_client", toClient).
		Msg("Relayed message")
}

// handleICECandidate relays ICE candidates to the other peer or queues them if not connected.
func (h *Handler) handleICECandidate(conn *websocket.Conn, msg *SignalMessage, currentClientID string, isHost bool) {
	peersRaw, ok := h.sessionPeers.Load(msg.SessionID)
	if !ok {
		h.sendError(conn, msg.SessionID, "SESSION_NOT_FOUND", "Session not found")
		return
	}

	peers := peersRaw.(*peerPair)
	peers.mu.Lock()
	defer peers.mu.Unlock()

	var target *websocket.Conn
	if isHost {
		if msg.ClientID != "" {
			target = peers.clients[msg.ClientID]
		}
	} else {
		target = peers.host
		msg.ClientID = currentClientID
	}

	if target == nil {
		if isHost {
			// Queue candidate for future clients
			peers.queuedCandidates = append(peers.queuedCandidates, msg)
			log.Debug().Str("session_id", msg.SessionID).Msg("Host ICE candidate queued (no client yet)")
		} else {
			h.sendError(conn, msg.SessionID, "HOST_NOT_CONNECTED", "Host is not connected")
		}
		return
	}

	h.sendMessage(target, msg)
}

// sendMessage sends a SignalMessage over WebSocket.
func (h *Handler) sendMessage(conn *websocket.Conn, msg interface{}) {
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(msg); err != nil {
		log.Error().Err(err).Msg("Failed to send WebSocket message")
	}
}

// sendError sends an error message over WebSocket and increments error metrics.
func (h *Handler) sendError(conn *websocket.Conn, sessionID, code, message string) {
	errorsTotal.WithLabelValues(code).Inc()
	errMsg, _ := NewErrorMessage(sessionID, code, message)
	h.sendMessage(conn, errMsg)
}

// pingLoop sends periodic pings to keep the connection alive.
func (h *Handler) pingLoop(conn *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// validateTimestamp checks if the message timestamp is within tolerance.
func (h *Handler) validateTimestamp(ts int64) bool {
	msgTime := time.Unix(ts, 0)
	diff := time.Since(msgTime)
	if diff < 0 {
		diff = -diff
	}
	return diff <= timestampTolerance
}

// checkRateLimit enforces per-IP rate limiting for session creation.
func (h *Handler) checkRateLimit(ip string) bool {
	now := time.Now()
	windowStart := now.Add(-1 * time.Minute)

	// Load or create timestamp list for this IP
	val, _ := h.rateLimiter.LoadOrStore(ip, &rateLimitEntry{})
	entry := val.(*rateLimitEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// Remove timestamps outside the window
	filtered := entry.timestamps[:0]
	for _, ts := range entry.timestamps {
		if ts.After(windowStart) {
			filtered = append(filtered, ts)
		}
	}
	entry.timestamps = filtered

	// Check limit
	if len(entry.timestamps) >= maxTokensPerMinute {
		log.Warn().Str("ip", ip).Int("count", len(entry.timestamps)).Msg("Rate limit exceeded")
		return false
	}

	entry.timestamps = append(entry.timestamps, now)
	return true
}

type rateLimitEntry struct {
	mu         sync.Mutex
	timestamps []time.Time
}

// getICEServers generates the STUN and dynamic TURN configuration.
func (h *Handler) getICEServers() []ICEServer {
	servers := []ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{URLs: []string{"stun:stun1.l.google.com:19302"}},
	}

	secret := os.Getenv("BTUNNEL_TURN_SECRET")
	turnURL := os.Getenv("BTUNNEL_TURN_SERVER_URL")

	if secret != "" && turnURL != "" {
		// Valid for 1 hour
		ttl := 3600
		timestamp := time.Now().Unix() + int64(ttl)
		username := fmt.Sprintf("%d:btunnel-user", timestamp)

		// HMAC-SHA1 password generation for Coturn short-term credentials
		mac := hmac.New(sha1.New, []byte(secret))
		mac.Write([]byte(username))
		password := base64.StdEncoding.EncodeToString(mac.Sum(nil))

		servers = append(servers, ICEServer{
			URLs:       []string{turnURL},
			Username:   username,
			Credential: password,
		})

		log.Debug().
			Str("turn_url", turnURL).
			Str("username", username).
			Msg("Generated dynamic TURN credentials")
	}

	return servers
}

// validateTimestampAbs returns absolute difference for timestamp validation.
func validateTimestampAbs(a, b float64) float64 {
	return math.Abs(a - b)
}
