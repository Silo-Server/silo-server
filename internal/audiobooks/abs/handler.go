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

	"github.com/Silo-Server/silo-server/internal/catalog"
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
	GetAudiobookByID(ctx context.Context, contentID string, access catalog.AccessFilter) (*models.MediaItem, error)
	// GetAudiobooksByIDs batch-fetches audiobooks by content_id (people + series
	// hydrated once), keyed by content_id, for list/shelf handlers.
	GetAudiobooksByIDs(ctx context.Context, contentIDs []string, access catalog.AccessFilter) (map[string]*models.MediaItem, error)
	// ListAudiobooks returns a page of audiobooks. When libraryID is non-zero
	// it filters to items in that media_folder; 0 means all audiobook items.
	// filter optionally pushes an authors/series/narrators predicate into the
	// query (Filter{} for none) so per-author syncs avoid a full-library scan.
	ListAudiobooks(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter, filter Filter) ([]*models.MediaItem, int, error)
	GetMediaFiles(ctx context.Context, contentID string, access catalog.AccessFilter) ([]*models.MediaFile, error)
	// GetMediaFileByID fetches a single media file by its integer PK.
	// Used by the ABS file-streaming handler when a caller supplies a
	// raw file ID instead of an ino.
	GetMediaFileByID(ctx context.Context, fileID int) (*models.MediaFile, error)
	// ListAudiobookLibraries returns media_folder rows with type='audiobooks'.
	ListAudiobookLibraries(ctx context.Context, access catalog.AccessFilter) ([]AudiobookLibrary, error)
	// SearchAudiobooks does a fuzzy title/author/narrator match for the ABS
	// /libraries/{id}/search endpoint. Hydrates People so the mapper has
	// author/narrator names.
	SearchAudiobooks(ctx context.Context, libraryID int64, query string, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error)
	// ListContinueListening returns books that the given user has progress
	// on but hasn't finished — feeds the Home tab's continue shelf.
	ListContinueListening(ctx context.Context, userID, profileID string, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error)
	// ListRecentlyAdded returns the most recently added audiobooks for the
	// Home tab's recently-added shelf.
	ListRecentlyAdded(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error)
	// ListDiscover returns a randomized sampling of audiobooks for the
	// Home tab's discover shelf (helps new users browse the library).
	ListDiscover(ctx context.Context, libraryID int64, limit int, access catalog.AccessFilter) ([]*models.MediaItem, error)
	// ListLibraryAuthors returns one page of distinct audiobook authors (from a
	// precomputed materialized view) plus the total author count. sortBy is one
	// of "name" (default), "addedAt", or "numBooks"; limit<=0 returns all.
	ListLibraryAuthors(ctx context.Context, libraryID int64, limit, offset int, sortBy string, sortDesc bool, access catalog.AccessFilter) ([]AuthorSummary, int, error)
	// ListLibrarySeries returns one SQL-paginated page of distinct series (from
	// audiobook_series) in the library plus the total series count. limit<=0
	// returns all.
	ListLibrarySeries(ctx context.Context, libraryID int64, limit, offset int, access catalog.AccessFilter) ([]SeriesSummary, int, error)
	// GetAuthorByID returns the author with the given people.id plus
	// their audiobook list, sorted by title. Returns ErrNotFound when
	// no people row matches.
	GetAuthorByID(ctx context.Context, authorID string, access catalog.AccessFilter) (Author, error)
	// GetSeriesByName returns the canonical series (case-insensitive
	// match on audiobook_series.series_name) with its books ordered
	// by series_index ASC (NULLS LAST), title fallback. Returns
	// ErrNotFound when no rows match.
	GetSeriesByName(ctx context.Context, seriesName string, access catalog.AccessFilter) (Series, error)
}

