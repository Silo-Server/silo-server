package models

import "time"

// How an app key was enrolled (auth_person_keys.enrolled_via and
// auth_device_keys.enrolled_via).
const (
	AppKeyEnrolledIntroduce = "introduce"
	AppKeyEnrolledSession   = "session"
	AppKeyEnrolledAppCode   = "app_code"
	AppKeyEnrolledGrant     = "grant"
)

// Device key kinds (auth_device_keys.kind).
const (
	// DeviceKeyKindDevice is an app's own key on one install.
	DeviceKeyKindDevice = "device"
	// DeviceKeyKindApp is another app's key, enrolled through a grant.
	DeviceKeyKindApp = "app"
)

// Device key statuses (auth_device_keys.status).
const (
	DeviceKeyStatusActive  = "active"
	DeviceKeyStatusPending = "pending"
	DeviceKeyStatusDenied  = "denied"
)

// AppPersonKey is a row in auth_person_keys: the key that vouches for an
// account's device keys. An account holds at most one that is not revoked.
type AppPersonKey struct {
	ID        string
	UserID    int
	PublicKey []byte // 65-byte uncompressed P-256 point
	KeyID     string
	// IdentityID is the provider identity of the session that enrolled the
	// key. It is a plain number: it stays after that identity is deleted,
	// and a key whose identity is gone from its account is dead.
	IdentityID *int64
	// ProviderBound is true when the enrolling session came from a provider
	// identity, and stays true after the identity is deleted.
	ProviderBound bool
	EnrolledVia   string
	// EnrolledIP is the client address of the request that enrolled the
	// key, as a login session records its own; empty when unknown.
	EnrolledIP string
	CreatedAt  time.Time
	RevokedAt  *time.Time
}

// AppDeviceKey is a row in auth_device_keys: a key that opens login sessions
// for its account.
type AppDeviceKey struct {
	ID        string
	UserID    int
	PublicKey []byte // 65-byte uncompressed P-256 point
	KeyID     string
	Kind      string
	Name      string
	Platform  string
	Status    string
	// EnrolledVia is one of the AppKeyEnrolled values.
	EnrolledVia string
	// PersonKeyID is the person key that signed the introduction or grant.
	PersonKeyID   *string
	IdentityID    *int64
	ProviderBound bool
	// EnrolledIP is as on AppPersonKey.
	EnrolledIP string
	CreatedAt  time.Time
	// LastUsedAt is when the key last opened a login session; nil for a key
	// that never has (a key waiting for approval).
	LastUsedAt *time.Time
	// LastAttemptAt is a waiting key's last sign-in attempt, for the retry
	// interval. Never shown.
	LastAttemptAt *time.Time
	RevokedAt     *time.Time
}
