// Package auth is TDMS's built-in login: bcrypt passwords and server-side
// sessions.
package auth

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"golang.org/x/crypto/bcrypt"
)

const (
	MinPasswordLen = 10
	// bcrypt silently ignores everything past 72 bytes, so a longer
	// password would authenticate with just its prefix; reject instead.
	maxPasswordBytes = 72
)

var ErrWeakPassword = fmt.Errorf("password must be %d to %d characters", MinPasswordLen, maxPasswordBytes)

// HashPassword validates a new password's length and returns its bcrypt hash.
func HashPassword(password string) (string, error) {
	if len(password) < MinPasswordLen || len(password) > maxPasswordBytes {
		return "", ErrWeakPassword
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

// dummyHash is compared against when a login names an unknown email, so a
// wrong email takes as long as a wrong password and accounts can't be
// discovered by timing.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("tdms-dummy-password"), bcrypt.DefaultCost)

// CheckPassword reports whether password matches hash. Pass an empty hash
// for an unknown user: it still spends a full bcrypt comparison.
func CheckPassword(hash, password string) bool {
	if hash == "" {
		bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// passwordAlphabet leaves out look-alike characters (0/O, 1/l/I) since temp
// passwords are read off a terminal and typed by hand.
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// TempPassword returns a random 16-character password for a new or reset
// account; the user must change it at first login.
func TempPassword() (string, error) {
	b := make([]byte, 16)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(passwordAlphabet))))
		if err != nil {
			return "", errors.New("generating password: " + err.Error())
		}
		b[i] = passwordAlphabet[n.Int64()]
	}
	return string(b), nil
}
