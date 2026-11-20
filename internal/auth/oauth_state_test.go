package auth

import (
	"strings"
	"testing"
	"time"
)

var testStateSecret = []byte("test-secret-do-not-use-in-prod")

func TestVerifyState_RejectsTampered(t *testing.T) {
	in := StatePayload{Nonce: "n", InstallID: "i", ExpiresAt: time.Now().Add(time.Hour)}
	signed := SignState(testStateSecret, in)

	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 {
		t.Fatalf("malformed signed: %q", signed)
	}
	// Flip one character in the payload portion.
	flipped := flipFirstChar(parts[0])
	tampered := flipped + "." + parts[1]

	if _, err := VerifyState(testStateSecret, tampered); err == nil {
		t.Error("VerifyState should reject tampered payload")
	}
}

func TestVerifyState_RejectsExpired(t *testing.T) {
	in := StatePayload{Nonce: "n", InstallID: "i", ExpiresAt: time.Now().Add(-time.Minute)}
	signed := SignState(testStateSecret, in)
	if _, err := VerifyState(testStateSecret, signed); err == nil {
		t.Error("VerifyState should reject expired state")
	}
}

func flipFirstChar(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] == 'A' {
		b[0] = 'B'
	} else {
		b[0] = 'A'
	}
	return string(b)
}
