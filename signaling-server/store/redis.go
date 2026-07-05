package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog/log"
)

// RedisStore is a Redis implementation of the Store interface.
type RedisStore struct {
	client *redis.Client
	ctx    context.Context
}

// NewRedisStore creates a new RedisStore.
func NewRedisStore(addr, password string, db int) *RedisStore {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	return &RedisStore{
		client: rdb,
		ctx:    context.Background(),
	}
}

// CreateSession creates a new session in Redis.
func (r *RedisStore) CreateSession(token, mode, target string, ttl time.Duration) (*Session, error) {
	now := time.Now()
	sessionID := "sess-" + redisRandomHex(16)

	session := &Session{
		ID:        sessionID,
		Token:     token,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		Used:      false,
		Mode:      mode,
		Target:    target,
	}

	data, err := json.Marshal(session)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session: %w", err)
	}

	pipe := r.client.Pipeline()
	// Store session data by ID with expiration
	pipe.Set(r.ctx, "btunnel:session:"+sessionID, data, ttl)
	// Map token to session ID with expiration
	pipe.Set(r.ctx, "btunnel:token:"+token, sessionID, ttl)

	_, err = pipe.Exec(r.ctx)
	if err != nil {
		return nil, fmt.Errorf("redis pipeline execution failed: %w", err)
	}

	log.Debug().
		Str("session_id", sessionID).
		Str("token", token).
		Str("mode", mode).
		Msg("Session created in Redis")

	return session, nil
}

// GetSessionByToken retrieves a session by its token.
func (r *RedisStore) GetSessionByToken(token string) (*Session, error) {
	sessionID, err := r.client.Get(r.ctx, "btunnel:token:"+token).Result()
	if err == redis.Nil {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("failed to get token mapping: %w", err)
	}

	return r.GetSession(sessionID)
}

// GetSession retrieves a session by ID.
func (r *RedisStore) GetSession(id string) (*Session, error) {
	data, err := r.client.Get(r.ctx, "btunnel:session:"+id).Result()
	if err == redis.Nil {
		return nil, ErrSessionNotFound
	} else if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	var session Session
	if err := json.Unmarshal([]byte(data), &session); err != nil {
		return nil, fmt.Errorf("failed to unmarshal session: %w", err)
	}

	if time.Now().After(session.ExpiresAt) {
		return nil, ErrSessionExpired
	}

	return &session, nil
}

// UpdateSession updates a session in Redis.
func (r *RedisStore) UpdateSession(session *Session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	ttl := time.Until(session.ExpiresAt)
	if ttl <= 0 {
		return ErrSessionExpired
	}

	err = r.client.Set(r.ctx, "btunnel:session:"+session.ID, data, ttl).Err()
	if err != nil {
		return fmt.Errorf("failed to update session: %w", err)
	}

	return nil
}

// DeleteSession removes a session and token mappings from Redis.
func (r *RedisStore) DeleteSession(id string) error {
	session, err := r.GetSession(id)
	if err != nil {
		return err
	}

	pipe := r.client.Pipeline()
	pipe.Del(r.ctx, "btunnel:session:"+id)
	pipe.Del(r.ctx, "btunnel:token:"+session.Token)

	_, err = pipe.Exec(r.ctx)
	if err != nil {
		return fmt.Errorf("failed to delete session in Redis: %w", err)
	}

	log.Debug().Str("session_id", id).Msg("Session deleted from Redis")
	return nil
}

// MarkTokenUsed marks a token as used in the stored session.
func (r *RedisStore) MarkTokenUsed(token string) error {
	session, err := r.GetSessionByToken(token)
	if err != nil {
		return err
	}

	session.Used = true
	return r.UpdateSession(session)
}

func redisRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
