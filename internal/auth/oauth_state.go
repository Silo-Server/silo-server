package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeriveOAuthStateSecret produces a stable per-server secret for signing
// OAuth `state` payloads. Derived from the JWT secret so we don't add a
// new required config knob; rotating JWT_SECRET invalidates in-flight
// OAuth flows, which is acceptable (state TTL is 10 minutes).
func DeriveOAuthStateSecret(jwtSecret []byte) []byte {
	h := hmac.New(sha256.New, jwtSecret)
	h.Write([]byte("silo/oauth-state-v1"))
	return h.Sum(nil)
}

// StatePayload is what we sign into the OAuth `state` parameter.
// The payload round-trips through the upstream IdP (WHMCS, Authentik, etc.)
// as the literal `state` query parameter — verification on /callback both
// authenticates the request as ours and recovers install_id + expiry.
type StatePayload struct {
	Nonce     string    `json:"n"`
	InstallID string    `json:"i"`
	ExpiresAt time.Time `json:"e"`
}

// SignState returns a self-contained signed state of the form
// "<base64url(json(payload))>.<base64url(hmac_sha256)>".
func SignState(secret []byte, p StatePayload) string {
	body, _ := json.Marshal(p)
	bodyB64 := base64.RawURLEncoding.EncodeToString(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(bodyB64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return bodyB64 + "." + sig
}

// ErrStateExpired is a state whose signature verified but whose expiry has
// passed. VerifyState returns the payload with it, so the callback can still
// send the flow's own client its session_expired failure.
var ErrStateExpired = errors.New("state expired")

// VerifyState parses, verifies the HMAC, checks expiry, and returns the
// payload. An expired but authentic state returns the payload together
// with ErrStateExpired.
func VerifyState(secret []byte, signed string) (StatePayload, error) {
	parts := strings.SplitN(signed, ".", 2)
	if len(parts) != 2 {
		return StatePayload{}, errors.New("malformed state")
	}
	bodyB64, sigB64 := parts[0], parts[1]

	expectedMac := hmac.New(sha256.New, secret)
	expectedMac.Write([]byte(bodyB64))
	expected := base64.RawURLEncoding.EncodeToString(expectedMac.Sum(nil))

	if !hmac.Equal([]byte(sigB64), []byte(expected)) {
		return StatePayload{}, errors.New("state signature mismatch")
	}

	body, err := base64.RawURLEncoding.DecodeString(bodyB64)
	if err != nil {
		return StatePayload{}, fmt.Errorf("decode state body: %w", err)
	}
	var p StatePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return StatePayload{}, fmt.Errorf("unmarshal state body: %w", err)
	}
	if time.Now().After(p.ExpiresAt) {
		return p, ErrStateExpired
	}
	return p, nil
}
