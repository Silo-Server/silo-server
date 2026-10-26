package access

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const profileTokenTestSecret = "profile-token-test-secret-0123456789"

func TestProfileTokenRoundTripsPINRevision(t *testing.T) {
	svc := NewProfileTokenService(profileTokenTestSecret, time.Minute)
	token, _, err := svc.Mint(ProfileTokenClaims{UserID: 1, SessionID: "s1", ProfileID: "p1", PINRevision: 4, PolicyRevision: 9})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := svc.Validate(token)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if claims.PINRevision != 4 || claims.PolicyRevision != 9 || claims.ProfileID != "p1" {
		t.Fatalf("claims = %+v", claims)
	}

	// A node from before pin_revision compares policy_revision, so the claim
	// stays on the wire, as does pin_revision at 0.
	token, _, err = svc.Mint(ProfileTokenClaims{UserID: 1, SessionID: "s1", ProfileID: "p1", PolicyRevision: 3})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeJWTPayload(t, token)
	if string(payload["pin_revision"]) != "0" || string(payload["policy_revision"]) != "3" {
		t.Fatalf("payload = %v", payload)
	}
}

// A token minted before tokens carried pin_revision cannot show whether the
// PIN changed since, so it is refused rather than read as revision 0.
func TestProfileTokenWithoutPINRevisionIsRefused(t *testing.T) {
	svc := NewProfileTokenService(profileTokenTestSecret, 0)
	legacy := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":         1,
		"session_id":      "s1",
		"profile_id":      "p1",
		"policy_revision": 0,
		"iat":             time.Now().Unix(),
	})
	token, err := legacy.SignedString([]byte(profileTokenTestSecret))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Validate(token); !errors.Is(err, ErrProfileUnverified) {
		t.Fatalf("legacy token err = %v, want ErrProfileUnverified", err)
	}
	profile := &userstore.Profile{ID: "p1", PINHash: "hash"}
	if err := CheckProfileToken(svc, token, 1, "s1", profile); !errors.Is(err, ErrProfileUnverified) {
		t.Fatalf("CheckProfileToken(legacy) = %v, want ErrProfileUnverified", err)
	}
}

func TestCheckProfileTokenBindsToProfilePINRevision(t *testing.T) {
	svc := NewProfileTokenService(profileTokenTestSecret, 0)
	profile := &userstore.Profile{ID: "parent", PINHash: "hash", PINRevision: 2}
	mint := func(c ProfileTokenClaims) string {
		t.Helper()
		token, _, err := svc.Mint(c)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	good := ProfileTokenClaims{UserID: 1, SessionID: "s1", ProfileID: "parent", PINRevision: 2, PolicyRevision: 1}

	if err := CheckProfileToken(svc, mint(good), 1, "s1", profile); err != nil {
		t.Fatalf("matching token refused: %v", err)
	}
	// The account revision is not part of the proof any more.
	stale := good
	stale.PolicyRevision = 0
	if err := CheckProfileToken(svc, mint(stale), 1, "s1", profile); err != nil {
		t.Fatalf("token with an older account revision refused: %v", err)
	}

	for name, mutate := range map[string]func(*ProfileTokenClaims){
		"older pin revision": func(c *ProfileTokenClaims) { c.PINRevision = 1 },
		"other account":      func(c *ProfileTokenClaims) { c.UserID = 2 },
		"other session":      func(c *ProfileTokenClaims) { c.SessionID = "s2" },
		"other profile":      func(c *ProfileTokenClaims) { c.ProfileID = "kid" },
	} {
		c := good
		mutate(&c)
		if err := CheckProfileToken(svc, mint(c), 1, "s1", profile); !errors.Is(err, ErrProfileUnverified) {
			t.Errorf("%s: err = %v, want ErrProfileUnverified", name, err)
		}
	}
	if err := CheckProfileToken(nil, mint(good), 1, "s1", profile); !errors.Is(err, ErrProfileUnverified) {
		t.Errorf("nil validator: err = %v, want ErrProfileUnverified", err)
	}
	if err := CheckProfileToken(svc, "", 1, "s1", profile); !errors.Is(err, ErrProfileUnverified) {
		t.Errorf("empty token: err = %v, want ErrProfileUnverified", err)
	}
}

func decodeJWTPayload(t *testing.T, token string) map[string]json.RawMessage {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
