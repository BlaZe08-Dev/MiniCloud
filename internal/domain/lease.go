package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewLeaseToken generates a cryptographically secure, URL-safe lease token
// containing at least 32 bytes (256 bits) of entropy from crypto/rand.
// It contains no sequential counters or predictable data.
func NewLeaseToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate secure lease token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashLeaseToken computes the SHA-256 digest of a raw lease token, returned
// as a lowercase hexadecimal string for durable storage and constant-time verification.
func HashLeaseToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
