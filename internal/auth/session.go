package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

const (
	CookieName = "tdms_session"
	// SessionLifetime is the absolute cap; IdleTimeout ends a session that
	// hasn't been used in a while, whichever comes first.
	SessionLifetime = 12 * time.Hour
	IdleTimeout     = 2 * time.Hour
)

// NewSessionToken returns a random token for the cookie and the SHA-256 hash
// to store. Only the hash reaches the database, so a leaked sessions table
// can't be replayed as cookies.
func NewSessionToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
