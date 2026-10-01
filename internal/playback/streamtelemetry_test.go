package playback

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func TestTelemetryTokenTiming(t *testing.T) {
	iat := time.Unix(1_700_000_000, 0).UTC()
	claimStart := iat.Add(-time.Minute)
	tests := []struct {
		name          string
		claims        *streamtoken.Claims
		wantStarted   time.Time
		wantSource    streamtelemetry.StartedAtSource
		wantIssued    time.Time
		wantIssuedSrc streamtelemetry.TokenIssuedAtSource
	}{
		{name: "nil", wantSource: streamtelemetry.StartedAtSourceFirstSeen, wantIssuedSrc: streamtelemetry.TokenIssuedAtSourceNone},
		{name: "original start claim", claims: &streamtoken.Claims{OriginalStartedAtUnixNano: claimStart.UnixNano(), RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(iat)}}, wantStarted: claimStart, wantSource: streamtelemetry.StartedAtSourceClaim, wantIssued: iat, wantIssuedSrc: streamtelemetry.TokenIssuedAtSourceVerified},
		{name: "issued at only", claims: &streamtoken.Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(iat)}}, wantStarted: iat, wantSource: streamtelemetry.StartedAtSourceIssuedAt, wantIssued: iat, wantIssuedSrc: streamtelemetry.TokenIssuedAtSourceVerified},
		{name: "neither", claims: &streamtoken.Claims{}, wantSource: streamtelemetry.StartedAtSourceFirstSeen, wantIssuedSrc: streamtelemetry.TokenIssuedAtSourceNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started, source, issued, issuedSource := TelemetryTokenTiming(test.claims)
			if !started.Equal(test.wantStarted) || source != test.wantSource || !issued.Equal(test.wantIssued) || issuedSource != test.wantIssuedSrc {
				t.Fatalf("timing = (%v, %q, %v, %q)", started, source, issued, issuedSource)
			}
		})
	}
}
