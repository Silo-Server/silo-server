package jellycompat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type adminAPIKeyContextKey string

const adminAPIKeyKey adminAPIKeyContextKey = "jellycompat_admin_api_key"

type apiKeyValidator interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
	UpdateLastUsed(ctx context.Context, id int64) error
}

type apiKeyUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type AdminAPIKeyAuthenticator struct {
	keys     apiKeyValidator
	users    apiKeyUserLoader
	provider userstore.UserStoreProvider
	now      func() time.Time

	// profiles caches only the primary-profile lookup per user — never an
	// authorization decision, which is re-checked on every request.
	profiles *apiKeyProfileCache

	lastUsedMu sync.Mutex
	lastUsedAt map[int64]time.Time
}

type adminAPIKeyAuthResult struct {
	ctx     context.Context
	status  int
	code    string
	message string
	ok      bool
}

// NewAdminAPIKeyAuthenticator builds the authenticator. keys and users are
// required (a nil return disables API-key auth entirely). provider is optional:
// when present, an admin key can synthesize a compat session (see
// resolveSession); when nil, only the admin-bool path (RequireAdminAPIKey /
// RequireSessionOrAdminAPIKey) is available.
func NewAdminAPIKeyAuthenticator(keys apiKeyValidator, users apiKeyUserLoader, provider userstore.UserStoreProvider, now func() time.Time) *AdminAPIKeyAuthenticator {
	if keys == nil || users == nil {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &AdminAPIKeyAuthenticator{
		keys:       keys,
		users:      users,
		provider:   provider,
		now:        now,
		profiles:   newAPIKeyProfileCache(),
		lastUsedAt: make(map[int64]time.Time),
	}
}

func AdminAPIKeyFromContext(ctx context.Context) bool {
	ok, _ := ctx.Value(adminAPIKeyKey).(bool)
	return ok
}

func (a *AdminAPIKeyAuthenticator) RequireAdminAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := a.authenticate(r)
		if !result.ok {
			writeError(w, result.status, result.code, result.message)
			return
		}
		next.ServeHTTP(w, r.WithContext(result.ctx))
	})
}

func RequireSessionOrAdminAPIKey(sessionAuth *Authenticator, keyAuth *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := ExtractToken(r)
			if ok && strings.HasPrefix(token, "sa_") {
				result := keyAuth.authenticate(r)
				if !result.ok {
					writeError(w, result.status, result.code, result.message)
					return
				}
				next.ServeHTTP(w, r.WithContext(result.ctx))
				return
			}
			sessionAuth.RequireSession(next).ServeHTTP(w, r)
		})
	}
}

// RequireSessionOrAPIKeySession authenticates either a compat session token or
// an sa_ admin API key. For an API key it injects a session synthesized for the
// key user's primary profile, so ordinary handlers (which read
// SessionFromContext) work unchanged for both auth modes — matching Jellyfin,
// where an API key authorizes the same endpoints a user token does. When
// API-key auth is unavailable (nil authenticator or no UserStoreProvider) an
// sa_ token falls through to session-token auth and is rejected there.
func RequireSessionOrAPIKeySession(sessionAuth *Authenticator, keyAuth *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token, ok := ExtractToken(r); ok && strings.HasPrefix(token, "sa_") {
				if session, result, handled := keyAuth.resolveSession(r.Context(), token); handled {
					if !result.ok {
						writeError(w, result.status, result.code, result.message)
						return
					}
					serveWithSession(next, w, r, session)
					return
				}
			}
			sessionAuth.RequireSession(next).ServeHTTP(w, r)
		})
	}
}

// authenticate validates an admin API key and, on success, returns a result
// carrying the admin-API-key marker in context. Used by the server-to-server
// admin routes that act on the admin identity rather than a compat session.
func (a *AdminAPIKeyAuthenticator) authenticate(r *http.Request) adminAPIKeyAuthResult {
	token, _ := ExtractToken(r)
	apiKey, _, res := a.validate(r.Context(), token)
	if !res.ok {
		return res
	}
	a.touchLastUsed(apiKey.ID)
	return adminAPIKeyAuthResult{
		ctx:    context.WithValue(r.Context(), adminAPIKeyKey, true),
		status: http.StatusOK,
		ok:     true,
	}
}

