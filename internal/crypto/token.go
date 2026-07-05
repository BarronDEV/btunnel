package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const (
	// TokenPrefix is the prefix for all btunnel tokens.
	TokenPrefix = "bt-"

	// TokenRandomBytes is the number of random bytes for token generation.
	// 20 bytes = 40 hex chars, providing 160 bits of entropy.
	// Brute-force infeasible: 2^160 possible values.
	TokenRandomBytes = 20

	// NonceBytes is the number of random bytes for nonce generation.
	NonceBytes = 16
)

// GenerateToken creates a cryptographically secure, single-use token.
// Format: bt-<40 hex chars> (e.g., bt-a7f3c9e2b1d4f6a8c0e2b4d6f8a0c2e4b6d8f0a2)
// Uses crypto/rand for cryptographic security against brute-force attacks.
func GenerateToken() (string, error) {
	b := make([]byte, TokenRandomBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return TokenPrefix + hex.EncodeToString(b), nil
}

// GenerateNonce creates a cryptographically secure nonce for message replay prevention.
func GenerateNonce() (string, error) {
	b := make([]byte, NonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// GenerateSessionID creates a unique session identifier.
func GenerateSessionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate session ID: %w", err)
	}
	return "sess-" + hex.EncodeToString(b), nil
}
