package models

import "time"

// APIKey represents a row in the api_keys table.
type APIKey struct {
	ID         int64
	UserID     int
	Label      string
	Key        string // full key including "sa_" prefix
	RateTier   string
	Scopes     []string // empty = unscoped (full access as the owning user)
	CreatedAt  time.Time
	LastUsedAt *time.Time // nil if never used
}

// APIKeyWithUser extends APIKey with the owning user's username.
type APIKeyWithUser struct {
	APIKey
	Username string
}
