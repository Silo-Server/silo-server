// Package abs implements the Audiobookshelf-mobile-app compatibility surface.
// It mints self-contained JWTs signed with a per-deployment secret and serves
// the /abs/api/* and /abs/public/* routes, as well as the canonical
// root-level paths real ABS clients build against (e.g. /login, /api/items).
//
// Stage 1 lands the package skeleton: Handler struct, interface stubs for
// silo-side dependencies, and an empty Mount() method. Real route handlers
// are added in subsequent stages (auth, file serving, progress, browse).
package abs

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// MediaStore is the slice of silo's catalog the ABS handler reads.
// Real impl: catalog.ItemRepository + scanner.FileRepository wrapped in
// a small adapter struct added in a later stage.
type MediaStore interface {
	GetAudiobookByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	ListAudiobooks(ctx context.Context, limit, offset int) ([]*models.MediaItem, int, error)
	GetMediaFiles(ctx context.Context, contentID string) ([]*models.MediaFile, error)
}

// TokenStore persists and validates the ABS JWT JTIs that back the
// revocable-token surface (login, refresh, logout, bearerAuth).
// Real impl: a thin repo over the abs_tokens table added in Stage 2.
type TokenStore interface {
	// InsertToken persists a newly minted JTI.
	InsertToken(ctx context.Context, tok ABSToken) error
	// GetTokenByJTI looks up a token by its JTI; returns ErrNotFound if absent.
	GetTokenByJTI(ctx context.Context, jti string) (ABSToken, error)
	// RevokeTokenByJTI marks a JTI as revoked (sets revoked_at).
	RevokeTokenByJTI(ctx context.Context, jti string) error
	// TouchToken extends last_seen_at for active-session bookkeeping.
	TouchToken(ctx context.Context, jti string) error
}

