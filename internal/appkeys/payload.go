package appkeys

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"unicode/utf8"
)

// Payload is a verified message's payload. Which fields are set depends on
// the purpose; fields the protocol doesn't name are ignored.
type Payload struct {
	Purpose   Purpose
	ServerID  string
	IssuedAt  int64
	ExpiresAt int64

	// Challenge is the raw 32-byte challenge, on every purpose but grant.
	Challenge []byte
	// Origin is the canonical origin the device is talking to.
	Origin   string
	ReplyKey *PublicKey

	// DeviceKey, DeviceName and DevicePlatform describe the device key an
	// introduce, enroll or redeem enrolls (and, optionally, a grant-redeem).
	DeviceKey      *PublicKey
	DeviceName     string
	DevicePlatform string
	// HasDeviceName is false when a grant-redeem left device_name out.
	HasDeviceName bool

	// Code is a redeem's app code.
	Code string

	// Grant fields.
	GrantID   []byte
	AccountID string
	Origins   []string
	AppKey    *PublicKey
	AppName   string

	// Grant is the grant envelope inside a grant-redeem.
	Grant *Envelope

	// Set is a profile-set's set, verbatim; SetVersion is its version.
	Set        json.RawMessage
	SetVersion int64
	// Pairs is a profile-set's optional pairs, verbatim.
	Pairs json.RawMessage

	fields map[string]json.RawMessage
}

// parse decodes the payload as one UTF-8 JSON object and checks its purpose.
// A key repeated in the object takes its last value; keys match exactly, by
// case.
func (p *Payload) parse(raw []byte, purpose Purpose) error {
	if !utf8.Valid(raw) {
		return fail(CodePayloadMalformed)
	}
	if err := json.Unmarshal(raw, &p.fields); err != nil || p.fields == nil {
		return fail(CodePayloadMalformed)
	}
	var named string
	if !decodeJSONString(p.fields["purpose"], &named) {
		return fail(CodeFieldInvalid)
	}
	if Purpose(named) != purpose {
		return fail(CodePurposeMismatch)
	}
	p.Purpose = purpose
	return nil
}

// parseFields reads and checks every field the purpose carries.
func (p *Payload) parseFields(purpose Purpose) error {
	if err := p.parseCommon(); err != nil {
		return err
	}
	switch purpose {
	case PurposeIntroduce, PurposeEnroll, PurposeRedeem:
		if err := p.parseExchange(); err != nil {
			return err
		}
		if err := p.parseDevice(true); err != nil {
			return err
		}
		if purpose == PurposeRedeem {
			code, err := p.bytesField("code", AppCodeSize)
			if err != nil {
				return err
			}
			p.Code = encoding.EncodeToString(code)
		}
		return nil
	case PurposeSignIn:
		return p.parseExchange()
	case PurposeGrant:
		return p.parseGrant()
	case PurposeGrantRedeem:
		if err := p.parseExchange(); err != nil {
			return err
		}
		if err := p.parseDevice(false); err != nil {
			return err
		}
		return p.parseInnerGrant()
	case PurposeProfileSet:
		return p.parseProfileSet()
	}
	return fail(CodeFieldInvalid)
}

func (p *Payload) parseCommon() error {
	if !decodeJSONString(p.fields["server_id"], &p.ServerID) || p.ServerID == "" {
		return fail(CodeFieldInvalid)
	}
	var err error
	if p.IssuedAt, err = p.wholeNumberField("iat"); err != nil {
		return err
	}
	p.ExpiresAt, err = p.wholeNumberField("exp")
	return err
}

// parseExchange reads the fields of a request that the server answers:
// challenge, origin and reply_key.
func (p *Payload) parseExchange() error {
	var err error
	if p.Challenge, err = p.bytesField("challenge", ChallengeSize); err != nil {
		return err
	}
	if p.Origin, err = p.originField("origin"); err != nil {
		return err
	}
	p.ReplyKey, err = p.publicKeyField("reply_key")
	return err
}

// parseDevice reads device_key, device_name and device_platform. On a
// grant-redeem the key is the grant's and the name and platform are
// optional.
func (p *Payload) parseDevice(required bool) error {
	if required && p.DeviceKey == nil {
		key, err := p.publicKeyField("device_key")
		if err != nil {
			return err
		}
		p.DeviceKey = key
	}
	name, hasName, err := p.textField("device_name", MaxDeviceNameLength, required)
	if err != nil {
		return err
	}
	platform, _, err := p.textField("device_platform", MaxDevicePlatformLength, required)
	if err != nil {
		return err
	}
	p.DeviceName, p.HasDeviceName, p.DevicePlatform = name, hasName, platform
	return nil
}

