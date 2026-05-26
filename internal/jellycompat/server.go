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
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Dependencies holds the pluggable pieces used by the compat server.
type Dependencies struct {
	Config           *config.Config
	DB               *pgxpool.Pool
	ClientIPResolver *clientip.Resolver
	HTTPClient       *http.Client
	Now              func() time.Time
	TokenGenerator   func() string
	SessionStore     *SessionStore
	IDCodec          *ResourceIDCodec
	ImageCache       *ImageCache
	DeviceProfiles   *DeviceProfileStore
	PlaybackStore    *PlaybackSessionStore
	LoginResolver    loginResolver
	Authenticator    *Authenticator
	WebFS            fs.FS

	// Direct service dependencies (replaces Client)
	ContentService  ContentService
	UserDataService UserDataService
	AuthService     *auth.Service

	// Autoscan / admin compatibility support.
	APIKeyValidator  apiKeyValidator
	APIKeyUserLoader apiKeyUserLoader
	ScanQueue        scantrigger.Queuer

	// Catalog repos (for ContentService construction)
	BrowseRepo     *catalog.BrowseRepository
	ItemRepo       *catalog.ItemRepository
	SeasonRepo     *catalog.SeasonRepository
	EpisodeRepo    *catalog.EpisodeRepository
	ProviderIDRepo *catalog.ProviderIDRepository
	DetailSvc      *catalog.DetailService
	FolderRepo     *catalog.FolderRepository

	// Person repository
	PersonRepo *catalog.PersonRepository

	// Library poster presigning
	PosterPresigner LibraryPosterPresigner
	PresignTTL      time.Duration

	// Playback
	SessionMgr        SessionManagerInterface
	FileResolver      FilePathResolver
	UserStoreProvider userstore.UserStoreProvider
	AccessFilterFn    AccessFilterResolver
	ProxyPool         *nodepool.ProxyPool
	TranscodePool     *nodepool.TranscodePool
	JWTSecret         string
	Recommender       recommendations.Recommender
	RecWorker         *recommendations.Worker

	// Settings (optional; reads server_settings for watched threshold, etc.)
	SettingsRepo SettingsReader

	// Subtitle support (optional)
	SubtitleRepo subtitles.Repository // optional; downloaded subtitle support
	S3Client     subtitles.S3Client   // optional
	S3Bucket     string               // optional
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
func (s *Server) StartBackgroundTasks(ctx context.Context) {
	if s.deps.DB != nil {
		repo := NewSessionRepository(s.deps.DB)
		StartSessionCleanup(ctx, repo, 1*time.Hour)
	}
}

// NewDependencies fills in sensible defaults for optional compat dependencies.
func NewDependencies(cfg *config.Config) Dependencies {
	return Dependencies{
		Config:         cfg,
		Now:            time.Now,
		TokenGenerator: uuid.NewString,
		HTTPClient:     &http.Client{Timeout: 30 * time.Second},
	}
}
