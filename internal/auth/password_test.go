package auth

import (
	"strings"
	"testing"
)

func TestHashPasswordEnforcesLength(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Error("expected a short password to be rejected")
	}
	// bcrypt would silently truncate past 72 bytes; reject instead.
	if _, err := HashPassword(strings.Repeat("a", 73)); err == nil {
		t.Error("expected a password over 72 bytes to be rejected")
	}
}

func TestCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(hash, "correct horse battery") {
		t.Error("expected the right password to match")
	}
	if CheckPassword(hash, "wrong password here") {
		t.Error("expected a wrong password not to match")
	}
	if CheckPassword("", "anything at all") {
		t.Error("an unknown user (empty hash) must never authenticate")
	}
}

func TestTempPasswordIsValidAndRandom(t *testing.T) {
	a, _ := TempPassword()
	b, _ := TempPassword()
	if a == b {
		t.Fatal("two temp passwords were identical")
	}
	if _, err := HashPassword(a); err != nil {
		t.Fatalf("temp password fails the length policy: %v", err)
	}
}