func (p *Payload) parseGrant() error {
	var err error
	if p.GrantID, err = p.bytesField("grant_id", GrantIDSize); err != nil {
		return err
	}
	if !decodeJSONString(p.fields["account_id"], &p.AccountID) || p.AccountID == "" {
		return fail(CodeFieldInvalid)
	}
	var origins []json.RawMessage
	if err := json.Unmarshal(p.fields["origins"], &origins); err != nil || len(origins) == 0 {
		return fail(CodeFieldInvalid)
	}
	for _, raw := range origins {
		var origin string
		if !decodeJSONString(raw, &origin) || !isCanonicalOrigin(origin) {
			return fail(CodeFieldInvalid)
		}
		p.Origins = append(p.Origins, origin)
	}
	if p.AppKey, err = p.publicKeyField("app_key"); err != nil {
		return err
	}
	p.AppName, _, err = p.textField("app_name", MaxAppNameLength, true)
	return err
}

func (p *Payload) parseInnerGrant() error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(p.fields["grant"], &fields); err != nil || fields == nil {
		return fail(CodeFieldInvalid)
	}
	env, ok := envelopeFromFields(fields)
	if !ok || env.CheckShape(PurposeGrant) != nil {
		return fail(CodeFieldInvalid)
	}
	p.Grant = &env
	return nil
}

func (p *Payload) parseProfileSet() error {
	var err error
	if p.Challenge, err = p.bytesField("challenge", ChallengeSize); err != nil {
		return err
	}
	if !decodeJSONString(p.fields["account_id"], &p.AccountID) || p.AccountID == "" {
		return fail(CodeFieldInvalid)
	}
	var set map[string]json.RawMessage
	if err := json.Unmarshal(p.fields["set"], &set); err != nil || set == nil {
		return fail(CodeFieldInvalid)
	}
	if p.SetVersion, err = wholeNumber(set["version"]); err != nil {
		return err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(set["items"], &items); err != nil || items == nil {
		return fail(CodeFieldInvalid)
	}
	p.Set = p.fields["set"]
	if raw, ok := p.fields["pairs"]; ok {
		var pairs []json.RawMessage
		if err := json.Unmarshal(raw, &pairs); err != nil || pairs == nil {
			return fail(CodeFieldInvalid)
		}
		p.Pairs = raw
	}
	return nil
}

func (p *Payload) wholeNumberField(name string) (int64, error) {
	return wholeNumber(p.fields[name])
}

// wholeNumber reads a JSON number as an IEEE 754 double, as a browser does,
// and accepts it when that double is a whole number from 0 to 2^53 - 1.
// 1795578600.0 and 1.7955786e9 pass; 1795578600.5 does not.
func wholeNumber(raw json.RawMessage) (int64, error) {
	raw = bytes.TrimSpace(raw)
	var n json.Number
	if len(raw) == 0 || raw[0] == '"' || json.Unmarshal(raw, &n) != nil {
		return 0, fail(CodeFieldInvalid)
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) || f != math.Trunc(f) || f < 0 || f > maxWholeNumber {
		return 0, fail(CodeFieldInvalid)
	}
	return int64(f), nil
}

// bytesField decodes a base64url field of exactly size bytes.
func (p *Payload) bytesField(name string, size int) ([]byte, error) {
	var s string
	if !decodeJSONString(p.fields[name], &s) {
		return nil, fail(CodeFieldInvalid)
	}
	b, err := encoding.DecodeString(s)
	if err != nil || len(b) != size {
		return nil, fail(CodeFieldInvalid)
	}
	return b, nil
}

func (p *Payload) publicKeyField(name string) (*PublicKey, error) {
	var s string
	if !decodeJSONString(p.fields[name], &s) {
		return nil, fail(CodeFieldInvalid)
	}
	key, err := ParsePublicKey(s)
	if err != nil {
		return nil, fail(CodeFieldInvalid)
	}
	return &key, nil
}

// originField reads an origin that is already in its canonical form.
func (p *Payload) originField(name string) (string, error) {
	var s string
	if !decodeJSONString(p.fields[name], &s) || !isCanonicalOrigin(s) {
		return "", fail(CodeFieldInvalid)
	}
	return s, nil
}

// textField reads a string of at most limit characters. A missing optional
// field reports present false.
func (p *Payload) textField(name string, limit int, required bool) (string, bool, error) {
	raw, ok := p.fields[name]
	if !ok {
		if required {
			return "", false, fail(CodeFieldInvalid)
		}
		return "", false, nil
	}
	var s string
	if !decodeJSONString(raw, &s) || utf8.RuneCountInString(s) > limit {
		return "", false, fail(CodeFieldInvalid)
	}
	return s, true, nil
}
