package crypto

import (
	"strings"
	"testing"
)

func TestGenerateToken(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Verify prefix
	if !strings.HasPrefix(token, TokenPrefix) {
		t.Errorf("Expected token to start with %q, got %q", TokenPrefix, token)
	}

	// Verify length (prefix + 40 hex chars = 43 chars)
	expectedLength := len(TokenPrefix) + (TokenRandomBytes * 2)
	if len(token) != expectedLength {
		t.Errorf("Expected token length %d, got %d", expectedLength, len(token))
	}

	// Verify uniqueness
	token2, _ := GenerateToken()
	if token == token2 {
		t.Errorf("Generated duplicate tokens: %s", token)
	}
}

func TestGenerateNonce(t *testing.T) {
	nonce, err := GenerateNonce()
	if err != nil {
		t.Fatalf("Failed to generate nonce: %v", err)
	}

	expectedLength := NonceBytes * 2
	if len(nonce) != expectedLength {
		t.Errorf("Expected nonce length %d, got %d", expectedLength, len(nonce))
	}
}
