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
	// existing account, and may not change the password, recovery email, role,
	// or break-glass flag of an account that is already an admin. Provisioning
	// and managing ordinary accounts, passwords included, is in scope.
	ScopeAdminUsers = "admin:users"

	// ScopeAdminAccessGroupsRead covers read-only access-group discovery.
	ScopeAdminAccessGroupsRead    = "admin:access-groups:read"
	ScopeAdminSessionsSummaryRead = "admin:sessions:summary:read"
	ScopeLibrariesRead            = "libraries:read"

	// ScopeAdminMonitorRead covers read-only server monitoring for an outside
	// tool: live sessions, playback history, activity stats, accounts and their
	// devices, downloads, node and host health, and the catalog listing. It
	// never reaches filesystem paths, node addresses, storage keys, setting
	// values or credentials; the few monitored operations that carry such
	// details withhold them from scoped keys.
	ScopeAdminMonitorRead = "admin:monitor:read"
	// ScopeAdminCatalogPaths covers the catalog listing with each file's
	// on-disk path, for a tool that mirrors the library and matches its files.
	// It reaches nothing else; combine it with admin:monitor:read for the
	// change events.
	ScopeAdminCatalogPaths = "admin:catalog:paths"
	// ScopeAdminSessionsControl covers the administrator playback commands:
	// pause, resume, stop, message and terminate a live session.
	ScopeAdminSessionsControl = "admin:sessions:control"

	// The user scopes act as one household profile, or as the account when an
	// account key names none, and reach nothing an administrator role is
	// needed for. They are the only scopes a profile-bound key, or a key on an
	// account that is not an admin, may carry. None admits playback, streams
	// or downloads.

	// ScopeCatalogRead covers browsing: libraries, home and library rows,
	// catalog items, people and search.
	ScopeCatalogRead = "catalog:read"
	// ScopeWatchRead covers a profile's own activity: history, progress,
	// ratings, favorites, watchlist, hidden titles and the profile export.
	ScopeWatchRead = "watch:read"
	// ScopeWatchWrite covers changing that activity: marking watched and
	// unwatched, syncing progress, removing history, and editing ratings,
	// favorites, watchlist and hidden titles. The profile import stays out:
	// it also overwrites settings.
	ScopeWatchWrite = "watch:write"
	// ScopeRequests covers media requests as the profile's own session sees
	// them: discovery, the account's requests (only the profile's own when it
	// is blocked from requests), creating, canceling and following them.
	ScopeRequests = "requests"
	// ScopeNotifications covers a profile's notification inbox: listing and
	// syncing it, the unread count, and marking notifications read. It does
	// not reach delivery settings: preferences, webhooks, email, Discord or
	// push registrations.
	ScopeNotifications = "notifications"
)

// Scope audiences. An admin scope names operations that need an
// administrator account; a user scope acts as a profile.
const (
	APIKeyScopeAudienceAdmin = "admin"
	APIKeyScopeAudienceUser  = "user"
)

// APIKeyScope is one scope a key may carry, paired with the description
// clients show when offering scopes to a user.
type APIKeyScope struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Audience is "user" for a scope any account's key may carry and "admin"
	// for one that only does anything on an administrator's account key.
	Audience string `json:"audience" enum:"admin,user"`
}

