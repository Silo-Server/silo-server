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
	"log/slog"
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

// AudiobookLibrary is the narrow library view the ABS handlers expose.
// The production adapter (Stage 7) builds these from media_folders WHERE
// type = 'audiobooks'.
type AudiobookLibrary struct {
	ID   int64
	Name string
	Type string // always "audiobooks" for this surface
}

// MediaStore is the slice of silo's catalog the ABS handler reads.
// Real impl: catalog.ItemRepository + scanner.FileRepository wrapped in
// a small adapter struct added in a later stage.
type MediaStore interface {
	GetAudiobookByID(ctx context.Context, contentID string) (*models.MediaItem, error)
	// ListAudiobooks returns a page of audiobooks. When libraryID is non-zero
	// it filters to items in that media_folder; 0 means all audiobook items.
	ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int) ([]*models.MediaItem, int, error)
	GetMediaFiles(ctx context.Context, contentID string) ([]*models.MediaFile, error)
	// GetMediaFileByID fetches a single media file by its integer PK.
	// Used by the ABS file-streaming handler when a caller supplies a
	// raw file ID instead of an ino.
	GetMediaFileByID(ctx context.Context, fileID int) (*models.MediaFile, error)
	// ListAudiobookLibraries returns media_folder rows with type='audiobooks'.
	ListAudiobookLibraries(ctx context.Context) ([]AudiobookLibrary, error)
	// SearchAudiobooks does a fuzzy title/author/narrator match for the ABS
	// /libraries/{id}/search endpoint. Hydrates People so the mapper has
	// author/narrator names.
	SearchAudiobooks(ctx context.Context, libraryID int64, query string, limit int) ([]*models.MediaItem, error)
	// ListContinueListening returns books that the given user has progress
	// on but hasn't finished — feeds the Home tab's continue shelf.
	ListContinueListening(ctx context.Context, userID, profileID string, libraryID int64, limit int) ([]*models.MediaItem, error)
	// ListRecentlyAdded returns the most recently added audiobooks for the
	// Home tab's recently-added shelf.
	ListRecentlyAdded(ctx context.Context, libraryID int64, limit int) ([]*models.MediaItem, error)
	// ListDiscover returns a randomized sampling of audiobooks for the
	// Home tab's discover shelf (helps new users browse the library).
	ListDiscover(ctx context.Context, libraryID int64, limit int) ([]*models.MediaItem, error)
	// ListLibraryAuthors returns distinct authors of audiobooks in the
	// library along with each author's book count.
	ListLibraryAuthors(ctx context.Context, libraryID int64, limit int) ([]AuthorSummary, error)
	// ListLibrarySeries returns distinct series (from audiobook_series)
	// represented in the library, ordered by name.
	ListLibrarySeries(ctx context.Context, libraryID int64, limit int) ([]SeriesSummary, error)
}

// AuthorSummary is an aggregated author entry for /libraries/{id}/authors.
type AuthorSummary struct {
	ID        string
	Name      string
	NumBooks  int
}

