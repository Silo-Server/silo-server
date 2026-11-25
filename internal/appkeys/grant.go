package appkeys

import (
	"slices"
	"time"
)

// PersonKeyLookup finds the person key of the account a grant names.
type PersonKeyLookup func(accountID string) (PublicKey, error)

// VerifyGrantRedeem runs every check of a grant-redeem. The key that signs it
// is inside it, so the order differs from Verify: the payload is decoded to
// read the grant's app_key, the outer sig and kid are checked against that
// key, then the grant's own signature against the person key of the grant's
// account. server_id must be this server's in both.
//
// The redeem's own envelope is refused at its signature like any request's:
// signature_invalid when the outer signature fails under the grant's app
// key, kid_mismatch when that key signed it under another kid. Both are the
// one answer on the wire (signature_invalid), and neither returns a payload,
// so the challenge stays unspent. app_grant_invalid is for the grant inside,
// when it fails against the account's person key, and app_grant_expired only
// for the grant's own exp.
func VerifyGrantRedeem(env Envelope, serverID string, now time.Time, personKey PersonKeyLookup) (*Payload, *Payload, error) {
	if err := env.CheckShape(PurposeGrantRedeem); err != nil {
		return nil, nil, err
	}
	unverified, err := decodeB64(env.Payload)
	if err != nil {
		return nil, nil, fail(CodeSignatureInvalid)
	}
	outer := &Payload{}
	if err := outer.parse(unverified, PurposeGrantRedeem); err != nil {
		return nil, nil, fail(CodeSignatureInvalid)
	}
	if err := outer.parseInnerGrant(); err != nil {
		return nil, nil, fail(CodeSignatureInvalid)
	}
	grant, err := readUnverifiedGrant(*outer.Grant)
	if err != nil {
		return nil, nil, fail(CodeSignatureInvalid)
	}
	if !verifyRawSignature(*grant.AppKey, env.signingInput(PurposeGrantRedeem), env.Sig) {
		return nil, nil, fail(CodeSignatureInvalid)
	}
	if env.KeyID != grant.AppKey.KeyID() {
		return nil, nil, fail(CodeKidMismatch)
	}
	if err := outer.parseFields(PurposeGrantRedeem); err != nil {
		return outer, nil, err
	}
	if err := outer.CheckClaims(serverID, now); err != nil {
		return outer, nil, err
	}
	verified, err := verifyGrant(*outer.Grant, grant.AccountID, serverID, now, personKey)
	if err != nil {
		return outer, nil, err
	}
	if !slices.Contains(verified.Origins, outer.Origin) {
		return outer, verified, fail(CodeOriginNotGranted)
	}
	return outer, verified, nil
}

// readUnverifiedGrant decodes a grant's payload to find its app key and
// account, before anything about it is trusted.
func readUnverifiedGrant(env Envelope) (*Payload, error) {
	raw, err := decodeB64(env.Payload)
	if err != nil {
		return nil, fail(CodeGrantInvalid)
	}
	p := &Payload{}
	if err := p.parse(raw, PurposeGrant); err != nil {
		return nil, fail(CodeGrantInvalid)
	}
	if err := p.parseFields(PurposeGrant); err != nil {
		return nil, fail(CodeGrantInvalid)
	}
	return p, nil
}

// verifyGrant checks the grant's signature by the account's person key and
// its claims. Every refusal is app_grant_invalid except the grant's own expiry.
func verifyGrant(env Envelope, accountID, serverID string, now time.Time, personKey PersonKeyLookup) (*Payload, error) {
	key, err := personKey(accountID)
	if err != nil {
		return nil, fail(CodeGrantInvalid)
	}
	p, err := Verify(env, PurposeGrant, key, serverID, now)
	switch CodeOf(err) {
	case "":
		return p, nil
	case CodeExpired:
		return nil, fail(CodeGrantExpired)
	}
	return nil, fail(CodeGrantInvalid)
}