// APIKeyScopeCatalog returns every scope a key may carry, in a stable order.
// It is the single source of truth behind both scope validation and the
// GET /api/v1/api-keys/scopes feature-detection endpoint.
func APIKeyScopeCatalog() []APIKeyScope {
	return []APIKeyScope{
		{Name: ScopeAdminSessionsSummaryRead, Audience: APIKeyScopeAudienceAdmin, Description: "Read live playback summaries without diagnostic details or session control."},
		{Name: ScopeLibrariesRead, Audience: APIKeyScopeAudienceUser, Description: "Discover libraries visible to the account or selected profile, without storage metadata."},
		{
			Name: ScopeCatalogRead, Audience: APIKeyScopeAudienceUser,
			Description: "Browse what the profile can see: libraries, home and library rows, titles, people and search. No playback or downloads.",
		},
		{
			Name: ScopeWatchRead, Audience: APIKeyScopeAudienceUser,
			Description: "Read the profile's watch history, progress, ratings, favorites, watchlist and hidden titles, and export the profile's data.",
		},
		{
			Name: ScopeWatchWrite, Audience: APIKeyScopeAudienceUser,
			Description: "Mark titles watched or unwatched, sync progress, remove history, and edit ratings, favorites, watchlist and hidden titles.",
		},
		{
			Name: ScopeRequests, Audience: APIKeyScopeAudienceUser,
			Description: "Browse requestable titles and create, follow and cancel the profile's media requests.",
		},
		{
			Name: ScopeNotifications, Audience: APIKeyScopeAudienceUser,
			Description: "Read the profile's notifications as they arrive and mark them read. No delivery settings.",
		},
		{
			Name: ScopeAdminUsers, Audience: APIKeyScopeAudienceAdmin,
			Description: "Manage user accounts: create, list, read, update, and delete users and " +
				"read their profiles. Cannot create or modify admin accounts.",
		},
		{
			Name:        ScopeAdminAccessGroupsRead,
			Audience:    APIKeyScopeAudienceAdmin,
			Description: "Read access groups and their policies.",
		},
		{
			Name: ScopeAdminMonitorRead, Audience: APIKeyScopeAudienceAdmin,
			Description: "Monitor the server read-only: live sessions, playback history, activity stats, " +
				"accounts, devices, downloads, node and host health, the catalog listing, and media requests. Never exposes filesystem paths, " +
				"node addresses, storage keys, setting values or credentials.",
		},
		{
			Name: ScopeAdminCatalogPaths, Audience: APIKeyScopeAudienceAdmin,
			Description: "Read the catalog listing with each file's path on the server, to mirror the library " +
				"and match its files. Nothing else.",
		},
		{
			Name:        ScopeAdminSessionsControl,
			Audience:    APIKeyScopeAudienceAdmin,
			Description: "Pause, resume, stop, message and terminate live playback sessions.",
		},
	}
}

// UnknownAPIKeyScopeError names a requested scope that the catalog being
// validated against does not contain. Callers use it to report which scope a
// client sent without re-parsing the message.
type UnknownAPIKeyScopeError struct {
	Scope string
}

func (e *UnknownAPIKeyScopeError) Error() string {
	return fmt.Sprintf("unknown api key scope %q", e.Scope)
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
			return nil, &UnknownAPIKeyScopeError{Scope: s}
		}
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out, nil
}

// UserAPIKeyScopesOnly reports whether scopes is a non-empty list of user
// scopes: what a profile-bound key, or a key on an account that is not an
// admin, must carry. An unscoped key and one naming an admin or unknown scope
// both fail.
func UserAPIKeyScopesOnly(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	catalog := APIKeyScopeCatalog()
	for _, s := range scopes {
		if !slices.ContainsFunc(catalog, func(c APIKeyScope) bool {
			return c.Name == s && c.Audience == APIKeyScopeAudienceUser
		}) {
			return false
		}
	}
	return true
}

// APIKeyEventScopes are the user scopes that open the realtime events socket.
// Each admits one channel there: catalog:read the catalog channel, watch:read
// the acting profile's user_state, notifications the acting profile's
// notifications.
var APIKeyEventScopes = []string{ScopeCatalogRead, ScopeWatchRead, ScopeNotifications}

// HoldsAPIKeyEventScope reports whether scopes names one of APIKeyEventScopes.
func HoldsAPIKeyEventScope(scopes []string) bool {
	return slices.ContainsFunc(scopes, func(s string) bool { return slices.Contains(APIKeyEventScopes, s) })
}
