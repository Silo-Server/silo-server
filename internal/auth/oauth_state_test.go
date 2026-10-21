package auth

import (
	"strings"
	"testing"
	"time"
)

var testStateSecret = []byte("test-secret-do-not-use-in-prod")

func TestSignAndVerifyState_Roundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	in := StatePayload{
		Nonce:     "nonce-1",
		InstallID: "inst-1",
		ExpiresAt: now.Add(10 * time.Minute),
	}
	signed := SignState(testStateSecret, in)
	if !strings.Contains(signed, ".") {
		t.Errorf("signed state should contain '.' separator: %q", signed)
	}
	out, err := VerifyState(testStateSecret, signed)
	if err != nil {
		t.Fatalf("VerifyState: %v", err)
	}
	if out.Nonce != in.Nonce || out.InstallID != in.InstallID {
		t.Errorf("payload mismatch: got %+v want %+v", out, in)
	}
	if !out.ExpiresAt.Equal(in.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", out.ExpiresAt, in.ExpiresAt)
	}
}

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

func TestVerifyState_RejectsMalformed(t *testing.T) {
	cases := []string{"", "nodot", "....", "x.y.z"}
	for _, c := range cases {
		if _, err := VerifyState(testStateSecret, c); err == nil {
			t.Errorf("VerifyState should reject %q", c)
		}
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
