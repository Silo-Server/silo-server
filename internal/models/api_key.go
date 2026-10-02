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

// APIKeyMetadata is the canonical editable configuration. It deliberately has
// no credential, mutable username, or usage timestamp. Revision identifies this
// configuration generation, including deletion and recreation of the same ID.
type APIKeyMetadata struct {
	ID        int64
	UserID    int
	Label     string
	KeyPrefix string
	RateTier  string
	Scopes    []string
	CreatedAt time.Time
	Revision  int64
}

// APIKeyMetadataWithUsage decorates configuration for lists, not editor tags.
type APIKeyMetadataWithUsage struct {
	APIKeyMetadata
	LastUsedAt *time.Time
}

type APIKeyMetadataWithUser struct {
	APIKeyMetadataWithUsage
	Username string
}