// ABSToken is the in-memory representation of a persisted JTI row.
type ABSToken struct {
	ID        string
	UserID    string
	ProfileID string
	JTI       string
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// ProfileCredentialValidator validates a (username, password) pair against
// silo's auth backend. Implemented by an adapter over internal/auth in a
// later stage.
type ProfileCredentialValidator interface {
	Validate(ctx context.Context, username, password string) (userID string, profileID string, displayName string, err error)
}

// EventPublisher delivers a realtime event to Socket.io clients. May be nil;
// handlers guard with publish/broadcast nil-safe wrappers.
type EventPublisher interface {
	Publish(userID, event string, payload any)
	Broadcast(event string, payload any)
}

// Recommender powers /items/{id}/similar. nil → route returns an empty list.
type Recommender interface {
	Similar(ctx context.Context, contentID string, limit int) ([]string, error)
}

// ---------------------------------------------------------------------------
// Config provider
// ---------------------------------------------------------------------------

// ConfigProvider supplies runtime config values the ABS handler needs.
// Keeps the handler decoupled from any particular settings-store shape.
type ConfigProvider interface {
	// JWTSecret returns the HMAC-SHA256 signing secret for ABS JWTs.
	JWTSecret(ctx context.Context) ([]byte, error)
	// AccessTTL / RefreshTTL are the default token lifetimes; zero means
	// "use built-in default (24 h / 30 d)".
	AccessTTL(ctx context.Context) (time.Duration, error)
	RefreshTTL(ctx context.Context) (time.Duration, error)
	// StandaloneLoginEnabled reports whether body-creds login is permitted
	// (i.e., operator has not disabled it in settings).
	StandaloneLoginEnabled(ctx context.Context) (bool, error)
}

// ---------------------------------------------------------------------------
// Dependencies + Handler
// ---------------------------------------------------------------------------

// Dependencies bundles everything the Handler needs at construction time.
type Dependencies struct {
	MediaStore    MediaStore
	TokenStore    TokenStore
	CredValidator ProfileCredentialValidator
	Config        ConfigProvider
	Publisher     EventPublisher   // may be nil
	Recommender   Recommender      // may be nil
	LoginLimiter  *LoginLimiter    // may be nil — one is created if absent
	// InstallID returns the current plugin install ID for building
	// host-proxy-routable URLs. Defaults to "silo.audiobooks" when nil.
	InstallID func() string
}

// Handler wires the /abs/api/* and canonical ABS-client paths.
type Handler struct {
	deps Dependencies
}

// New constructs an ABS Handler. Sensible defaults are applied for optional
// fields (LoginLimiter, InstallID).
func New(deps Dependencies) *Handler {
	if deps.LoginLimiter == nil {
		deps.LoginLimiter = NewLoginLimiter()
	}
	if deps.InstallID == nil {
		deps.InstallID = func() string { return "silo.audiobooks" }
	}
	return &Handler{deps: deps}
}

// ---------------------------------------------------------------------------
// Mount
// ---------------------------------------------------------------------------

// Mount registers the ABS-compatible routes on r. Stage 1 registers an empty
// /abs group with the access-log middleware attached; subsequent stages add
// real route handlers.
//
// The dual-mount design (routes at both /abs/api/* and /* roots) is preserved
// here so stage-by-stage handlers land in the right places without needing to
// revisit Mount later.
func (h *Handler) Mount(parent chi.Router) {
	parent.Group(func(r chi.Router) {
		r.Use(h.accessLog)
		h.mountRoutes(r)
	})
}

func (h *Handler) mountRoutes(_ chi.Router) {
	// TODO Stage 2: auth routes (login, logout, refresh, authorize, me, ping, status, init)
	// TODO Stage 3: library browse routes (libraries, items, item detail, cover, authors, series, search, personalized)
	// TODO Stage 4: playback session + file routes (play, file/download, public/track)
	// TODO Stage 5: progress routes (me/progress/*, me/items-in-progress, me/listening-stats, me/stats/year/*)
	// TODO Stage 6: social / collection routes (bookmarks, smart-collections, collections, playlists, RSS feeds, similar)
}

// ---------------------------------------------------------------------------
// Auth context helpers (used by bearerAuth middleware + handlers)
// ---------------------------------------------------------------------------

// ctxKey is the unexported ABS auth context key.
type ctxKey struct{}

// ctxAuth carries the decoded ABS JWT claims for the lifetime of a request.
type ctxAuth struct {
	UserID    string
	ProfileID string
	JTI       string
	Token     string // raw bearer token
}

// absAuthFrom extracts ABS auth from the request context. Returns (zero, false)
// when bearerAuth middleware hasn't run (unauthenticated routes).
func absAuthFrom(r *http.Request) (ctxAuth, bool) {
	a, ok := r.Context().Value(ctxKey{}).(ctxAuth)
	return a, ok
}

// bearerAuth is the authentication middleware for protected ABS routes.
// It reads the bearer token from the Authorization header or ?token= query
// param, validates the JWT, checks the JTI isn't revoked, and injects
// ctxAuth into the request context.
//
// Placeholder implementation — full validation logic lands in Stage 2 when
// TokenStore and ConfigProvider are wired to real backing stores.
func (h *Handler) bearerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			raw = r.URL.Query().Get("token")
		}
		if raw == "" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if h.deps.Config == nil || h.deps.TokenStore == nil {
			// Dependencies not yet wired — reject to avoid a security hole.
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		secret, err := h.deps.Config.JWTSecret(r.Context())
		if err != nil {
			http.Error(w, "config unavailable", http.StatusInternalServerError)
			return
		}
		claims, err := ParseToken(secret, raw)
		if err != nil || claims.Type != "access" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		row, err := h.deps.TokenStore.GetTokenByJTI(r.Context(), claims.JTI)
		if err != nil || row.RevokedAt != nil {
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		_ = h.deps.TokenStore.TouchToken(r.Context(), claims.JTI)
		ctx := context.WithValue(r.Context(), ctxKey{}, ctxAuth{
			UserID:    claims.UserID,
			ProfileID: claims.ProfileID,
			JTI:       claims.JTI,
			Token:     raw,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ---------------------------------------------------------------------------
// Publisher nil-safe wrappers
// ---------------------------------------------------------------------------

func (h *Handler) publish(userID, event string, payload any) {
	if h.deps.Publisher == nil {
		return
	}
	h.deps.Publisher.Publish(userID, event, payload)
}

func (h *Handler) broadcast(event string, payload any) {
	if h.deps.Publisher == nil {
		return
	}
	h.deps.Publisher.Broadcast(event, payload)
}

// ---------------------------------------------------------------------------
// URL helpers
// ---------------------------------------------------------------------------

// absBaseURL returns the server address prefix ABS clients should use to
// resolve response-embedded URLs.
//
//   - Host-proxied (X-Silo-User-Id header present): returns the plugin-proxy
//     path "<scheme>://<host>/api/v1/plugins/<installID>".
//   - Standalone listener: returns "<scheme>://<host>" — origin only.
//
// Honors X-Forwarded-Proto / X-Forwarded-Host for TLS-terminating proxies.
func (h *Handler) absBaseURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if r.Header.Get("X-Silo-User-Id") != "" {
		return scheme + "://" + host + "/api/v1/plugins/" + h.deps.InstallID()
	}
	return scheme + "://" + host
}

// ---------------------------------------------------------------------------
// Shared response helpers (used by handlers across multiple stages)
// ---------------------------------------------------------------------------

// writeJSON serialises v as JSON and writes it with the given HTTP status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// readPagedQuery extracts `limit` and `page` from query params. Real ABS
// treats limit=0 as "return all" (not "return zero rows") — we surface that
// intent and let callers short-circuit pagination.
func readPagedQuery(r *http.Request, defaultLimit int) (limit, page int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			page = n
		}
	}
	return limit, page
}

// pagedEnvelope builds the standard ABS pagination response shape. All eight
// fields are always emitted (no omitempty) because ABS clients branch on
// their presence (sortBy, filterBy, minified).
func pagedEnvelope(results any, total, limit, page int, sortBy string, sortDesc bool, filterBy string, minified bool, include string) map[string]any {
	return map[string]any{
		"results":  results,
		"total":    total,
		"limit":    limit,
		"page":     page,
		"sortBy":   sortBy,
		"sortDesc": sortDesc,
		"filterBy": filterBy,
		"minified": minified,
		"include":  include,
	}
}
