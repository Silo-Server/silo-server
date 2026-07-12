package proxy

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestTranscodeTransportIDFromClaims(t *testing.T) {
	claims := &streamtoken.Claims{SessionID: "public-session", TranscodeTransportID: "public-session-plan-a"}
	if got := transcodeTransportIDFromClaims(claims); got != claims.TranscodeTransportID {
		t.Fatalf("transport id = %q", got)
	}
	claims.TranscodeTransportID = ""
	if got := transcodeTransportIDFromClaims(claims); got != claims.SessionID {
		t.Fatalf("legacy transport id = %q", got)
	}
}