// AuthorSummary is an aggregated author entry for /libraries/{id}/authors.
type AuthorSummary struct {
	ID       string
	Name     string
	NumBooks int
	// HasPhoto reports whether the author's people row carries a photo, so
	// list responses can emit a non-null imagePath (the client's cue to
	// fetch /api/authors/{id}/image).
	HasPhoto bool
}

// SeriesSummary is an aggregated series entry for /libraries/{id}/series.
//
// Books carries up to ~4 cover-preview entries for the LazySeriesCard
// GroupCover stack — the ABS mobile client reads
// `series.books[i].media.coverPath` to render each cover, and a card
// with no books renders only the series name as a fallback.
type SeriesSummary struct {
	ID       string
	Name     string
	NumBooks int
	Books    []SeriesBookPreview
}

// SeriesBookPreview is a single book id+title pair returned alongside
// each SeriesSummary. The /libraries/{id}/series handler expands these
// into full minified LibraryItem entries (with cover URLs) on the wire.
type SeriesBookPreview struct {
	ContentID string
	Title     string
	UpdatedAt time.Time
}

// Author is the detail-shape author with embedded books list.
type Author struct {
	ID         string
	Name       string
	PosterPath string // resolved via CoverResolver on emit
	Books      []*models.MediaItem
}

// Series is the detail-shape series with books ordered by series_index.
type Series struct {
	ID    string // lowercased series_name
	Name  string // canonical series_name
	Books []*models.MediaItem
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
	// RevokeTokenIfActive atomically marks an unrevoked token as revoked and
	// returns its previous row. Returns ErrNotFound when the token is absent or
	// was already revoked.
	RevokeTokenIfActive(ctx context.Context, jti string) (ABSToken, error)
	// RevokeTokensForPrincipal revokes every active access/refresh token for a
	// user profile. Logout uses this to invalidate refresh tokens as well as the
	// presented access token.
	RevokeTokensForPrincipal(ctx context.Context, userID, profileID string) error
	// TouchToken extends last_seen_at for active-session bookkeeping.
	TouchToken(ctx context.Context, jti string) error
}

