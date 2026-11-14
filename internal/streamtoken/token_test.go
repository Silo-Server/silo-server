package streamtoken

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// An empty HMAC key is public: anyone can sign a token naming any media path
// with it. A node whose loaded config lost its secret must refuse such tokens,
// and nothing may mint them.
func TestSignAndVerifyRefuseEmptySecret(t *testing.T) {
	for _, secret := range []string{"", " ", "\t\n"} {
		if _, err := Sign(Claims{MediaPath: "/etc/passwd"}, secret, time.Hour); !errors.Is(err, ErrNoSecret) {
			t.Errorf("Sign(secret=%q) error = %v, want ErrNoSecret", secret, err)
		}

		// Forge the token the way an attacker would, without Sign's guard.
		forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{
			MediaPath:  "/etc/passwd",
			PlayMethod: PlayMethodDownload,
			RegisteredClaims: jwt.RegisteredClaims{
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
		}).SignedString([]byte(secret))
		if err != nil {
			t.Fatalf("forge token with secret %q: %v", secret, err)
		}
		if claims, err := Verify(forged, secret); !errors.Is(err, ErrNoSecret) {
			t.Errorf("Verify(secret=%q) error = %v (accepted: %t), want ErrNoSecret", secret, err, claims != nil)
		}
	}
}

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

// Stream tokens sit in URLs clients hold, and anyone can read a JWT's claims,
// so the media path must not appear in them. A token signed before sealing
// still carries the path in the clear and must keep verifying.
func TestMediaPathIsSealed(t *testing.T) {
	const secret, path = "test-secret", "/media/Movie (2023)/Movie.2023.2160p.WEB-DL-GROUP.mkv"
	token, err := Sign(Claims{SessionID: "s1", MediaPath: path}, secret, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil || strings.Contains(string(payload), "Movie") {
		t.Fatalf("token payload exposes the path: %s %v", payload, err)
	}
	if claims, err := Verify(token, secret); err != nil || claims.MediaPath != path || claims.SealedMediaPath != "" {
		t.Fatalf("Verify = %+v, %v; want the path back", claims, err)
	}
	if _, err := Verify(token, "other-secret"); err == nil {
		t.Fatal("Verify with another secret succeeded")
	}

	legacy, err := jwt.NewWithClaims(jwt.SigningMethodHS256, Claims{SessionID: "s1", MediaPath: path,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := Verify(legacy, secret); err != nil || claims.MediaPath != path {
		t.Fatalf("legacy Verify = %+v, %v", claims, err)
	}
}
