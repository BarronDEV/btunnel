package state

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// initialReconnectDelay is the first reconnection delay.
	initialReconnectDelay = 1 * time.Second

	// maxReconnectDelay is the maximum delay between reconnection attempts.
	maxReconnectDelay = 60 * time.Second

	// maxReconnectAttempts is the maximum number of reconnection attempts (0 = infinite).
	maxReconnectAttempts = 20

	// reconnectBackoffMultiplier is the exponential backoff multiplier.
	reconnectBackoffMultiplier = 2.0

	// healthCheckInterval is how often to check connection health.
	healthCheckInterval = 5 * time.Second
)

// ReconnectConfig holds configuration for auto-reconnection.
type ReconnectConfig struct {
	InitialDelay    time.Duration
	MaxDelay        time.Duration
	MaxAttempts     int
	BackoffMultiple float64
}

// DefaultReconnectConfig returns the default reconnection configuration.
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		InitialDelay:    initialReconnectDelay,
		MaxDelay:        maxReconnectDelay,
		MaxAttempts:     maxReconnectAttempts,
		BackoffMultiple: reconnectBackoffMultiplier,
	}
}

// ReconnectManager handles automatic reconnection with exponential backoff.
type ReconnectManager struct {
	mu     sync.Mutex
	config ReconnectConfig

	// Current state
	attempts int
	isActive bool

	// Callbacks
	onReconnect func() error // Called to attempt reconnection
	onSuccess   func()       // Called when reconnection succeeds
	onFailure   func(err error, attempt int) // Called on each failed attempt
	onGiveUp    func() // Called when max attempts reached

	// Context for cancellation
	ctx    context.Context
	cancel context.CancelFunc
}

// NewReconnectManager creates a new auto-reconnection manager.
func NewReconnectManager(config ReconnectConfig) *ReconnectManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &ReconnectManager{
		config: config,
		ctx:    ctx,
		cancel: cancel,
	}
}

// OnReconnect sets the callback that performs the actual reconnection.
func (r *ReconnectManager) OnReconnect(fn func() error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onReconnect = fn
}

// OnSuccess sets the callback for successful reconnection.
func (r *ReconnectManager) OnSuccess(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onSuccess = fn
}

// OnFailure sets the callback for failed reconnection attempts.
func (r *ReconnectManager) OnFailure(fn func(err error, attempt int)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onFailure = fn
}

// OnGiveUp sets the callback for when max attempts are exhausted.
func (r *ReconnectManager) OnGiveUp(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onGiveUp = fn
}

// Start begins the reconnection loop.
func (r *ReconnectManager) Start() {
	r.mu.Lock()
	if r.isActive {
		r.mu.Unlock()
		return
	}
	r.isActive = true
	r.attempts = 0
	r.mu.Unlock()

	go r.reconnectLoop()
}

// reconnectLoop is the main reconnection loop with exponential backoff.
func (r *ReconnectManager) reconnectLoop() {
	delay := r.config.InitialDelay

	for {
		r.mu.Lock()
		if !r.isActive {
			r.mu.Unlock()
			return
		}
		r.attempts++
		attempt := r.attempts
		reconnectFn := r.onReconnect
		r.mu.Unlock()

		// Check max attempts
		if r.config.MaxAttempts > 0 && attempt > r.config.MaxAttempts {
			log.Warn().
				Int("max_attempts", r.config.MaxAttempts).
				Msg("Max reconnection attempts reached, giving up")

			r.mu.Lock()
			r.isActive = false
			giveUpFn := r.onGiveUp
			r.mu.Unlock()

			if giveUpFn != nil {
				giveUpFn()
			}
			return
		}

		log.Info().
			Int("attempt", attempt).
			Dur("delay", delay).
			Msg("Attempting reconnection")

		// Wait before attempting
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(delay):
		}

		// Attempt reconnection
		if reconnectFn != nil {
			err := reconnectFn()
			if err == nil {
				// Success!
				log.Info().
					Int("attempt", attempt).
					Msg("Reconnection successful")

				r.mu.Lock()
				r.isActive = false
				r.attempts = 0
				successFn := r.onSuccess
				r.mu.Unlock()

				if successFn != nil {
					successFn()
				}
				return
			}

			// Failed
			log.Warn().
				Err(err).
				Int("attempt", attempt).
				Dur("next_delay", delay).
				Msg("Reconnection failed")

			r.mu.Lock()
			failureFn := r.onFailure
			r.mu.Unlock()

			if failureFn != nil {
				failureFn(err, attempt)
			}
		}

		// Exponential backoff
		delay = time.Duration(float64(delay) * r.config.BackoffMultiple)
		if delay > r.config.MaxDelay {
			delay = r.config.MaxDelay
		}
	}
}

// Stop cancels the reconnection loop.
func (r *ReconnectManager) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.isActive = false
	r.cancel()
	log.Debug().Msg("Reconnection manager stopped")
}

// IsActive returns whether a reconnection is currently in progress.
func (r *ReconnectManager) IsActive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.isActive
}

// Attempts returns the current number of reconnection attempts.
func (r *ReconnectManager) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempts
}

// CalculateDelay computes the delay for a given attempt number.
func CalculateDelay(config ReconnectConfig, attempt int) time.Duration {
	delay := float64(config.InitialDelay) * math.Pow(config.BackoffMultiple, float64(attempt-1))
	if delay > float64(config.MaxDelay) {
		delay = float64(config.MaxDelay)
	}
	return time.Duration(delay)
}
