package appkeys

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"time"
)

// Envelope is a signed message: a payload and the signature over it.
type Envelope struct {
	// KeyID is the ID of the key that made Sig.
	KeyID string `json:"kid"`
	// Payload is base64url of the UTF-8 JSON payload, exactly as sent.
	Payload string `json:"payload"`
	// Sig is base64url of the 64-byte r || s signature.
	Sig string `json:"sig"`
	// DeviceSig is the device key's signature over the same bytes, on the
	// purposes that enroll a device key; nil elsewhere.
	DeviceSig *string `json:"device_sig,omitempty"`
}

// ParseEnvelope decodes an envelope that has exactly its fields, each a
// string, and checks its shape for the purpose (CheckShape).
func ParseEnvelope(data []byte, purpose Purpose) (Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Envelope{}, fail(CodeEnvelopeMalformed)
	}
	env, ok := envelopeFromFields(fields)
	if !ok {
		return Envelope{}, fail(CodeEnvelopeMalformed)
	}
	return env, env.CheckShape(purpose)
}

// envelopeFromFields reads an envelope from its decoded JSON fields. It
// refuses a field that is not a string and any field the envelope lacks.
func envelopeFromFields(fields map[string]json.RawMessage) (Envelope, bool) {
	var env Envelope
	for name, raw := range fields {
		var target *string
		switch name {
		case "kid":
			target = &env.KeyID
		case "payload":
			target = &env.Payload
		case "sig":
			target = &env.Sig
		case "device_sig":
			env.DeviceSig = new(string)
			target = env.DeviceSig
		default:
			return Envelope{}, false
		}
		if !decodeJSONString(raw, target) {
			return Envelope{}, false
		}
	}
	_, hasKid := fields["kid"]
	_, hasPayload := fields["payload"]
	_, hasSig := fields["sig"]
	return env, hasKid && hasPayload && hasSig
}

// decodeJSONString decodes raw into s when it is a JSON string.
func decodeJSONString(raw json.RawMessage, s *string) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return false
	}
	return json.Unmarshal(raw, s) == nil
}

// CheckShape refuses an envelope whose device_sig does not match its
// purpose: required where a device key is enrolled, refused everywhere else.
func (e Envelope) CheckShape(purpose Purpose) error {
	if !purpose.known() || (e.DeviceSig != nil) != purpose.enrollsDeviceKey() {
		return fail(CodeEnvelopeMalformed)
	}
	return nil
}

// signingInput is the ASCII bytes both signatures cover.
func (e Envelope) signingInput(purpose Purpose) []byte {
	return []byte(signingPrefix + string(purpose) + "." + e.Payload)
}

// VerifySignature checks Sig by key over the purpose's signing input, then
// that KeyID is key's ID, and returns the decoded payload bytes. It does not
// look inside the payload.
func (e Envelope) VerifySignature(purpose Purpose, key PublicKey) ([]byte, error) {
	if !verifyRawSignature(key, e.signingInput(purpose), e.Sig) {
		return nil, fail(CodeSignatureInvalid)
	}
	if e.KeyID != key.KeyID() {
		return nil, fail(CodeKidMismatch)
	}
	payload, err := encoding.DecodeString(e.Payload)
	if err != nil {
		return nil, fail(CodePayloadMalformed)
	}
	return payload, nil
}

// verifyRawSignature checks a base64url r || s ECDSA P-256 signature over
// SHA-256 of msg. Any valid (r, s) passes, a high s included.
func verifyRawSignature(key PublicKey, msg []byte, encodedSig string) bool {
	sig, err := encoding.DecodeString(encodedSig)
	if err != nil || len(sig) != SignatureSize {
		return false
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), key.raw[:])
	if err != nil {
		return false
	}
	digest := sha256.Sum256(msg)
	r := new(big.Int).SetBytes(sig[:SignatureSize/2])
	s := new(big.Int).SetBytes(sig[SignatureSize/2:])
	return ecdsa.Verify(pub, digest[:], r, s)
}

