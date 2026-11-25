package appkeys

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The protocol's test vectors, copied unchanged by scripts/sync-app-key-vectors.
// Every client implements the same protocol from them, so a server that
// drifts from any case stops talking to those clients. Each valid case must
// verify or be reproduced byte for byte, each invalid one must be refused,
// and a case type this test doesn't read fails it, so a refreshed file can't
// skip cases silently.

type vectorFile struct {
	Protocol string           `json:"protocol"`
	Cases    []map[string]any `json:"cases"`
}

func loadVectors(t *testing.T, name string) vectorFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "vectors", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if f.Protocol != Protocol || len(f.Cases) == 0 {
		t.Fatalf("%s: protocol %q with %d cases", name, f.Protocol, len(f.Cases))
	}
	return f
}

// vectorCase reads typed fields from one case.
type vectorCase struct {
	t *testing.T
	m map[string]any
}

func (c vectorCase) str(name string) string {
	c.t.Helper()
	s, ok := c.m[name].(string)
	if !ok {
		c.t.Fatalf("case field %q is not a string: %v", name, c.m[name])
	}
	return s
}

func (c vectorCase) bytes(name string) []byte {
	c.t.Helper()
	b, err := encoding.DecodeString(c.str(name))
	if err != nil {
		c.t.Fatalf("case field %q: %v", name, err)
	}
	return b
}

