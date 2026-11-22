package auth

import (
	"crypto/rand"
	"encoding/hex"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword returns a bcrypt hash suitable for storage.
func HashPassword(password string) (string, error) {
	sum, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(sum), nil
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// NewSessionToken returns an opaque token for a cookie and its storage hash.
func NewSessionToken() (plain, hash string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", err
	}
	plain = hex.EncodeToString(buf)
	return plain, Hash(plain), nil
}

// RandomPassword returns a human-readable password with no ambiguous glyphs.
func RandomPassword(length int) (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, v := range buf {
		b.WriteByte(alphabet[int(v)%len(alphabet)])
	}
	return b.String(), nil
}

// SessionCookieName is the cookie that carries the admin session.
const SessionCookieName = "llmgw_session"