// Verify runs every check of a signed message for one purpose, in the
// protocol's order: the shape, sig and kid by key, the payload's form and
// purpose, device_sig by the payload's device_key, every field, the server
// and the times.
//
// The returned payload is non-nil whenever the signature verified, even
// when a later check refused it, so a route can spend the challenge the
// signer committed to (Payload.Challenge may still be nil when that field is
// malformed). grant-redeem has its own order; use VerifyGrantRedeem.
func Verify(env Envelope, purpose Purpose, key PublicKey, serverID string, now time.Time) (*Payload, error) {
	if purpose == PurposeGrantRedeem {
		return nil, fail(CodeEnvelopeMalformed)
	}
	if err := env.CheckShape(purpose); err != nil {
		return nil, err
	}
	raw, err := env.VerifySignature(purpose, key)
	if err != nil {
		return nil, err
	}
	p := &Payload{}
	if err := p.parse(raw, purpose); err != nil {
		return p, err
	}
	if purpose.enrollsDeviceKey() {
		if err := p.checkDeviceSig(env, purpose); err != nil {
			return p, err
		}
	}
	if err := p.parseFields(purpose); err != nil {
		return p, err
	}
	err = p.CheckClaims(serverID, now)
	return p, err
}

// CheckClaims refuses a payload naming another server or outside its times.
func (p *Payload) CheckClaims(serverID string, now time.Time) error {
	if p.ServerID != serverID {
		return fail(CodeServerMismatch)
	}
	return checkTimes(p.IssuedAt, p.ExpiresAt, now)
}

func checkTimes(iat, exp int64, now time.Time) error {
	lifetime := exp - iat
	if lifetime <= 0 || lifetime > int64(MaxMessageLifetime/time.Second) {
		return fail(CodeLifetimeTooLong)
	}
	clock := now.Unix()
	if clock >= exp {
		return fail(CodeExpired)
	}
	if iat > clock+int64(MaxClockSkew/time.Second) {
		return fail(CodeNotYetValid)
	}
	return nil
}

// checkDeviceSig verifies device_sig by the payload's device_key over the
// same bytes the outer signature covers.
func (p *Payload) checkDeviceSig(env Envelope, purpose Purpose) error {
	key, err := p.publicKeyField("device_key")
	if err != nil {
		return err
	}
	p.DeviceKey = key
	if env.DeviceSig == nil || !verifyRawSignature(*key, env.signingInput(purpose), *env.DeviceSig) {
		return fail(CodeDeviceSignatureInvalid)
	}
	return nil
}

// KeyBody is the body of the routes where the signing person key travels
// beside its envelope: enroll and app-code redeem.
type KeyBody struct {
	PersonKey string   `json:"person_key"`
	Message   Envelope `json:"message"`
}

// ParseKeyBody decodes a body of exactly person_key and message and checks
// the message's shape for the purpose.
func ParseKeyBody(data []byte, purpose Purpose) (PublicKey, Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || len(fields) != 2 {
		return PublicKey{}, Envelope{}, fail(CodeBodyMalformed)
	}
	var encodedKey string
	keyRaw, hasKey := fields["person_key"]
	msgRaw, hasMsg := fields["message"]
	if !hasKey || !hasMsg || !decodeJSONString(keyRaw, &encodedKey) {
		return PublicKey{}, Envelope{}, fail(CodeBodyMalformed)
	}
	key, err := ParsePublicKey(encodedKey)
	if err != nil {
		return PublicKey{}, Envelope{}, fail(CodeBodyMalformed)
	}
	env, err := ParseEnvelope(msgRaw, purpose)
	return key, env, err
}

// decoyKey is a fixed public key nobody signs with: the P-256 base point.
var decoyKey = func() PublicKey {
	raw, err := hex.DecodeString("04" +
		"6b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c296" +
		"4fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5")
	if err != nil {
		panic(err)
	}
	key, err := PublicKeyFromBytes(raw)
	if err != nil {
		panic(err)
	}
	return key
}()

// DecoyVerify runs one signature check of env against a key nobody holds. A
// route calls it when the envelope names a key it doesn't hold, so that
// refusal takes about as long as a known key's failed signature.
func DecoyVerify(env Envelope, purpose Purpose) {
	_ = verifyRawSignature(decoyKey, env.signingInput(purpose), env.Sig)
}