func (c vectorCase) raw(name string) []byte {
	c.t.Helper()
	v, ok := c.m[name]
	if !ok {
		c.t.Fatalf("case has no %q", name)
	}
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

func (c vectorCase) key(name string) PublicKey {
	c.t.Helper()
	k, err := ParsePublicKey(c.str(name))
	if err != nil {
		c.t.Fatalf("case key %q: %v", name, err)
	}
	return k
}

func (c vectorCase) valid() bool { return c.m["valid"] == true }

func (c vectorCase) has(name string) bool { _, ok := c.m[name]; return ok }

// expectRefusal fails unless err refuses the case, with the case's code when
// it is one this package distinguishes.
func (c vectorCase) expectRefusal(err error, known map[Code]bool) {
	c.t.Helper()
	if err == nil {
		c.t.Fatalf("accepted; want %v (%v)", c.m["error"], c.m["reason"])
	}
	want := Code(c.str("error"))
	if known[want] && CodeOf(err) != want {
		c.t.Fatalf("refused with %v, want %s (%v)", err, want, c.m["reason"])
	}
}

func runVectorCases(t *testing.T, file string, handlers map[string]func(vectorCase), skip map[string]string) {
	for _, m := range loadVectors(t, file).Cases {
		typ, _ := m["type"].(string)
		name, _ := m["name"].(string)
		t.Run(typ+"/"+name, func(t *testing.T) {
			if why, ok := skip[typ]; ok {
				t.Skip(why)
			}
			handle, ok := handlers[typ]
			if !ok {
				t.Fatalf("case type %q is not read by this test", typ)
			}
			handle(vectorCase{t: t, m: m})
		})
	}
}

var signedMessageCodes = map[Code]bool{
	CodeBodyMalformed: true, CodeEnvelopeMalformed: true, CodeSignatureInvalid: true, CodeKidMismatch: true,
	CodePayloadMalformed: true, CodePurposeMismatch: true, CodeDeviceSignatureInvalid: true, CodeFieldInvalid: true,
	CodeServerMismatch: true, CodeExpired: true, CodeNotYetValid: true, CodeLifetimeTooLong: true,
	CodeOriginNotGranted: true, CodeGrantInvalid: true, CodeGrantExpired: true,
}

func TestSignedMessageVectors(t *testing.T) {
	runVectorCases(t, "signed-messages.json", map[string]func(vectorCase){"signed-message": checkSignedMessage}, nil)
}

func checkSignedMessage(c vectorCase) {
	purpose := Purpose(c.str("purpose"))
	serverID := c.str("server_id")
	now := time.Unix(int64(c.m["now"].(float64)), 0)
	var payload *Payload
	var err error
	switch {
	case c.has("body"):
		var key PublicKey
		var env Envelope
		key, env, err = ParseKeyBody(c.raw("body"), purpose)
		if err == nil {
			payload, err = Verify(env, purpose, key, serverID, now)
		}
	case purpose == PurposeGrantRedeem:
		var env Envelope
		env, err = ParseEnvelope(c.raw("envelope"), purpose)
		if err == nil {
			signer := c.key("grant_signer_public_key")
			payload, _, err = VerifyGrantRedeem(env, serverID, now, func(string) (PublicKey, error) { return signer, nil })
		}
	default:
		var env Envelope
		env, err = ParseEnvelope(c.raw("envelope"), purpose)
		if err == nil {
			payload, err = Verify(env, purpose, c.key("verify_key"), serverID, now)
		}
	}
	if !c.valid() {
		c.expectRefusal(err, signedMessageCodes)
		return
	}
	if err != nil {
		c.t.Fatalf("refused: %v", err)
	}
	if c.has("payload_json") && payload.ServerID != serverID {
		c.t.Fatalf("payload server_id %q", payload.ServerID)
	}
	if c.has("device_public_key") && purpose.enrollsDeviceKey() && payload.DeviceKey.String() != c.str("device_public_key") {
		c.t.Fatalf("device key %s, want %s", payload.DeviceKey, c.str("device_public_key"))
	}
	if c.has("signing_input") {
		env := Envelope{Payload: c.str("payload")}
		if string(env.signingInput(purpose)) != c.str("signing_input") {
			c.t.Fatalf("signing input differs")
		}
	}
}

func TestSealedReplyVectors(t *testing.T) {
	runVectorCases(t, "sealed-reply.json", map[string]func(vectorCase){"sealed-reply": checkSealedReply}, nil)
}

func checkSealedReply(c vectorCase) {
	var sealed SealedReply
	if err := json.Unmarshal(c.raw("sealed"), &sealed); err != nil {
		c.t.Fatal(err)
	}
	challenge := c.bytes("challenge")
	opened, err := Open(c.bytes("reply_private_key"), challenge, sealed)
	if !c.valid() {
		c.expectRefusal(err, map[Code]bool{CodeOpenFailed: true})
		return
	}
	if err != nil || string(opened) != c.str("plaintext_json") {
		c.t.Fatalf("opened %q, %v", opened, err)
	}
	ephemeral, err := ecdh.P256().NewPrivateKey(c.bytes("ephemeral_private_key"))
	if err != nil {
		c.t.Fatal(err)
	}
	resealed, err := sealWith(ephemeral, c.bytes("nonce"), c.key("reply_public_key"), challenge, []byte(c.str("plaintext_json")))
	if err != nil {
		c.t.Fatal(err)
	}
	if resealed != sealed {
		c.t.Fatalf("sealed %+v, want %+v", resealed, sealed)
	}
}

func TestKeyVectors(t *testing.T) {
	runVectorCases(t, "keys.json", map[string]func(vectorCase){
		"key": func(c vectorCase) {
			priv, err := ecdh.P256().NewPrivateKey(c.bytes("private_key"))
			if err != nil {
				c.t.Fatal(err)
			}
			key := c.key("public_key")
			if !bytes.Equal(priv.PublicKey().Bytes(), key.Bytes()) || key.KeyID() != c.str("kid") {
				c.t.Fatalf("key ID %s, want %s", key.KeyID(), c.str("kid"))
			}
		},
		"token-hash": func(c vectorCase) {
			if got, err := TokenHash(c.str("token")); err != nil || got != c.str("token_hash") {
				c.t.Fatalf("token hash %q, %v", got, err)
			}
		},
		"public-key": func(c vectorCase) {
			_, err := ParsePublicKey(c.str("public_key"))
			c.expectRefusal(err, nil)
		},
	}, nil)
}

func TestLinkVectors(t *testing.T) {
	address := func(parse func(string) (string, error), field string) func(vectorCase) {
		return func(c vectorCase) {
			got, err := parse(c.str("input"))
			if !c.valid() {
				c.expectRefusal(err, map[Code]bool{CodeNotAnAddress: true})
				return
			}
			if err != nil || got != c.str(field) {
				c.t.Fatalf("got %q, %v; want %q", got, err, c.str(field))
			}
		}
	}
	runVectorCases(t, "links.json", map[string]func(vectorCase){
		"origin":         address(CanonicalOrigin, "origin"),
		"server-address": address(CanonicalServerAddress, "address"),
		"join-link": func(c vectorCase) {
			link, err := ParseJoinLink(c.str("string"))
			if !c.valid() {
				c.expectRefusal(err, map[Code]bool{
					CodeJoinPrefix: true, CodeJoinParts: true, CodeJoinVersion: true, CodeJoinAddress: true, CodeJoinAppCode: true,
				})
				return
			}
			want := JoinLink{Form: JoinLinkForm(c.str("form")), Address: c.str("address"), AppCode: c.str("app_code")}
			if err != nil || link != want || want.String() != c.str("string") {
				c.t.Fatalf("parsed %+v, %v; built %q", link, err, want.String())
			}
		},
	}, map[string]string{
		"pairing-qr": "pairing QR strings are read by apps, not this server",
		"allow-link": "Allow access links are read by apps, not this server",
	})
}

// A trailing dot names the same host as the name without it, so an address
// written with one has a second spelling.
func TestAddressRefusesTrailingDot(t *testing.T) {
	for _, input := range []string{"https://media.example.com.", "https://media.example.com./silo", "http://192.168.1.20.:8080"} {
		if _, err := CanonicalOrigin(input); CodeOf(err) != CodeNotAnAddress {
			t.Errorf("CanonicalOrigin(%q) = %v", input, err)
		}
		if _, err := CanonicalServerAddress(input); CodeOf(err) != CodeNotAnAddress {
			t.Errorf("CanonicalServerAddress(%q) = %v", input, err)
		}
	}
}
