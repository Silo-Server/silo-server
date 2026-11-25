package appkeys

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// caseFields are the fields each case type may carry: the ones its handler
// reads. A refreshed file that adds a field fails here, as a new case type
// does, rather than passing with the field unchecked.
var caseFields = map[string][]string{
	"signed-message": {"type", "name", "valid", "reason", "error", "purpose", "now", "server_id", "signer_private_key",
		"signer_public_key", "device_private_key", "device_public_key", "payload_json", "payload", "signing_input", "sig",
		"device_sig", "envelope", "body", "verify_key", "s_form", "grant_signer_private_key", "grant_signer_public_key"},
	"sealed-reply": {"type", "name", "valid", "reason", "error", "challenge", "reply_private_key", "reply_public_key",
		"ephemeral_private_key", "ephemeral_public_key", "z", "kdf_input", "key", "nonce", "plaintext_json", "sealed"},
	"key":            {"type", "name", "valid", "private_key", "public_key", "kid"},
	"token-hash":     {"type", "name", "valid", "token", "token_hash"},
	"public-key":     {"type", "name", "valid", "reason", "error", "public_key"},
	"origin":         {"type", "name", "valid", "reason", "error", "input", "origin"},
	"server-address": {"type", "name", "valid", "reason", "error", "input", "address"},
	"join-link":      {"type", "name", "valid", "reason", "error", "form", "string", "address", "app_code"},
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
			for field := range m {
				if !slices.Contains(caseFields[typ], field) {
					t.Fatalf("case field %q is not read by this test", field)
				}
			}
			handle(vectorCase{t: t, m: m})
		})
	}
}

// privateOf checks that a case's private key derives its public key.
func (c vectorCase) privateOf(private, public string) {
	c.t.Helper()
	if !c.has(private) {
		return
	}
	priv, err := ecdh.P256().NewPrivateKey(c.bytes(private))
	if err != nil {
		c.t.Fatal(err)
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), c.key(public).Bytes()) {
		c.t.Fatalf("%s does not derive %s", private, public)
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
		// Every refusal of a signed message is a code this package
		// answers, compared by name: a code renamed here without the
		// vectors, or the reverse, fails instead of passing on any refusal.
		if want := Code(c.str("error")); !signedMessageCodes[want] {
			c.t.Fatalf("the case's error %q is not a code this package answers", want)
		}
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
	checkSignedMessageInputs(c)
}

