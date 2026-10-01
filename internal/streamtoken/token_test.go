package streamtoken

import (
	"testing"
	"time"
)

func TestStartedAtRoundTrip(t *testing.T) {
	started := time.Date(2026, 8, 16, 12, 34, 56, 987654321, time.UTC)
	token, err := Sign(Claims{SessionID: "s", OriginalStartedAtUnixNano: started.UnixNano()}, "secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(token, "secret")
	if err != nil {
		t.Fatal(err)
	}
	got, source := claims.StartedAt()
	if source != StartedAtSourceClaim || !got.Equal(started) || claims.OriginalStartedAtUnixNano != started.UnixNano() {
		t.Fatalf("StartedAt = (%s, %q), claim=%d; want (%s, %q), claim=%d", got, source, claims.OriginalStartedAtUnixNano, started, StartedAtSourceClaim, started.UnixNano())
	}
}

func TestStartedAtLegacyAndAbsent(t *testing.T) {
	token, err := Sign(Claims{SessionID: "legacy"}, "secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(token, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, source := claims.StartedAt(); got.IsZero() || source != StartedAtSourceIssuedAt {
		t.Fatalf("legacy StartedAt = (%s, %q), want non-zero issued_at", got, source)
	}
	if got, source := (&Claims{}).StartedAt(); !got.IsZero() || source != StartedAtSourceNone {
		t.Fatalf("empty StartedAt = (%s, %q), want zero none", got, source)
	}
}

func TestStartedAtSameSecondPreservesActualOrder(t *testing.T) {
	older := time.Date(2026, 8, 16, 12, 0, 0, 100, time.UTC)
	newer := older.Add(200 * time.Nanosecond)
	resolve := func(id string, started time.Time) time.Time {
		t.Helper()
		token, err := Sign(Claims{SessionID: id, OriginalStartedAtUnixNano: started.UnixNano()}, "secret", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		claims, err := Verify(token, "secret")
		if err != nil {
			t.Fatal(err)
		}
		got, _ := claims.StartedAt()
		return got
	}
	if !resolve("z-session", older).Before(resolve("a-session", newer)) {
		t.Fatal("same-second token round trip lost nanosecond ordering")
	}
}

func TestSourceAudioChannelsRoundTripUsesDistinctClaim(t *testing.T) {
	token, err := Sign(Claims{
		SessionID:           "source-audio",
		AudioChannels:       2,
		SourceAudioChannels: 6,
	}, "secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := Verify(token, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if claims.SourceAudioChannels != 6 || claims.AudioChannels != 2 {
		t.Fatalf("source channels = %d, legacy audio channels = %d; want 6 and 2", claims.SourceAudioChannels, claims.AudioChannels)
	}

	legacyToken, err := Sign(Claims{SessionID: "legacy", AudioChannels: 2}, "secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	legacyClaims, err := Verify(legacyToken, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if legacyClaims.SourceAudioChannels != 0 || legacyClaims.AudioChannels != 2 {
		t.Fatalf("legacy source channels = %d, audio channels = %d; want 0 and 2", legacyClaims.SourceAudioChannels, legacyClaims.AudioChannels)
	}
}
