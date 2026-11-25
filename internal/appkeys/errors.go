package appkeys

import "errors"

// Code names the check a signed message, a sealed reply or an address
// failed. A route maps codes to its problem types; the codes themselves are
// not on the wire.
type Code string

// Codes, in the order the verifier runs its checks.
const (
	// CodeBodyMalformed: a key body that is not exactly person_key and
	// message.
	CodeBodyMalformed Code = "body_malformed"
	// CodeEnvelopeMalformed: an envelope without exactly its fields, or with
	// device_sig on a purpose that has none, or without it on one that does.
	CodeEnvelopeMalformed Code = "envelope_malformed"
	// CodeSignatureInvalid: sig does not verify by the key.
	CodeSignatureInvalid Code = "signature_invalid"
	// CodeKidMismatch: kid is not the ID of the key that must have signed.
	CodeKidMismatch Code = "kid_mismatch"
	// CodePayloadMalformed: the signed payload is not one UTF-8 JSON object.
	CodePayloadMalformed Code = "payload_malformed"
	// CodePurposeMismatch: the payload names a purpose other than the
	// route's.
	CodePurposeMismatch Code = "purpose_mismatch"
	// CodeDeviceSignatureInvalid: device_sig does not verify by the payload's
	// device_key.
	CodeDeviceSignatureInvalid Code = "device_signature_invalid"
	// CodeFieldInvalid: a payload field is missing or in the wrong form.
	CodeFieldInvalid Code = "field_invalid"
	// CodeServerMismatch: the payload names another server.
	CodeServerMismatch Code = "server_mismatch"
	// CodeExpired: the server's clock reads exp or later.
	CodeExpired Code = "expired"
	// CodeNotYetValid: iat is more than MaxClockSkew ahead.
	CodeNotYetValid Code = "not_yet_valid"
	// CodeLifetimeTooLong: exp is not after iat, or more than
	// MaxMessageLifetime after it.
	CodeLifetimeTooLong Code = "lifetime_too_long"
	// CodeOriginNotGranted: a grant-redeem origin outside the grant's
	// origins.
	CodeOriginNotGranted Code = "origin_not_granted"
	// CodeGrantInvalid: the grant inside a grant-redeem does not verify, or
	// names another server or app key.
	CodeGrantInvalid Code = "grant_invalid"
	// CodeGrantExpired: the grant's own exp has passed.
	CodeGrantExpired Code = "grant_expired"
	// CodeOpenFailed: a sealed reply that does not open.
	CodeOpenFailed Code = "open_failed"
	// CodeNotAPoint: a public key that is not the uncompressed form of a
	// point on P-256.
	CodeNotAPoint Code = "not_a_point"
	// CodeNotAnAddress: an origin or server address that is not http or
	// https written in its one spelling.
	CodeNotAnAddress Code = "not_an_address"
)

// Join link codes.
const (
	CodeJoinPrefix  Code = "prefix"
	CodeJoinParts   Code = "parts"
	CodeJoinVersion Code = "version"
	CodeJoinAddress Code = "address"
	CodeJoinAppCode Code = "app_code"
)

// Error is a refusal with the code of the check that failed.
type Error struct {
	Code Code
	// Field names the body member at fault, when one is known.
	Field string
}

func (e *Error) Error() string { return "appkeys: " + string(e.Code) }

func fail(code Code) error { return &Error{Code: code} }

// CodeOf returns the code of an appkeys refusal, or "" for any other error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Stage says how far a signed message got before it was refused. Routes
// answer differently for a refusal at the signature and one after it.
func (c Code) Stage() Stage {
	switch c {
	case CodeBodyMalformed, CodeEnvelopeMalformed:
		return StageShape
	case CodeSignatureInvalid, CodeKidMismatch:
		return StageSignature
	case CodePayloadMalformed, CodeFieldInvalid:
		return StageFields
	}
	return StageClaims
}

// Stage is a group of verifier checks.
type Stage int

const (
	// StageShape: the body or envelope is not in its shape. Nothing was
	// verified.
	StageShape Stage = iota
	// StageSignature: the signature or key ID did not verify.
	StageSignature
	// StageFields: the signature verified; a payload field is malformed.
	StageFields
	// StageClaims: the signature verified; the payload's purpose, device
	// signature, server, times or grant were refused.
	StageClaims
)
