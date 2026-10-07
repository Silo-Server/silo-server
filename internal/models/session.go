package models

import "time"

// AuthSession represents a row in the auth_sessions table.
type AuthSession struct {
	ID                     string     // UUID session ID (included in JWT claims)
	UserID                 int        // FK to users.id
	DeviceName             string     // human-readable device name: the reported one, else the User-Agent
	IPAddress              string     // optional IP address
	CreatedAt              time.Time  // when the session was created
	LastSeenAt             *time.Time // last authenticated request; nil until recorded
	ExpiresAt              time.Time  // when the session expires
	RevokedAt              *time.Time // nil if active, set when revoked
	ImpersonatorUserID     *int
	ImpersonationStartedAt *time.Time
	// IdentityID is the external sign-in identity the session came from
	// (plugin_auth_identities.id); nil for a local session. A refresh of such
	// a session re-checks the identity with its provider.
	IdentityID *int64
	// ProviderSince is when the external provider last vouched for the chain
	// of sign-ins this session belongs to (auth_sessions.provider_since); nil
	// for a session the provider never vouched for. When the provider cannot
	// re-check the identity, or the identity is gone, the session ends the
	// refresh expiry after this instant instead of sliding.
	ProviderSince *time.Time
	// DeviceID and DevicePlatform are the device the session was opened from,
	// as the client reported it in X-Silo-Device-Id and
	// X-Silo-Device-Platform; empty when it sent none. Client-reported audit
	// data: never authorize with them.
	DeviceID       string
	DevicePlatform string
}
