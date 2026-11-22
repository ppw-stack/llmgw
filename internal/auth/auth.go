package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

const KeyPrefix = "sk-gw-"

// Generate returns the plaintext key (shown once), its hash, and a short prefix.
func Generate() (plain, hash, prefix string, err error) {
	buf := make([]byte, 24)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plain = KeyPrefix + hex.EncodeToString(buf)
	hash = Hash(plain)
	prefix = plain[:len(KeyPrefix)+4]
	return plain, hash, prefix, nil
}

// Hash is the storage form of a gateway key or session token.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Extract pulls the gateway key from either protocol's auth header.
func Extract(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if v, ok := strings.CutPrefix(h, "Bearer "); ok {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(h)
	}
	if h := r.Header.Get("x-api-key"); h != "" {
		return strings.TrimSpace(h)
	}
	return ""
}

// EqualToken compares admin tokens in constant time.
func EqualToken(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
