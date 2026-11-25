package appkeys

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// A key the server stores must be a real P-256 point: a value off the curve
// or of the wrong length can never verify a signature, and accepting it would
// enroll a key nobody holds.
func TestParsePublicKey(t *testing.T) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := priv.PublicKey().Bytes()
	offCurve := append([]byte(nil), valid...)
	offCurve[PublicKeySize-1] ^= 0x01
	enc := base64.RawURLEncoding.EncodeToString
	for _, tc := range []struct {
		name    string
		encoded string
		ok      bool
	}{
		{name: "valid point", encoded: enc(valid), ok: true},
		{name: "point not on the curve", encoded: enc(offCurve)},
		{name: "compressed length", encoded: enc(valid[:33])},
		{name: "one byte too long", encoded: enc(append(append([]byte(nil), valid...), 0))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := ParsePublicKey(tc.encoded)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && (key.String() != tc.encoded || len(key.KeyID()) != 22) {
				t.Fatalf("round trip %q, key id %q", key.String(), key.KeyID())
			}
		})
	}
}
