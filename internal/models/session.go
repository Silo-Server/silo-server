package models

import "time"

// AuthSession represents a row in the auth_sessions table.
type AuthSession struct {
	ID                       string     // UUID session ID (included in JWT claims)
	UserID                   int        // FK to users.id
	DeviceName               string     // optional device identifier
	IPAddress                string     // optional IP address
	CreatedAt                time.Time  // when the session was created
	ExpiresAt                time.Time  // when the session expires
	RevokedAt                *time.Time // nil if active, set when revoked
	AuthRevision             int64
	ImpersonatorUserID       *int
	ImpersonatorAuthRevision *int64
	ImpersonationStartedAt   *time.Time
}
