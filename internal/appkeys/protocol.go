package appkeys

import "time"

// Every number, label and name of the app key protocol, in one place
// (docs/architecture/app-keys.md, "Protocol").

// Protocol is the protocol name the capability advertises. Every signing
// prefix and label starts with it.
const Protocol = "silo-sync-v1"

// Purpose names what a signed message is for. The route picks the purpose it
// expects; a message signed for one purpose never verifies as another,
// because the purpose is part of the signed bytes.
type Purpose string

// The purposes a payload can carry.
const (
	PurposeIntroduce   Purpose = "introduce"
	PurposeEnroll      Purpose = "enroll"
	PurposeRedeem      Purpose = "redeem"
	PurposeSignIn      Purpose = "sign-in"
	PurposeGrant       Purpose = "grant"
	PurposeGrantRedeem Purpose = "grant-redeem"
	PurposeProfileSet  Purpose = "profile-set"
)

// enrollsDeviceKey reports whether a purpose enrolls a device key, so its
// envelope carries device_sig, the device key's own signature.
func (p Purpose) enrollsDeviceKey() bool {
	return p == PurposeIntroduce || p == PurposeEnroll || p == PurposeRedeem
}

// known reports whether p is a purpose of this protocol.
func (p Purpose) known() bool {
	switch p {
	case PurposeIntroduce, PurposeEnroll, PurposeRedeem, PurposeSignIn, PurposeGrant, PurposeGrantRedeem, PurposeProfileSet:
		return true
	}
	return false
}

// signingPrefix starts the bytes a signature covers:
// "silo-sync-v1.<purpose>.<payload as sent>".
const signingPrefix = Protocol + "."

// ReplyLabel is the KDF label of the sealed reply key.
const ReplyLabel = Protocol + ".reply"

// Sizes, in bytes, of the binary values on the wire.
const (
	// SignatureSize is an ECDSA P-256 signature as r || s.
	SignatureSize = 64
	// ChallengeSize is a server challenge.
	ChallengeSize = 32
	// GrantIDSize is a grant's single-use ID.
	GrantIDSize = 16
	// AppCodeSize is a one-time app code.
	AppCodeSize = 32
	// sealNonceSize is the AES-256-GCM nonce of a sealed reply.
	sealNonceSize = 12
	// sealKeySize is the AES-256 key a sealed reply derives.
	sealKeySize = 32
)

// Text limits, in characters.
const (
	MaxDeviceNameLength     = 128
	MaxDevicePlatformLength = 64
	MaxAppNameLength        = 128
)

// Times.
const (
	// ChallengeLifetime is how long a challenge can be spent.
	ChallengeLifetime = 2 * time.Minute
	// MaxMessageLifetime is the most exp may be after iat.
	MaxMessageLifetime = 300 * time.Second
	// MaxClockSkew is how far iat may be ahead of the server's clock.
	MaxClockSkew = 60 * time.Second
	// PendingKeyLifetime is how long an admin's waiting device key can still
	// be approved; after it, the key answers key_denied.
	PendingKeyLifetime = 24 * time.Hour
	// PendingRetryInterval is how often a waiting device key may repeat
	// sign-in.
	PendingRetryInterval = 5 * time.Second
)

// maxWholeNumber is the largest whole number a payload may carry, 2^53 - 1:
// the largest integer every JSON reader holds exactly as a double.
const maxWholeNumber = 1<<53 - 1

// Reply statuses inside a sealed reply's plaintext.
const (
	ReplySignedIn         = "signed_in"
	ReplyApprovalRequired = "approval_required"
)

// Join link prefixes. Both forms carry the same fragment,
// "v1.<base64url of the server address>.<app code>".
const (
	joinLinkHTTPS   = "https://siloserver.org/join#"
	joinLinkScheme  = "silo://join#"
	joinLinkVersion = "v1"
)
