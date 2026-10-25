package domain_test

import (
	"encoding/base64"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func TestNewLeaseToken_EntropyAndUniqueness(t *testing.T) {
	const iterations = 10000
	seen := make(map[string]struct{}, iterations)

	for i := 0; i < iterations; i++ {
		tok, err := domain.NewLeaseToken()
		if err != nil {
			t.Fatalf("NewLeaseToken failed at iteration %d: %v", i, err)
		}

		decoded, err := base64.RawURLEncoding.DecodeString(tok)
		if err != nil {
			t.Fatalf("expected valid URL-safe base64 string, got %q: %v", tok, err)
		}
		if len(decoded) < 32 {
			t.Fatalf("expected at least 32 bytes of entropy, got %d bytes", len(decoded))
		}

		if _, exists := seen[tok]; exists {
			t.Fatalf("duplicate lease token detected after %d iterations: %s", i, tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestHashLeaseToken_DeterministicAndDifferentFromToken(t *testing.T) {
	tok, err := domain.NewLeaseToken()
	if err != nil {
		t.Fatalf("NewLeaseToken failed: %v", err)
	}

	h1 := domain.HashLeaseToken(tok)
	h2 := domain.HashLeaseToken(tok)

	if h1 != h2 {
		t.Fatalf("HashLeaseToken is non-deterministic: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("expected 64-char hex SHA-256 string, got length %d (%s)", len(h1), h1)
	}
	if h1 == tok {
		t.Fatalf("hash must not equal the raw token")
	}

	// Verify different tokens yield different hashes
	tok2, _ := domain.NewLeaseToken()
	h3 := domain.HashLeaseToken(tok2)
	if h1 == h3 {
		t.Fatalf("different tokens produced the same hash: %s", h1)
	}
}
