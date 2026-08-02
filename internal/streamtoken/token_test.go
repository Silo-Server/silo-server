package streamtoken

import (
	"testing"
	"time"
)

func TestSessionGenerationAndStartedAtSurviveSigning(t *testing.T) {
	claims := Claims{
		SessionID:         "session-1",
		SessionGeneration: "cb61ec6b-f95c-4c61-8506-34e46f2810dc",
		StartedAt:         "2026-08-01T12:00:00.123456Z",
	}
	token, err := Sign(claims, "secret", time.Minute)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	got, err := Verify(token, "secret")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.SessionGeneration != claims.SessionGeneration || got.StartedAt != claims.StartedAt {
		t.Fatalf("identity = (%q, %q), want (%q, %q)", got.SessionGeneration, got.StartedAt, claims.SessionGeneration, claims.StartedAt)
	}
}
