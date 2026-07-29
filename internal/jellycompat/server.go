package jellycompat

import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/scantrigger"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

// Dependencies holds the pluggable pieces used by the compat server.
type Dependencies struct {
	Config *config.Config
	// AppContext is the process lifecycle context. When set, it bounds the
	// periodic orphan-transcode sweep so it stops on shutdown; nil (tests) makes
	// the sweep a single boot-time run instead of a long-lived ticker.
	AppContext context.Context
	// LiveConfig returns the current hot-reloaded config. May be nil (tests,
	// worker modes); read through CurrentConfig(), which falls back to Config.
	LiveConfig       func() *config.Config
	DB               *pgxpool.Pool
	SecretCipher     *secret.Cipher // at-rest credential cipher (required when DB is set)
	ClientIPResolver *clientip.Resolver
	Now              func() time.Time
	TokenGenerator   func() string
	SessionStore     *SessionStore
	IDCodec          *ResourceIDCodec
	ImageCache       *ImageCache
	DeviceProfiles   *DeviceProfileStore
	PlaybackStore    CompatPlaybackStore
	// RecipeNodeStore hands remote-transcode reconstruction recipes to the
	// control-plane recipe store (Redis) so a restarted transcode node can rebuild
	// a jellycompat session. Optional; nil disables the handoff.
	RecipeNodeStore recipeNodePutter
	LoginResolver   loginResolver
	Authenticator   *Authenticator
	WebFS           fs.FS
	// FrontendFS is the embedded Silo frontend asset filesystem (web/dist),
	// used to serve app-relative artwork such as bundled collection-template
	// posters that have no remote origin. Optional.
	FrontendFS fs.FS
	HTTPClient *http.Client

	// Direct service dependencies (replaces Client)
	ContentService  ContentService
	UserDataService UserDataService
	AuthService     *auth.Service

	// WatchCompletionObserver is notified when a Jellyfin-compat mark-played
	// completes a watch, so fully-watched items leave the watchlist. Optional.
	WatchCompletionObserver watchstate.CompletionObserver

	// Autoscan / admin compatibility support.
	APIKeyValidator  apiKeyValidator
	APIKeyUserLoader apiKeyUserLoader
	ScanQueue        scantrigger.Queuer

	// Catalog repos (for ContentService construction)
	BrowseRepo            *catalog.BrowseRepository
	ItemRepo              *catalog.ItemRepository
	SeasonRepo            *catalog.SeasonRepository
	EpisodeRepo           *catalog.EpisodeRepository
	ProviderIDRepo        *catalog.ProviderIDRepository
	DetailSvc             *catalog.DetailService
	FolderRepo            *catalog.FolderRepository
	CatalogSearchProvider catalog.CatalogSearchProvider

	// Person repository
	PersonRepo *catalog.PersonRepository

	// Library poster presigning
	PosterPresigner LibraryPosterPresigner
	PresignTTL      time.Duration

	// Playback
	SessionMgr SessionManagerInterface
	// SessionSyncer flushes native session-manager state into the shared
	// admin live-session table right after compat playback starts/stops, so
	// the activity dashboard doesn't wait for the periodic reconciler tick.
	// Optional.
	SessionSyncer          PlaybackSessionSyncer
	FileResolver           FilePathResolver
	UserStoreProvider      userstore.UserStoreProvider
	WatchScrobbler         PlaybackWatchScrobbler
	StableIdentityResolver watchsync.ScrobbleIdentityResolver
	AccessFilterFn         AccessFilterResolver
	NodePlanner            nodepool.SessionPlanner
	JWTSecret              string
	Recommender            recommendations.Recommender
	RecWorker              *recommendations.Worker

	// Settings (optional; reads server_settings for watched threshold, etc.)
	SettingsRepo SettingsReader

	// Subtitle support (optional)
	SubtitleRepo subtitles.Repository // optional; downloaded subtitle support
	S3Client     subtitles.S3Client   // optional
	S3Bucket     string               // optional
}

// CurrentConfig returns the live config when hot reload is wired, falling
// back to the startup snapshot otherwise.
func (d *Dependencies) CurrentConfig() *config.Config {
	if d.LiveConfig != nil {
		if cfg := d.LiveConfig(); cfg != nil {
			return cfg
		}
	}
	return d.Config
}

// Server wraps the compat HTTP handler.
type Server struct {
	cfg     *config.Config
	handler http.Handler
	deps    Dependencies
}

// NewServer creates a new Jellyfin-compatibility server.
func NewServer(cfg *config.Config) *Server {
	return NewServerWithDependencies(NewDependencies(cfg))
}

// NewServerWithDependencies creates a new Jellyfin-compatibility server using explicit dependencies.
func NewServerWithDependencies(deps Dependencies) *Server {
	deps = withDefaults(deps)
	return &Server{
		cfg:     deps.Config,
		handler: NewRouter(deps),
		deps:    deps,
	}
}

// Handler returns the compat HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.handler
}

// HTTPServer builds an http.Server using the compat listen address.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:    s.cfg.JellyfinCompat.Listen,
		Handler: s.handler,
	}
}

// Dependencies returns the resolved dependency set.
func (s *Server) Dependencies() Dependencies {
	return s.deps
}

// SessionStore returns the compat session store for external revocation hooks.
func (s *Server) SessionStore() *SessionStore {
	return s.deps.SessionStore
}

// StartBackgroundTasks starts background goroutines tied to the server lifecycle.
// Call this once after constructing the server; goroutines stop when ctx is cancelled.
// The returned channel closes after the initial terminal recovery scan so
// callers can order broader stale-session sweeps behind authoritative compat
// recovery without delaying HTTP startup.
func (s *Server) StartBackgroundTasks(ctx context.Context) <-chan struct{} {
	if s.deps.DB != nil {
		repo := NewSessionRepository(s.deps.DB, s.deps.SecretCipher)
		StartSessionCleanupWithPlaybackStore(ctx, repo, s.deps.PlaybackStore, 1*time.Hour)
	}
	return StartTerminalScrobbleRecovery(ctx, s.deps.PlaybackStore, s.deps.WatchScrobbler, 30*time.Second)
}

// NewDependencies fills in sensible defaults for optional compat dependencies.
func NewDependencies(cfg *config.Config) Dependencies {
	return Dependencies{
		Config:         cfg,
		Now:            time.Now,
		TokenGenerator: uuid.NewString,
	}
}
