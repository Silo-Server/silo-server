package auth

import (
	"fmt"
	"slices"
)

// API key scopes. A key with no scopes behaves as it always has: full access
// as the owning user. A key with scopes is an allowlist credential — the auth
// middleware only admits it to the routes its scopes name. Scopes narrow, they
// never grant: role checks (e.g. admin-only routes) still apply to the owning
// user afterwards.
const (
	// ScopeAdminUsers covers admin user lifecycle management: list, create,
	// read, update, and delete users, plus reading a user's profiles.
	//
	// A scoped key must never be able to trade its allowlist for an unscoped
	// admin session, so this scope stops at the admin boundary: it may not
	// create an account with the admin role, may not grant that role to an
	// existing account, and may not change the password or role of an account
	// that is already an admin. Provisioning and managing ordinary accounts,
	// passwords included, is in scope.
	ScopeAdminUsers = "admin:users"

	// ScopeAdminAccessGroupsRead covers read-only access-group discovery.
	ScopeAdminAccessGroupsRead = "admin:access-groups:read"
)

// APIKeyScope is one scope a key may carry, paired with the description
// clients show when offering scopes to a user.
type APIKeyScope struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// APIKeyScopeCatalog returns every scope a key may carry, in a stable order.
// It is the single source of truth behind both scope validation and the
// GET /api/v1/api-keys/scopes feature-detection endpoint.
func APIKeyScopeCatalog() []APIKeyScope {
	return []APIKeyScope{
		{
			Name: ScopeAdminUsers,
			Description: "Manage user accounts: create, list, read, update, and delete users and " +
				"read their profiles. Cannot create or modify admin accounts.",
		},
		{
			Name:        ScopeAdminAccessGroupsRead,
			Description: "Read access groups and their policies.",
		},
	}
}

// ValidAPIKeyScopes returns every scope a key may carry.
func ValidAPIKeyScopes() []string {
	catalog := APIKeyScopeCatalog()
	scopes := make([]string, 0, len(catalog))
	for _, scope := range catalog {
		scopes = append(scopes, scope.Name)
	}
	return scopes
}

// NormalizeAPIKeyScopes validates and deduplicates a requested scope list.
// nil or empty input is valid and means "unscoped" (full access).
func NormalizeAPIKeyScopes(scopes []string) ([]string, error) {
	if len(scopes) == 0 {
		return []string{}, nil
	}
	valid := ValidAPIKeyScopes()
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		if !slices.Contains(valid, s) {
			return nil, fmt.Errorf("unknown api key scope %q", s)
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}