// validate authenticates an admin API key by token and returns the key plus its
// owning admin user. The full authorization check — key lookup, user load,
// enabled, admin role — runs on EVERY call, so revocation (key deletion, user
// disable, role change) takes effect on the very next request. On failure the
// result carries the HTTP status/code/message: 401 for a missing/unknown/
// disabled key, 403 for a non-admin key.
func (a *AdminAPIKeyAuthenticator) validate(ctx context.Context, token string) (*models.APIKey, *models.User, adminAPIKeyAuthResult) {
	unauthorized := adminAPIKeyAuthResult{
		status:  http.StatusUnauthorized,
		code:    "Unauthorized",
		message: "Invalid API key",
	}
	if a == nil || a.keys == nil || a.users == nil {
		return nil, nil, unauthorized
	}
	if !strings.HasPrefix(token, "sa_") {
		return nil, nil, unauthorized
	}
	apiKey, err := a.keys.GetByKey(ctx, token)
	if err != nil || apiKey == nil {
		return nil, nil, unauthorized
	}
	user, err := a.users.GetByID(ctx, apiKey.UserID)
	if err != nil || user == nil || !user.Enabled {
		return nil, nil, unauthorized
	}
	if user.Role != "admin" {
		return nil, nil, adminAPIKeyAuthResult{
			status:  http.StatusForbidden,
			code:    "Forbidden",
			message: "Admin access required",
		}
	}
	return apiKey, user, adminAPIKeyAuthResult{ok: true}
}

// touchLastUsed records key usage without blocking the request, throttled to at
// most once per apiKeyLastUsedInterval per key so per-request validation on the
// hot path (e.g. HLS segments) does not issue a DB write each time.
func (a *AdminAPIKeyAuthenticator) touchLastUsed(id int64) {
	now := a.now()
	a.lastUsedMu.Lock()
	if last, ok := a.lastUsedAt[id]; ok && now.Sub(last) < apiKeyLastUsedInterval {
		a.lastUsedMu.Unlock()
		return
	}
	a.lastUsedAt[id] = now
	a.lastUsedMu.Unlock()

	go func(id int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.keys.UpdateLastUsed(ctx, id); err != nil {
			slog.Debug("jellycompat api key last-used update failed", "id", id, "error", err)
		}
	}(id)
}

// resolveSession authenticates an sa_ API key and returns a compat session
// synthesized for the key user's primary profile, so ordinary browse/stream
// handlers (which read SessionFromContext) work unchanged for an API key.
//
// handled reports whether this authenticator can serve API-key session auth at
// all: false when the authenticator is nil or has no UserStoreProvider wired, in
// which case the caller must fall through to session-token auth. When handled is
// true, result.ok reports success; on failure result carries what to return.
//
// The key and owning user are re-validated on EVERY call (no cached
// authorization), so revocation is immediate. Only the primary-profile lookup is
// cached (see primaryProfile), sparing the hot path the ListProfiles round-trip.
func (a *AdminAPIKeyAuthenticator) resolveSession(ctx context.Context, token string) (*Session, adminAPIKeyAuthResult, bool) {
	if a == nil || a.provider == nil {
		return nil, adminAPIKeyAuthResult{}, false
	}
	apiKey, user, res := a.validate(ctx, token)
	if !res.ok {
		return nil, res, true
	}
	a.touchLastUsed(apiKey.ID)

	profile, err := a.primaryProfile(ctx, user.ID)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat api key session synthesis failed", "component", "jellycompat", "user_id", user.ID, "error", err)
		return nil, adminAPIKeyAuthResult{
			status:  http.StatusUnauthorized,
			code:    "Unauthorized",
			message: "Unable to resolve a profile for this API key",
		}, true
	}

	// Upstream Silo token fields are left empty and the expiry zero: an API key
	// has no refreshable Silo token, and a zero expiry makes RequireSession's
	// refresh path a no-op. Downstream services scope by StreamAppUserID/ProfileID.
	return &Session{
		Token:           token,
		Username:        user.Username,
		AccountUsername: user.Username,
		ProfileID:       profile.ID,
		ProfileName:     profile.Name,
		PseudoUserID:    PseudoUserID(user.ID, profile.ID),
		StreamAppUserID: user.ID,
		CreatedAt:       a.now(),
	}, adminAPIKeyAuthResult{ok: true}, true
}

