// Package appkeystest signs app key messages the way an app does, for tests
// outside internal/auth that need a device to sign in by key.
package appkeystest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/appkeys"
)

// Key is a P-256 key pair a test signs with.
type Key struct {
	priv *ecdsa.PrivateKey
	pub  appkeys.PublicKey
}

// NewKey makes a fresh key.
func NewKey(t testing.TB) Key {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := priv.PublicKey.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := appkeys.PublicKeyFromBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return Key{priv: priv, pub: pub}
}

// Public is the key's public half.
func (k Key) Public() appkeys.PublicKey { return k.pub }

// Sign is the base64url r || s signature over the purpose's signing input
// for an encoded payload.
func (k Key) Sign(t testing.TB, purpose appkeys.Purpose, encodedPayload string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(appkeys.Protocol + "." + string(purpose) + "." + encodedPayload))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, appkeys.SignatureSize)
	r.FillBytes(sig[:appkeys.SignatureSize/2])
	s.FillBytes(sig[appkeys.SignatureSize/2:])
	return base64.RawURLEncoding.EncodeToString(sig)
}

// Envelope signs payload, with its purpose field set, as a message for
// purpose.
func (k Key) Envelope(t testing.TB, purpose appkeys.Purpose, payload map[string]any) appkeys.Envelope {
	t.Helper()
	fields := map[string]any{"purpose": string(purpose)}
	for name, value := range payload {
		fields[name] = value
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return appkeys.Envelope{KeyID: k.pub.KeyID(), Payload: encoded, Sig: k.Sign(t, purpose, encoded)}
}

// SignIn is a device key's sign-in message to serverID on challenge, issued
// at now.
func (k Key) SignIn(t testing.TB, serverID, challenge string, now time.Time) appkeys.Envelope {
	t.Helper()
	return k.Envelope(t, appkeys.PurposeSignIn, map[string]any{
		"server_id": serverID, "challenge": challenge,
		"iat": now.Unix() - 10, "exp": now.Unix() + 110,
		"origin": "https://media.example.test", "reply_key": NewKey(t).Public().String(),
	})
}

// Redeem is an app's redeem of code to serverID on challenge, issued at now:
// signed by k, the person key, with device's device_sig.
func (k Key) Redeem(t testing.TB, device Key, serverID, challenge, code string, now time.Time) appkeys.Envelope {
	t.Helper()
	env := k.Envelope(t, appkeys.PurposeRedeem, map[string]any{
		"server_id": serverID, "challenge": challenge,
		"iat": now.Unix() - 10, "exp": now.Unix() + 110,
		"origin": "https://media.example.test", "reply_key": NewKey(t).Public().String(),
		"device_key": device.Public().String(), "device_name": "Phone", "device_platform": "ios",
		"code": code,
	})
	deviceSig := device.Sign(t, appkeys.PurposeRedeem, env.Payload)
	env.DeviceSig = &deviceSig
	return env
}