// checkSignedMessageInputs reads the inputs a valid case states beside its
// envelope: the keys that made it, the payload JSON it encodes, and the form
// of its s.
func checkSignedMessageInputs(c vectorCase) {
	c.privateOf("signer_private_key", "signer_public_key")
	c.privateOf("device_private_key", "device_public_key")
	c.privateOf("grant_signer_private_key", "grant_signer_public_key")
	if c.has("payload_json") {
		if decoded, err := encoding.DecodeString(c.str("payload")); err != nil || string(decoded) != c.str("payload_json") {
			c.t.Fatalf("payload does not encode payload_json")
		}
	}
	for _, field := range []string{"sig", "device_sig"} {
		if c.has(field) && c.str(field) == "" {
			c.t.Fatalf("empty %s", field)
		}
	}
	if c.has("s_form") {
		sig := c.bytes("sig")
		half := new(big.Int).Rsh(elliptic.P256().Params().N, 1)
		high := new(big.Int).SetBytes(sig[32:]).Cmp(half) > 0
		if high != (c.str("s_form") == "high") {
			c.t.Fatalf("s is not %s", c.str("s_form"))
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
	c.privateOf("reply_private_key", "reply_public_key")
	c.privateOf("ephemeral_private_key", "ephemeral_public_key")
	recipient, err := ecdh.P256().NewPublicKey(c.bytes("reply_public_key"))
	if err != nil {
		c.t.Fatal(err)
	}
	z, err := ephemeral.ECDH(recipient)
	if err != nil || !bytes.Equal(z, c.bytes("z")) {
		c.t.Fatalf("z differs: %v", err)
	}
	input := append(append(append([]byte(nil), z...), c.bytes("ephemeral_public_key")...), c.bytes("reply_public_key")...)
	if !bytes.Equal(input, c.bytes("kdf_input")) {
		c.t.Fatal("kdf_input differs")
	}
	key, err := hkdf.Key(sha256.New, input, challenge, ReplyLabel, sealKeySize)
	if err != nil || !bytes.Equal(key, c.bytes("key")) {
		c.t.Fatalf("key differs: %v", err)
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
			c.expectRefusal(err, map[Code]bool{CodeNotAPoint: true})
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

// Host spellings the frozen vectors don't pin yet. A trailing dot names the
// same host as the name without it, so an address written with one has a
// second spelling. Only the last label decides whether a host is a number:
// all digits, or 0x and zero or more hex digits, as URL parsers read it; a
// LAN name such as 0xroom stays a name. A label is 1 to 63 characters from
// letters, digits, '-' and '_', in any position.
func TestAddressSpellings(t *testing.T) {
	for _, tc := range []struct {
		input string
		ok    bool
	}{
		{input: "https://media.example.com."},
		{input: "https://media.example.com./silo"},
		{input: "http://192.168.1.20.:8080"},
		{input: "http://0xroom:52830", ok: true},
		{input: "http://0x1f:52830"},
		{input: "http://0x:52830"},
		{input: "http://0x1f.example:52830", ok: true},
		{input: "http://room.0x1f:52830"},
		{input: "http://0X1F:52830"},
		{input: "http://my_nas:8096", ok: true},
		{input: "http://-room:8096", ok: true},
		{input: "http://room-:8096", ok: true},
		{input: "http://_:8096", ok: true},
		{input: "http://" + strings.Repeat("a", 63) + ":8096", ok: true},
		{input: "http://" + strings.Repeat("a", 64) + ":8096"},
		{input: "http://a..b:8096"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			_, err := CanonicalOrigin(tc.input)
			if (err == nil) != tc.ok || (!tc.ok && CodeOf(err) != CodeNotAnAddress) {
				t.Fatalf("CanonicalOrigin = %v, want ok %v", err, tc.ok)
			}
			if _, err := CanonicalServerAddress(tc.input); (err == nil) != tc.ok {
				t.Fatalf("CanonicalServerAddress = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

// Go's strict base64url decoder skips CR and LF. A payload with a line break
// in it, signed exactly as sent, would otherwise verify and decode as if the
// break weren't there: one payload, two spellings. It is refused, and so is
// any other character outside the alphabet.
func TestPayloadRefusesCharactersOutsideBase64url(t *testing.T) {
	var signIn vectorCase
	for _, m := range loadVectors(t, "signed-messages.json").Cases {
		if m["name"] == "sign-in" {
			signIn = vectorCase{t: t, m: m}
		}
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), signIn.bytes("signer_private_key"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(int64(signIn.m["now"].(float64)), 0)
	payload := signIn.str("payload")
	for _, tc := range []struct{ name, payload string }{
		{name: "line feed", payload: payload[:20] + "\n" + payload[20:]},
		{name: "carriage return", payload: payload[:20] + "\r" + payload[20:]},
		{name: "standard alphabet", payload: payload[:20] + "+" + payload[21:]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := Envelope{KeyID: signIn.key("signer_public_key").KeyID(), Payload: tc.payload}
			digest := sha256.Sum256(env.signingInput(PurposeSignIn))
			r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
			if err != nil {
				t.Fatal(err)
			}
			sig := make([]byte, SignatureSize)
			r.FillBytes(sig[:32])
			s.FillBytes(sig[32:])
			env.Sig = encoding.EncodeToString(sig)
			if _, err := Verify(env, PurposeSignIn, signIn.key("signer_public_key"), signIn.str("server_id"), now); CodeOf(err) != CodePayloadMalformed {
				t.Fatalf("Verify = %v, want payload_malformed", err)
			}
		})
	}
}
