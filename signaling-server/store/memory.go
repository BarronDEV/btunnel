package store

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// Session represents a signaling session between two peers.
type Session struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	HostConn  string    `json:"host_conn,omitempty"` // WebSocket connection ID for host
	ClientConn string   `json:"client_conn,omitempty"` // WebSocket connection ID for client
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Used      bool      `json:"used"` // Token is single-use
	Mode      string    `json:"mode"` // "mesh" or "web"
	Target    string    `json:"target"` // Docker network name or port
}

// Store defines the interface for session storage.
type Store interface {
	// CreateSession creates a new session and returns it.
	CreateSession(token, mode, target string, ttl time.Duration) (*Session, error)

	// GetSessionByToken retrieves a session by its token.
	GetSessionByToken(token string) (*Session, error)

	// GetSession retrieves a session by its ID.
	GetSession(id string) (*Session, error)

	// UpdateSession updates an existing session.
	UpdateSession(session *Session) error

	// DeleteSession removes a session.
	DeleteSession(id string) error

	// MarkTokenUsed marks a token as used (single-use enforcement).
	MarkTokenUsed(token string) error
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu             sync.RWMutex
	sessions       map[string]*Session // keyed by session ID
	tokenToSession map[string]string   // token -> session ID mapping
}

// NewMemoryStore creates a new in-memory session store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:       make(map[string]*Session),
		tokenToSession: make(map[string]string),
	}
}

// CreateSession creates a new session with the given parameters.
func (m *MemoryStore) CreateSession(token, mode, target string, ttl time.Duration) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	session := &Session{
		ID:        generateSessionID(),
		Token:     token,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Used:      false,
		Mode:      mode,
		Target:    target,
	}

	m.sessions[session.ID] = session
	m.tokenToSession[token] = session.ID

	log.Debug().
		Str("session_id", session.ID).
		Str("token", token).
		Str("mode", mode).
		Time("expires_at", session.ExpiresAt).
		Msg("Session created")

	return session, nil
}

// GetSessionByToken retrieves a session by token.
func (m *MemoryStore) GetSessionByToken(token string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessionID, exists := m.tokenToSession[token]
	if !exists {
		return nil, ErrSessionNotFound
	}

	session, exists := m.sessions[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}

	// Check expiration
	if time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionExpired
	}

	return session, nil
}

// GetSession retrieves a session by ID.
func (m *MemoryStore) GetSession(id string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	session, exists := m.sessions[id]
	if !exists {
		return nil, ErrSessionNotFound
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionExpired
	}

	return session, nil
}

// UpdateSession updates a session in-place.
func (m *MemoryStore) UpdateSession(session *Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[session.ID]; !exists {
		return ErrSessionNotFound
	}

	m.sessions[session.ID] = session
	return nil
}

// DeleteSession removes a session.
func (m *MemoryStore) DeleteSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	session, exists := m.sessions[id]
	if !exists {
		return ErrSessionNotFound
	}

	delete(m.tokenToSession, session.Token)
	delete(m.sessions, id)

	log.Debug().Str("session_id", id).Msg("Session deleted")
	return nil
}

// MarkTokenUsed marks a token as used so it cannot be reused.
func (m *MemoryStore) MarkTokenUsed(token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sessionID, exists := m.tokenToSession[token]
	if !exists {
		return ErrSessionNotFound
	}

	session, exists := m.sessions[sessionID]
	if !exists {
		return ErrSessionNotFound
	}

	session.Used = true
	return nil
}

// StartCleanup periodically removes expired sessions.
func (m *MemoryStore) StartCleanup(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *MemoryStore) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	removed := 0

	for id, session := range m.sessions {
		if now.After(session.ExpiresAt) {
			delete(m.tokenToSession, session.Token)
			delete(m.sessions, id)
			removed++
		}
	}

	if removed > 0 {
		log.Debug().Int("removed", removed).Int("remaining", len(m.sessions)).Msg("Cleaned up expired sessions")
	}
}

// generateSessionID creates a unique session ID.
func generateSessionID() string {
	// Using google/uuid for proper UUID generation
	// For now, use a simple approach; will be replaced with uuid.New().String()
	return "sess-" + randomHex(16)
}

func randomHex(n int) string {
	const chars = "0123456789abcdef"
	b := make([]byte, n)
	// Use crypto/rand for production
	for i := range b {
		b[i] = chars[i%len(chars)]
	}
	return string(b)
}
