package proxy

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestSessionInfoPreservesStartedAtAcrossTouches(t *testing.T) {
	started := time.Date(2026, 8, 16, 12, 34, 56, 987654321, time.UTC)
	tracker := nodesessions.NewTracker(nil, "http://proxy", "proxy", "proxy")
	claims := &streamtoken.Claims{SessionID: "s", UserID: 42, ProfileID: "p", MediaFileID: 77, OriginalStartedAtUnixNano: started.UnixNano()}

	first := sessionInfo(tracker, claims, "transcode")
	time.Sleep(time.Millisecond)
	second := sessionInfo(tracker, claims, "transcode")
	if first.StartedAtUnixNano != started.UnixNano() || second.StartedAtUnixNano != first.StartedAtUnixNano {
		t.Fatalf("touch reset StartedAtUnixNano: first=%d second=%d want=%d", first.StartedAtUnixNano, second.StartedAtUnixNano, started.UnixNano())
	}
	if first.StartedAtSource != string(streamtoken.StartedAtSourceClaim) {
		t.Fatalf("StartedAtSource = %q, want claim", first.StartedAtSource)
	}
}

func TestSessionInfoLegacyUsesIssuedAt(t *testing.T) {
	issued := time.Date(2026, 8, 16, 12, 34, 56, 0, time.UTC)
	tracker := nodesessions.NewTracker(nil, "http://proxy", "proxy", "proxy")
	claims := &streamtoken.Claims{SessionID: "legacy"}
	claims.IssuedAt = jwt.NewNumericDate(issued)

	info := sessionInfo(tracker, claims, "direct_play")
	if info.StartedAtUnixNano != issued.UnixNano() || info.StartedAtSource != string(streamtoken.StartedAtSourceIssuedAt) {
		t.Fatalf("legacy session info = %+v", info)
	}
}