// ABSToken is the in-memory representation of a persisted JTI row.
type ABSToken struct {
	ID        string
	UserID    string
	ProfileID string
	Type      string
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

// AccessResolver resolves the ABS-authenticated user/profile into the same
// effective catalog access filter used by silo's native API.
type AccessResolver interface {
	ResolveABSAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error)
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
	MediaStore     MediaStore
	TokenStore     TokenStore
	CredValidator  ProfileCredentialValidator
	AccessResolver AccessResolver
	// UsernameResolver returns the display username for an ABS principal
	// (userID, profileID) without re-authenticating. Optional; GET /me falls
	// back to the userID when this is nil or returns "". Login gets the
	// display name from the credential validator, but /me only has the token
	// claims, so it needs this to show the real username instead of the id.
	UsernameResolver func(ctx context.Context, userID, profileID string) string
	Config           ConfigProvider
	Publisher        EventPublisher // may be nil
	Recommender      Recommender    // may be nil
	LoginLimiter     *LoginLimiter  // may be nil — one is created if absent
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
	// BookmarkStore persists ABS bookmark rows (migration 148) for the
	// POST/PATCH/DELETE /me/item/{itemId}/bookmark endpoints. May be
	// nil; handlers respond 503 when unset.
	BookmarkStore BookmarkStore
	// CollectionStore persists ABS user-collection rows (migrations 149 + 150).
	// May be nil; handlers respond 503 when unset.
	CollectionStore CollectionStore
	// PlaylistStore persists ABS playlist rows (migrations 151 + 152).
	// May be nil; handlers respond 503 when unset.
	PlaylistStore PlaylistStore
	// SmartCollectionStore persists user_personal_collections rows with
	// collection_type='smart' (migration 156 unified the old
	// abs_smart_collections table into the canonical store).
	// May be nil; handlers respond 503 when unset.
	SmartCollectionStore SmartCollectionStore
	// RSSFeedStore persists abs_rss_feeds rows (migration 155).
	// May be nil; handlers respond 503 when unset.
	RSSFeedStore RSSFeedStore
	// SocketIO is the Socket.io server mounted at /abs/socket.io/. May be nil;
	// the route is only registered when a non-nil value is supplied.
	SocketIO SocketIOServer
	// NativeSessions mirrors ABS playback into Silo's native playback session
	// manager so shared live-session views, limits, and stale-session cleanup
	// see Audiobookshelf-compatible clients. May be nil; ABS playback still
	// functions, but admin live-session visibility is unavailable.
	NativeSessions PlaybackSessionManager
	// NativeSessionSyncer flushes native session-manager state into the shared
	// admin live-session table after ABS play/sync/close events.
	NativeSessionSyncer PlaybackSessionSyncer
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

	// Stage 2: login (body credentials). Real ABS serves /login at root, but
	// clients differ on the prefix — some POST /api/login or /abs/api/login.
	// The rest of the authenticated surface is mounted under both /api and
	// /abs/api, so mount login+refresh under the same set; a client posting
	// /api/login otherwise 404s and surfaces a generic "unknown error".
	for _, prefix := range []string{"", "/api", "/abs/api"} {
		r.Post(prefix+"/login", h.handleLogin)
		// Token rotation — mobile clients call this every ~22h to avoid the
		// 24h access-token interactive re-login trap.
		r.Post(prefix+"/auth/refresh", h.handleRefresh)
	}
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

	// Session-scoped audio streaming (ABS v2.22.0+ DirectPlay). The Android
	// and iOS clients call this WITHOUT a bearer token — the session ID is
	// the capability. Mounted at both /public/session and /abs/public/session
	// for compatibility with clients that pin either prefix.
	for _, prefix := range []string{"", "/abs"} {
		r.Get(prefix+"/public/session/{sid}/track/{idx}", h.handlePublicTrack)
		r.Head(prefix+"/public/session/{sid}/track/{idx}", h.handlePublicTrack)
	}

	// Public RSS feed routes — slug is the capability token, no auth.
	r.Get("/feed/{slug}.xml", h.handlePublicFeed)
	r.Get("/feed/{slug}", h.handlePublicFeed)
	r.Get("/feed/{slug}/file/{ino}", h.handlePublicFeedFile)

	// Server discovery — unauthenticated. Mounted at both /api and the
	// canonical root so curl-style network probes, the official ABS app's
	// connect-server flow, and AudioBooth's saved-server liveness check
	// all land on the same response.
	for _, prefix := range []string{"/abs", "/api"} {
		r.Get(prefix+"/ping", h.handlePing)
		r.Get(prefix+"/healthcheck", h.handleHealthcheck)
		r.Get(prefix+"/init", h.handleInit)
		r.Get(prefix+"/auth-settings", h.handleAuthSettings)
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
			// PATCH alias — AudioBooth and the canonical ABS server use
			// PATCH for the same write; route both methods to the handler.
			r.Patch(prefix+"/me/progress/{libraryItemId}", h.handleSetItemProgress)
			// DELETE /me/progress/{id}       — clear progress (Reset Progress)
			r.Delete(prefix+"/me/progress/{libraryItemId}", h.handleDeleteItemProgress)
			// PATCH /me/progress/{id}/{episodeId} — podcast episode
			// progress; audiobook-only catalog, so this is a stub.
			r.Patch(prefix+"/me/progress/{libraryItemId}/{episodeId}", h.handleSetEpisodeProgress)
			// POST  /session/{sid}/sync      — real ABS heartbeat path
			// (SessionController.sync). The official ABS mobile/web clients
			// POST here; missing it means playback progress never syncs.
			r.Post(prefix+"/session/{sid}/sync", h.handleSessionSync)
			// PATCH /session/{sid}           — silo-native heartbeat alias
			// (kept additive for silo's own clients).
			r.Patch(prefix+"/session/{sid}", h.handleSessionSync)
			// POST  /session/{sid}/close     — finalise the play session
			r.Post(prefix+"/session/{sid}/close", h.handleSessionClose)
			// POST  /session/local          — sync one offline-recorded session
			r.Post(prefix+"/session/local", h.handleSyncLocalSession)
			// POST  /session/local-all      — batch-sync offline-recorded sessions
			r.Post(prefix+"/session/local-all", h.handleSyncLocalSessions)
			// Bookmarks — POST/PATCH both upsert; DELETE is idempotent.
			r.Post(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_created"))
			r.Patch(prefix+"/me/item/{itemId}/bookmark", h.handleUpsertBookmark("bookmark_updated"))
			r.Delete(prefix+"/me/item/{itemId}/bookmark/{time}", h.handleDeleteBookmark)
			// Collections — owner-gated CRUD with cross-user public reads.
			r.Get(prefix+"/collections", h.handleListCollections)
			// Per-library collections list — bookshelf "Collections" tab
			// hits this. Paged envelope with full-shape entries (books[]
			// included) so the cover stack renders.
			r.Get(prefix+"/libraries/{libraryId}/collections", h.handleListLibraryCollections)
			r.Post(prefix+"/collections", h.handleCreateCollection)
			r.Get(prefix+"/collections/{id}", h.handleGetCollection)
			r.Patch(prefix+"/collections/{id}", h.handleUpdateCollection)
			r.Delete(prefix+"/collections/{id}", h.handleDeleteCollection)
			r.Post(prefix+"/collections/{id}/book/{bookId}", h.handleAddCollectionBook)
			r.Delete(prefix+"/collections/{id}/book/{bookId}", h.handleRemoveCollectionBook)
			// Playlists — owner-gated CRUD with cross-user public reads,
			// realtime events on every mutation, batch endpoints.
			r.Get(prefix+"/playlists", h.handleListPlaylists)
			// Per-library playlist list — mobile create-playlist modal
			// loads from here before opening the form. Emits
			// `{results: [...]}` with full-shape entries.
			r.Get(prefix+"/libraries/{libraryId}/playlists", h.handleListLibraryPlaylists)
			r.Post(prefix+"/playlists", h.handleCreatePlaylist)
			r.Get(prefix+"/playlists/{id}", h.handleGetPlaylist)
			r.Patch(prefix+"/playlists/{id}", h.handleUpdatePlaylist)
			r.Delete(prefix+"/playlists/{id}", h.handleDeletePlaylist)
			r.Post(prefix+"/playlists/{id}/item", h.handleAddPlaylistItem)
			r.Post(prefix+"/playlists/{id}/batch/add", h.handleBatchAddPlaylistItems)
			r.Post(prefix+"/playlists/{id}/batch/remove", h.handleBatchRemovePlaylistItems)
			r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}", h.handleRemovePlaylistItem)
			r.Delete(prefix+"/playlists/{id}/item/{libraryItemId}/{episodeId}", h.handleRemovePlaylistEpisode)
			// Smart collections — rule-based dynamic groupings.
			r.Get(prefix+"/me/smart-collections", h.handleListSmartCollections)
			r.Post(prefix+"/me/smart-collections", h.handleCreateSmartCollection)
			r.Get(prefix+"/me/smart-collections/{id}", h.handleGetSmartCollection)
			r.Get(prefix+"/me/smart-collections/{id}/items", h.handleSmartCollectionItems)
			r.Patch(prefix+"/me/smart-collections/{id}", h.handleUpdateSmartCollection)
			r.Delete(prefix+"/me/smart-collections/{id}", h.handleDeleteSmartCollection)
			// Phase 1 close-out: listening stats / author+series / continue / RSS auth.
			r.Get(prefix+"/me/listening-stats", h.handleListeningStats)
			r.Get(prefix+"/me/listening-sessions", h.handleListeningSessions)
			r.Get(prefix+"/me/listening-sessions/{sid}", h.handleListeningSessionDetail)
			r.Get(prefix+"/authors/{id}", h.handleAuthorDetail)
			r.Get(prefix+"/series/{id}", h.handleSeriesDetail)
			r.Get(prefix+"/me/progress/{itemId}/remove-from-continue-listening", h.handleRemoveFromContinueListening)
			r.Get(prefix+"/me/progress/{itemId}/readd-to-continue-listening", h.handleReaddToContinueListening)
			r.Get(prefix+"/feeds", h.handleListRSSFeeds)
			r.Post(prefix+"/feeds/item/{itemId}/open", h.handleOpenItemFeed)
			r.Post(prefix+"/feeds/{id}/close", h.handleCloseFeed)
			// Year-in-review stats — AudioBooth's "Year Stats" widget on the
			// profile screen. Synthesized from AggregateStats today.
			r.Get(prefix+"/me/stats/year/{year}", h.handleYearStats)
			// Ebook surface — stubs until the ebook scanner lands.
			// Mobile clients call these but degrade cleanly on empty/404.
			r.Get(prefix+"/items/{id}/ebook/{fileid}", h.handleEbookFile)
			r.Patch(prefix+"/items/{id}/ebook/{fileid}/status", h.handleEbookStatus)
			// E-reader devices + ebook email delivery — empty list / 503
			// until SMTP integration is wired.
			r.Get(prefix+"/me/ereader-devices", h.handleListEreaderDevices)
			r.Post(prefix+"/emails/send-ebook-to-device", h.handleSendEbookToDevice)
			// Podcast stubs — audiobook-only catalog in v1. Endpoints
			// return empty-but-well-formed shapes so the mobile UI doesn't
			// crash on the podcast surfaces.
			r.Post(prefix+"/podcasts/feed", h.handlePodcastFeed)
			r.Post(prefix+"/items/{libraryItemId}/play/{episodeId}", h.handlePlayEpisode)
			r.Get(prefix+"/libraries/{libraryId}/recent-episodes", h.handleRecentEpisodes)
			r.Get(prefix+"/search/podcast", h.handleSearchPodcast)
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

func (h *Handler) accessFilterForAuth(ctx context.Context, a ctxAuth) (catalog.AccessFilter, error) {
	if h.deps.AccessResolver != nil {
		return h.deps.AccessResolver.ResolveABSAccess(ctx, a.UserID, a.ProfileID)
	}
	filter := catalog.AccessFilter{ProfileID: a.ProfileID}
	if uid, err := strconv.Atoi(a.UserID); err == nil {
		filter.UserID = uid
	}
	return filter, nil
}

func (h *Handler) accessFilterFromRequest(r *http.Request) (catalog.AccessFilter, bool, error) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		return catalog.AccessFilter{}, false, nil
	}
	filter, err := h.accessFilterForAuth(r.Context(), a)
	return filter, true, err
}

func emptyAccessFilter() catalog.AccessFilter {
	return catalog.AccessFilter{}
}

func sameABSPrincipal(a ctxAuth, userID, profileID string) bool {
	return a.UserID == userID && a.ProfileID == profileID
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
		if row.UserID != "" && row.UserID != claims.UserID {
			slog.Debug("abs bearerAuth: token user mismatch", "jti", claims.JTI, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if row.ProfileID != "" && row.ProfileID != claims.ProfileID {
			slog.Debug("abs bearerAuth: token profile mismatch", "jti", claims.JTI, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if row.Type != "" && row.Type != "access" {
			slog.Debug("abs bearerAuth: persisted token type mismatch", "type", row.Type, "path", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if !row.ExpiresAt.IsZero() && time.Now().After(row.ExpiresAt) {
			slog.Debug("abs bearerAuth: persisted token expired", "jti", claims.JTI, "path", r.URL.Path)
			http.Error(w, "token expired", http.StatusUnauthorized)
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