// SeriesSummary is an aggregated series entry for /libraries/{id}/series.
type SeriesSummary struct {
	ID       string
	Name     string
	NumBooks int
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

// SocketIOServer exposes the Socket.io HTTP handler. The concrete
// implementation lives in internal/audiobooks/abssocket. Keeping the
// interface here avoids a circular import: handler.go uses it, abssocket
// imports abs for ParseToken/EventPublisher, and the wiring is done by the
// caller (service.go or main.go) that imports both packages.
type SocketIOServer interface {
	Handler() http.Handler
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
	// ProgressStore provides access to user_watch_progress for ABS
	// progress endpoints. May be nil; handlers degrade gracefully.
	ProgressStore ProgressStore
	// PlaybackSessionStore persists abs_playback_sessions rows
	// (migration 143) for /session/{sid}/sync and /session/{sid}/close.
	// May be nil; handlers degrade gracefully.
	PlaybackSessionStore ABSPlaybackSessionStore
	// SocketIO is the Socket.io server mounted at /abs/socket.io/. May be nil;
	// the route is only registered when a non-nil value is supplied.
	SocketIO SocketIOServer
	// CoverResolver translates a raw silo poster path (e.g.
	// "local/audiobooks/.../original.webp") into a fully-qualified URL
	// the ABS client can fetch. Optional; when nil, /api/items/{id}/cover
	// 404s rather than redirecting to an unreachable relative path.
	CoverResolver func(ctx context.Context, path, variant string) string
}

// Handler wires the /abs/api/* and canonical ABS-client paths.
type Handler struct {
	deps Dependencies
}

// New constructs an ABS Handler. Sensible defaults are applied for optional
// fields (LoginLimiter, InstallID).
//
// MediaStore is required: many handlers (libraries, items, me, play) deref
// it unconditionally on the request hot path, and a nil store would panic
// the first time a real request hits them. Fail fast at construction so
// misconfigured deployments break at startup rather than silently passing
// /login and crashing on the next request.
func New(deps Dependencies) *Handler {
	if deps.MediaStore == nil {
		panic("abs.New: MediaStore is required")
	}
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

func (h *Handler) mountRoutes(r chi.Router) {
	// Discovery + auth endpoints: real ABS exposes these at server ROOT
	// (no /api or /abs/api prefix). Mobile clients do `${addr}/ping`,
	// `${addr}/login`, etc. Designed to be mounted on a dedicated listener
	// so the routes don't collide with silo's SPA catch-all.
	for _, prefix := range []string{"", "/abs/api"} {
		r.Get(prefix+"/ping", h.handleABSPing)
		r.Get(prefix+"/healthcheck", h.handleABSPing) // same body as /ping
		r.Get(prefix+"/init", h.handleABSInit)
		r.Get(prefix+"/status", h.handleABSStatus)
	}

	// Stage 2: login (body-creds + host-proxied paths).
	r.Post("/login", h.handleLogin)
	r.Post("/abs/api/login", h.handleLogin)
	// Token rotation — mobile clients call this every ~22h to avoid the
	// 24h access-token interactive re-login trap.
	r.Post("/auth/refresh", h.handleRefresh)
	r.Post("/abs/api/auth/refresh", h.handleRefresh)
	// Logout is mounted OUTSIDE bearerAuth so an expired-access client can
	// still sign out (the primary "sign out" UX moment). The handler parses
	// the bearer locally, revokes the JTI if parseable, and always returns
	// 204 — matches the canonical continuum-plugin behavior.
	r.Post("/logout", h.handleLogout)
	r.Post("/api/logout", h.handleLogout)
	r.Post("/abs/api/logout", h.handleLogout)
	r.Post("/abs/api/auth/logout", h.handleLogout) // legacy path

	// Unauthenticated cover + author-image routes. Real ABS serves covers
	// without auth (getDoesServerImagesRequireToken returns false for our
	// version), so mounting these outside bearerAuth avoids 401s.
	for _, prefix := range []string{"/abs/api", "/api"} {
		r.Get(prefix+"/items/{id}/cover", h.handleItemCover)
		r.Get(prefix+"/authors/{id}/image", h.handleAuthorImage)
	}

	// Stage 3: playback session + file routes, registered under both the
	// legacy /abs/api prefix and the canonical /api prefix that the official
	// ABS mobile client builds against (no /abs prefix at server root).
	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		for _, prefix := range []string{"/abs/api", "/api"} {
			// POST /api/items/{libraryItemId}/play — start a play session,
			// get back a stream URL + ABS-shaped manifest.
			r.Post(prefix+"/items/{libraryItemId}/play", h.handlePlayStart)

			// GET /api/items/{libraryItemId}/file/{ino} — stream a specific audio file.
			// /download variant is the same handler; Content-Disposition is set when
			// the path ends in /download.
			r.Get(prefix+"/items/{libraryItemId}/file/{ino}", h.handleFileStream)
			r.Get(prefix+"/items/{libraryItemId}/file/{ino}/download", h.handleFileStream)
		}
	})

	// Stage 4: progress + session tracking — requires bearerAuth.
	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		for _, prefix := range []string{"/abs/api", "/api"} {
			// GET  /me/progress              — all audiobook progress for the caller
			r.Get(prefix+"/me/progress", h.handleGetMyProgress)
			// GET  /me/progress/{id}         — progress for one item
			r.Get(prefix+"/me/progress/{libraryItemId}", h.handleGetItemProgress)
			// POST /me/progress/{id}         — set / update progress (ABS PATCH semantics)
			r.Post(prefix+"/me/progress/{libraryItemId}", h.handleSetItemProgress)
			// PATCH /session/{sid}           — heartbeat: position + time_listening
			r.Patch(prefix+"/session/{sid}", h.handleSessionSync)
			// POST  /session/{sid}/close     — finalise the play session
			r.Post(prefix+"/session/{sid}/close", h.handleSessionClose)
		}
	})

	// Stage 5: browse routes (libraries, items, item detail, me, similar,
	// continue-listening) + author/series/search/personalized stubs.
	// Requires bearerAuth.
	r.Group(func(r chi.Router) {
		r.Use(h.bearerAuth)
		for _, prefix := range []string{"/abs/api", "/api"} {
			// Current user object.
			r.Get(prefix+"/me", h.handleMe)
			// Real-ABS /authorize: validates the bearer and re-mints the
			// /me envelope so the client can resume without retyping creds.
			r.Post(prefix+"/authorize", h.handleABSAuthorize)
			// Continue Listening shelf.
			r.Get(prefix+"/me/items-in-progress", h.handleItemsInProgress)
			// Library list + detail.
			r.Get(prefix+"/libraries", h.handleLibraries)
			r.Get(prefix+"/libraries/{libraryId}", h.handleLibraryDetail)
			// Browse items in a library.
			r.Get(prefix+"/libraries/{libraryId}/items", h.handleLibraryItems)
			// Author / series / search / personalized — stubbed.
			r.Get(prefix+"/libraries/{libraryId}/authors", h.handleLibraryAuthors)
			r.Get(prefix+"/libraries/{libraryId}/series", h.handleLibrarySeries)
			r.Get(prefix+"/libraries/{libraryId}/search", h.handleLibrarySearch)
			r.Get(prefix+"/libraries/{libraryId}/personalized", h.handlePersonalized)
			// Single item detail.
			r.Get(prefix+"/items/{id}", h.handleItem)
			// Similar items (optional Recommender; empty list when nil).
			r.Get(prefix+"/items/{id}/similar", h.handleSimilarItems)
		}
	})

	// Stage 6: Socket.io realtime endpoint.
	if h.deps.SocketIO != nil {
		r.Mount("/socket.io", h.deps.SocketIO.Handler())
		r.Mount("/abs/socket.io", h.deps.SocketIO.Handler())
	}

	// TODO: social / collection routes (bookmarks, smart-collections,
	// collections, playlists, RSS feeds, author/series detail, listening stats)
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
			slog.Debug("abs bearerAuth: no token", "path", r.URL.Path, "remote", r.RemoteAddr)
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		if h.deps.Config == nil || h.deps.TokenStore == nil {
			slog.Warn("abs bearerAuth: deps not wired",
				"have_config", h.deps.Config != nil,
				"have_token_store", h.deps.TokenStore != nil,
				"path", r.URL.Path)
			http.Error(w, "auth not configured", http.StatusServiceUnavailable)
			return
		}
		secret, err := h.deps.Config.JWTSecret(r.Context())
		if err != nil {
			slog.Error("abs bearerAuth: jwt secret fetch failed", "err", err, "path", r.URL.Path)
			http.Error(w, "config unavailable", http.StatusInternalServerError)
			return
		}
		claims, err := ParseToken(secret, raw)
		if err != nil {
			slog.Debug("abs bearerAuth: parse failed", "err", err, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if claims.Type != "access" {
			slog.Debug("abs bearerAuth: wrong token type", "type", claims.Type, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		row, err := h.deps.TokenStore.GetTokenByJTI(r.Context(), claims.JTI)
		if err != nil {
			slog.Debug("abs bearerAuth: jti lookup failed",
				"jti", claims.JTI, "err", err, "path", r.URL.Path)
			http.Error(w, "token revoked", http.StatusUnauthorized)
			return
		}
		if row.RevokedAt != nil {
			slog.Debug("abs bearerAuth: jti revoked", "jti", claims.JTI, "path", r.URL.Path)
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
