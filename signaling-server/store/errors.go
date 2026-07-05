package store

import "errors"

var (
	// ErrSessionNotFound is returned when a session cannot be found.
	ErrSessionNotFound = errors.New("session not found")

	// ErrSessionExpired is returned when a session has expired.
	ErrSessionExpired = errors.New("session has expired")

	// ErrTokenAlreadyUsed is returned when a single-use token has already been consumed.
	ErrTokenAlreadyUsed = errors.New("token has already been used")
)