// primaryProfile returns the account's primary profile, caching the result per
// user for apiKeyProfileCacheTTL. This caches profile DATA only — never an
// authorization decision — so it is safe against revocation (validated
// separately on every request).
func (a *AdminAPIKeyAuthenticator) primaryProfile(ctx context.Context, userID int) (userstore.Profile, error) {
	if profile, ok := a.profiles.get(userID, a.now()); ok {
		return profile, nil
	}
	store, err := a.provider.ForUser(ctx, userID)
	if err != nil {
		return userstore.Profile{}, fmt.Errorf("open user store for user %d: %w", userID, err)
	}
	profiles, err := store.ListProfiles(ctx)
	if err != nil {
		return userstore.Profile{}, fmt.Errorf("list profiles for user %d: %w", userID, err)
	}
	profile, err := selectPrimaryProfile(userID, profiles)
	if err != nil {
		return userstore.Profile{}, err
	}
	a.profiles.put(userID, profile, a.now().Add(apiKeyProfileCacheTTL))
	return profile, nil
}

// selectPrimaryProfile returns the account's primary profile, falling back to
// the first profile (with a warning) when none is flagged primary, and erroring
// when the account has no profiles.
func selectPrimaryProfile(userID int, profiles []userstore.Profile) (userstore.Profile, error) {
	if len(profiles) == 0 {
		return userstore.Profile{}, fmt.Errorf("user %d has no profiles", userID)
	}
	for _, p := range profiles {
		if p.IsPrimary {
			return p, nil
		}
	}
	slog.Warn("jellycompat api key: no primary profile found, using first",
		"user_id", userID, "profile_id", profiles[0].ID)
	return profiles[0], nil
}

const (
	// apiKeyProfileCacheTTL bounds how long a primary-profile lookup is reused.
	// Profiles are stable, so this is generous; it only affects how quickly a
	// reassigned/renamed primary profile is picked up, never authorization
	// (the key and user are re-validated on every request).
	apiKeyProfileCacheTTL = 5 * time.Minute
	// apiKeyLastUsedInterval throttles api_keys.last_used_at writes so that
	// per-request validation on the hot path does not write each time.
	apiKeyLastUsedInterval = time.Minute
)

type apiKeyProfileCacheEntry struct {
	profile   userstore.Profile
	expiresAt time.Time
}

// apiKeyProfileCache caches the primary-profile lookup per user so the hot path
// (e.g. HLS segment requests) skips the ListProfiles round-trip. It caches only
// profile data, never an authorization decision.
type apiKeyProfileCache struct {
	mu      sync.RWMutex
	entries map[int]apiKeyProfileCacheEntry
}

func newAPIKeyProfileCache() *apiKeyProfileCache {
	return &apiKeyProfileCache{entries: make(map[int]apiKeyProfileCacheEntry)}
}

func (c *apiKeyProfileCache) get(userID int, now time.Time) (userstore.Profile, bool) {
	c.mu.RLock()
	entry, ok := c.entries[userID]
	c.mu.RUnlock()
	if !ok || !entry.expiresAt.After(now) {
		return userstore.Profile{}, false
	}
	return entry.profile, true
}

func (c *apiKeyProfileCache) put(userID int, profile userstore.Profile, expiresAt time.Time) {
	c.mu.Lock()
	c.entries[userID] = apiKeyProfileCacheEntry{profile: profile, expiresAt: expiresAt}
	c.mu.Unlock()
}
