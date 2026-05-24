// Package api provides the HTTP router and middleware setup for Silo.
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/download"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	metatrakt "github.com/Silo-Server/silo-server/internal/metadata/trakt"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/opensubtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subdl"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsource"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/taskmanager/repository"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	watchtrakt "github.com/Silo-Server/silo-server/internal/watchsync/providers/trakt"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

// Dependencies holds all shared dependencies that handlers need.
type Dependencies struct {
	Config                       *config.Config
	BootstrapSensitiveConfigured map[string]bool
	BootstrapSensitiveValues     map[string]string
	AppContext                   context.Context
	DB                           *pgxpool.Pool
	FrontendFS                   fs.FS
	S3Public                     *s3client.Client                 // public assets bucket client (may be nil)
	S3Private                    *s3client.Client                 // private internal bucket client (may be nil)
	S3UserDB                     *s3client.Client                 // user-db bucket client (may be nil)
	FolderRepo                   *catalog.FolderRepository        // media folder repository (may be nil)
	FileRepo                     *scanner.FileRepository          // media file repository (may be nil)
	Scanner                      *scanner.Scanner                 // scanner instance (may be nil)
	LibraryIngester              *libraryingest.Executor          // shared library ingest executor (may be nil)
	ProbeEnsurer                 handlers.PlaybackProbeEnsurer    // on-demand probe repair for playback/detail (may be nil)
	UserStoreProvider            userstore.UserStoreProvider      // user store provider (may be nil)
	SessionMgr                   *playback.SessionManager         // playback session manager (may be nil)
	SkippedRootRepo              *metadata.SkippedRootRepository  // skipped root repository (may be nil)
	StaleIDRepo                  *metadata.StaleMediaIDRepository // stale media ID repository (may be nil)
	Refresher                    handlers.AdminMetadataRefresher  // metadata refresher (may be nil)
	NodeRepo                     *nodepool.Repository             // stream node repository (may be nil)
	ProxyPool                    *nodepool.ProxyPool              // proxy node pool (may be nil)
	TranscodePool                *nodepool.TranscodePool          // transcode node pool (may be nil)
	SessionSyncer                handlers.PlaybackSessionSyncer   // optional; immediate playback session sync trigger
	EventBus                     cache.EventBus
	AdminStatsProvider           handlers.AdminStatsSource
	Recommender                  recommendations.Recommender // nil when disabled
	RecWorker                    *recommendations.Worker     // nil when disabled
	RatingsRepo                  *catalog.RatingsRepo
	PersonRepo                   *catalog.PersonRepository
	PersonRefreshQueue           handlers.PersonRefreshQueue
	PersonRefresher              handlers.PersonRefresher
	RateLimitMW                  *ratelimit.Middleware
	ClientIPResolver             *clientip.Resolver
	NodeID                       string
	LogStreamHub                 *logstream.Hub
	RealtimeHub                  *notifications.Hub
	EventsHub                    *evt.Hub
	ScanRegistry                 *evt.ScanRegistry
	LibraryScanQueue             *scanqueue.Service
	ActivityLogWriter            activitylog.Writer
	ActivityLogRepo              *activitylog.Repo
	OpsLogRepo                   *opslog.Repo
	FFmpegLogSink                playback.FFmpegLogSink
	RedisClient                  *redis.Client            // for session listing (may be nil)
	TaskManager                  *taskmanager.TaskManager // task manager (may be nil)
	IntroRepository              *intromarkers.Repository
	IntroAnalyzer                *intromarkers.Analyzer
	MarkerRegistry               *markers.Registry
	MarkerResolver               markers.ExternalIDResolver
	WatchProviderService         handlers.WatchProviderService
	PluginService                *plugins.Service
	PluginHTTPProxy              *plugins.HTTPProxy
	PluginUserConfig             *plugins.UserConfigStore
	AuthProviders                []auth.RegisteredProvider
	// PublicURL is the externally-reachable origin (scheme + host) for this
	// silo instance. Used to build redirect_uri values handed to OAuth
	// IdPs. Empty disables the /oauth/{install_id}/{init,callback} routes.
	PublicURL              string
	ImageResolver          catalog.ImageResolver             // plugin-based image URL resolver (may be nil)
	PluginImageResolver    *metadata.PluginImageResolver     // concrete resolver for runtime source registration (may be nil)
	MetadataService        handlers.MatchMetadataService     // metadata search+process (may be nil)
	CollectionService      *catalog.LibraryCollectionService // collection service (may be nil)
	ChapterThumbnailQueuer catalog.ChapterThumbnailQueuer
	PlaybackRealtimeHub    *playback.RealtimeHub
	OnUserSessionsRevoked  func(ctx context.Context, userID int)
	OnServerSettingUpdated func(ctx context.Context, key, value string)

	// UserCollectionSync handles per-profile imported collections (TMDB /
	// Trakt / MDBList) — the user-facing analogue of CollectionService.
	UserCollectionSync      *usercollections.Service
	UserCollectionScheduler *usercollections.Scheduler

	// MDBListClient is used by user-facing list discovery endpoints
	// (search/top). May be nil; the handlers report "not configured" in
	// that case rather than failing.
	MDBListClient *mdblist.Client
}

// NewRouter creates a chi.Router with all middleware and routes mounted
// under /api/v1/.
func NewRouter(deps Dependencies) chi.Router {
	r := chi.NewRouter()

	// Standard middleware.
	r.Use(middleware.RequestID)

	// Client IP resolution must run before request logging.
	if deps.ClientIPResolver != nil {
		r.Use(clientip.Middleware(deps.ClientIPResolver))
	}

	r.Use(apimw.RequestLogger(deps.NodeID))
	r.Use(middleware.Recoverer)
	r.Use(apimw.Metrics)

	// Activity logging (before auth — captures all requests including failed auth).
	if deps.ActivityLogWriter != nil {
		r.Use(activitylog.NewMiddleware(deps.ActivityLogWriter, deps.NodeID))
	}

	// Build the readiness handler with optional S3 check.
	var s3Checker handlers.S3HealthChecker
	if deps.S3Public != nil {
		s3Checker = deps.S3Public
	} else if deps.S3Private != nil {
		s3Checker = deps.S3Private
	}

	// PG pinger: use the pool if available.
	var pgPinger handlers.PGPinger
	if deps.DB != nil {
		pgPinger = deps.DB
	}

	readyHandler := handlers.NewReadyHandler(pgPinger, s3Checker)

	// Health handler advertises the server's identity so multi-server
	// clients can display a friendly name. Falls back to empty strings
	// if config is absent (tests, minimal fixtures); JSON omits empties.
	var healthServerName, healthServerID string
	if deps.Config != nil {
		healthServerName = deps.Config.JellyfinCompat.ServerName
		healthServerID = deps.Config.JellyfinCompat.ServerID
	}
	healthHandler := handlers.NewHealthHandler(healthServerName, healthServerID)

	// Build server settings repo if DB is available (needed by auth and admin).
	var settingsRepo *catalog.ServerSettingsRepo
	if deps.DB != nil {
		settingsRepo = catalog.NewServerSettingsRepo(deps.DB)
	}

	// Build auth handler and auth middleware if DB and config are available.
	var userRepo *auth.UserRepository
	var inviteCodeRepo *auth.InviteCodeRepository
	var apiKeyRepo *auth.APIKeyRepository
	var authService *auth.Service
	var authHandler *handlers.AuthHandler
	var authMiddleware *apimw.AuthMiddleware
	var viewerAccessMiddleware *apimw.ViewerAccessMiddleware
	var viewerResolver *access.Resolver
	var profileTokenService *access.ProfileTokenService
	var jwtService *auth.JWTService
	var sessionRepo *auth.SessionRepository
	var deviceLoginService *auth.DeviceLoginService
	if deps.DB != nil && deps.Config != nil {
		userRepo = auth.NewUserRepository(deps.DB)
		sessionRepo = auth.NewSessionRepository(deps.DB)
		inviteCodeRepo = auth.NewInviteCodeRepository(deps.DB)
		apiKeyRepo = auth.NewAPIKeyRepository(deps.DB)
		jwtService = auth.NewJWTService(
			deps.Config.Auth.JWTSecret,
			deps.Config.Auth.AccessTokenExpiry,
			deps.Config.Auth.RefreshTokenExpiry,
		)
		provider := auth.NewLocalProvider(userRepo, sessionRepo)
		authService = auth.NewService(
			provider,
			jwtService,
			sessionRepo,
			userRepo,
			inviteCodeRepo,
			settingsRepo,
			deps.UserStoreProvider,
		)
		for _, registration := range deps.AuthProviders {
			authService.RegisterProvider(registration.Info, registration.Provider)
		}
		deviceLoginService = auth.NewDeviceLoginService(deps.DB, userRepo, jwtService, sessionRepo)
		authHandler = handlers.NewAuthHandler(authService, jwtService, deviceLoginService)
		authMiddleware = apimw.NewAuthMiddleware(jwtService, sessionRepo, apiKeyRepo, userRepo)
		profileTokenService = access.NewProfileTokenService(deps.Config.Auth.JWTSecret, 0)
		if deps.UserStoreProvider != nil {
			viewerResolver = access.NewResolver(userRepo, deps.UserStoreProvider, profileTokenService)
			viewerAccessMiddleware = apimw.NewViewerAccessMiddleware(viewerResolver)
		}
	}
	if deps.SessionMgr != nil && userRepo != nil {
		deps.SessionMgr.SetLimitProvider(func(ctx context.Context, userID int) (playback.SessionLimits, error) {
			user, err := userRepo.GetByID(ctx, userID)
			if err != nil {
				return playback.SessionLimits{}, err
			}
			return playback.SessionLimits{
				MaxStreams:    user.MaxStreams,
				MaxTranscodes: user.MaxTranscodes,
			}, nil
		})
	}

	// Build demo guard middleware if server settings are available.
	var demoGuard *apimw.DemoGuard
	if settingsRepo != nil {
		demoGuard = apimw.NewDemoGuard(settingsRepo)
	}

	// Build library handler if folder repo is available.
	var libraryHandler *handlers.LibraryHandler
	if deps.FolderRepo != nil {
		libraryHandler = handlers.NewLibraryHandler(deps.FolderRepo, deps.LibraryIngester, userRepo, deps.DB, deps.Refresher, deps.AppContext)
		libraryHandler.EventBus = deps.EventBus
		libraryHandler.EventsHub = deps.EventsHub
		libraryHandler.ScanRegistry = deps.ScanRegistry
		libraryHandler.ScanQueue = deps.LibraryScanQueue
		if deps.DB != nil {
			libraryHandler.JobRepo = adminjob.NewRepository(deps.DB)
		}

		// Library poster uploads are writable client-facing assets, so they
		// belong in the public assets bucket.
		if deps.S3Public != nil {
			libraryHandler.S3Meta = deps.S3Public
		}

		// Wire provider chain repos for per-library provider priority management.
		if deps.DB != nil && deps.PluginService != nil {
			libraryHandler.ChainRepo = metadata.NewChainRepository(deps.DB)
			libraryHandler.PluginInstallations = plugins.NewInstallationStore(deps.DB)
		}
		if invalidator, ok := deps.MetadataService.(interface{ InvalidateChainCache() }); ok {
			libraryHandler.SetChainCacheInvalidator(invalidator)
		}
		if deps.SkippedRootRepo != nil {
			libraryHandler.SkippedRootRepo = deps.SkippedRootRepo
		}
		if deps.StaleIDRepo != nil {
			libraryHandler.StaleIDRepo = deps.StaleIDRepo
		}
		if deps.DB != nil {
			libraryHandler.SectionRepo = sections.NewRepository(deps.DB)
		}
		if deps.UserStoreProvider != nil {
			libraryHandler.StoreProvider = deps.UserStoreProvider
		}
	}

	// Build ratings repo if DB is available. Use dep-injected repo when provided
	// (e.g. already constructed in main.go for the recommendations engine).
	var ratingsRepo *catalog.RatingsRepo
	if deps.RatingsRepo != nil {
		ratingsRepo = deps.RatingsRepo
	} else if deps.DB != nil {
		ratingsRepo = catalog.NewRatingsRepo(deps.DB)
	}

	// Build browse/search/items handlers if DB is available.
	var itemsHandler *handlers.ItemsHandler
	var catalogResourceHandler *handlers.CatalogResourceHandler
	var catalogHandler *handlers.CatalogHandler
	var peopleHandler *handlers.PeopleHandler
	var itemRepo *catalog.ItemRepository
	var episodeRepo *catalog.EpisodeRepository
	var providerIDRepo *catalog.ProviderIDRepository
	var seasonRepo *catalog.SeasonRepository
	var detailSvc *catalog.DetailService
	var calendarRepo *catalog.CalendarRepository
	var webhookSyncHandler *handlers.WebhookSyncHandler
	if deps.DB != nil {
		browseRepo := catalog.NewBrowseRepository(deps.DB)
		itemRepo = catalog.NewItemRepository(deps.DB)
		episodeRepo = catalog.NewEpisodeRepository(deps.DB)
		providerIDRepo = catalog.NewProviderIDRepository(deps.DB)
		calendarRepo = catalog.NewCalendarRepository(deps.DB)

		var fileFetcher catalog.FileVersionFetcher
		if deps.FileRepo != nil {
			fileFetcher = deps.FileRepo
		}

		seasonRepo = catalog.NewSeasonRepository(deps.DB)
		folderRepo := catalog.NewFolderRepository(deps.DB)

		var episodeFileProvider handlers.EpisodeFileProvider
		if deps.FileRepo != nil {
			episodeFileProvider = deps.FileRepo
		}

		rootClaimRepo := catalog.NewRootClaimRepository(deps.DB)
		groupClaimRepo := catalog.NewGroupClaimRepository(deps.DB)
		detailSvc = catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, deps.PersonRepo, fileFetcher)
		detailSvc.SetFolderRepository(folderRepo)
		detailSvc.SetRootClaimRepository(rootClaimRepo)
		detailSvc.SetGroupClaimRepository(groupClaimRepo)
		detailSvc.SetProbeEnsurer(deps.ProbeEnsurer)
		detailSvc.SetChapterThumbnailQueuer(deps.ChapterThumbnailQueuer)
		if deps.ImageResolver != nil {
			detailSvc.SetImageResolver(deps.ImageResolver)
		}
		detailSvc.SetUserStoreProvider(deps.UserStoreProvider)
		itemsHandler = handlers.NewItemsHandler(
			browseRepo,
			itemRepo,
			episodeRepo,
			seasonRepo,
			ratingsRepo,
			episodeFileProvider,
			deps.UserStoreProvider,
			detailSvc,
			providerIDRepo,
		)
		itemsHandler.EventsHub = deps.EventsHub
		itemsHandler.UserRepo = userRepo
		if requester, ok := deps.MetadataService.(handlers.MetadataRefreshRequester); ok {
			itemsHandler.SetMetadataRefreshRequester(requester)
		}
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalWatchEventDispatcher); ok {
			itemsHandler.SetLocalWatchEventDispatcher(dispatcher)
		}
		catalogResourceHandler = handlers.NewCatalogResourceHandler(itemsHandler)
		catalogHandler = handlers.NewCatalogHandler(
			catalog.NewCatalogResolver(browseRepo, itemRepo).
				WithEpisodeRepository(episodeRepo).
				WithUserStoreProvider(deps.UserStoreProvider),
			itemsHandler,
		)

		if deps.PersonRepo != nil {
			peopleHandler = handlers.NewPeopleHandler(deps.PersonRepo, browseRepo, itemRepo, detailSvc)
			peopleHandler.SetItemsHandler(itemsHandler)
			peopleHandler.SetRefreshQueue(deps.PersonRefreshQueue)
			peopleHandler.SetRefreshService(deps.PersonRefresher)
		}
	}

	// Build profile/personal data handlers if UserStoreProvider is available.
	var profileHandler *handlers.ProfileHandler
	var personalDataHandler *handlers.PersonalDataHandler
	var progressHandler *handlers.ProgressHandler
	var collectionHandler *handlers.CollectionHandler
	var settingsHandler *handlers.SettingsHandler
	var homeDismissalHandler *handlers.HomeDismissalHandler
	var subtitlePrefHandler *handlers.SubtitlePrefHandler
	var audioPrefHandler *handlers.AudioPrefHandler
	var libraryPlaybackPrefHandler *handlers.LibraryPlaybackPrefHandler
	var watchProviderHandler *handlers.WatchProviderHandler
	var playbackSessionsLoader *handlers.PlaybackSessionsLoader
	if deps.DB != nil {
		playbackSessionsLoader = handlers.NewPlaybackSessionsLoader(deps.DB, deps.UserStoreProvider, detailSvc)
	}

	if deps.UserStoreProvider != nil {
		profileHandler = handlers.NewProfileHandler(deps.UserStoreProvider)
		profileHandler.UserRepo = userRepo
		profileHandler.ProfileTokens = profileTokenService
		profileHandler.AvatarStore = deps.S3Private
		profileHandler.SessionsReader = playbackSessionsLoader
		personalDataHandler = handlers.NewPersonalDataHandler(deps.UserStoreProvider, itemRepo)
		if detailSvc != nil {
			personalDataHandler.SetDetailService(detailSvc)
		}
		personalDataHandler.SetEpisodeRepo(episodeRepo)
		personalDataHandler.SetSeasonRepo(seasonRepo)
		personalDataHandler.EventsHub = deps.EventsHub
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalFavoriteEventDispatcher); ok {
			personalDataHandler.SetLocalFavoriteEventDispatcher(dispatcher)
		}
		progressHandler = handlers.NewProgressHandler(deps.UserStoreProvider)
		progressHandler.EventsHub = deps.EventsHub
		if settingsRepo != nil {
			progressHandler.SettingsRepo = settingsRepo
		}
		if deps.DB != nil {
			progressHandler.LibraryLookup = catalog.NewLibraryItemRepository(deps.DB)
		}
		collectionHandler = handlers.NewCollectionHandler(deps.UserStoreProvider)
		if deps.DB != nil {
			collectionHandler.Executor = &catalog.QueryExecutor{Pool: deps.DB}
		}
		if deps.S3Public != nil {
			collectionHandler.S3GP = deps.S3Public
			collectionHandler.PresignTTL = 4 * time.Hour
		}
		settingsHandler = handlers.NewSettingsHandler(deps.UserStoreProvider)
		if settingsRepo != nil {
			settingsHandler.SetServerSettings(settingsRepo)
		}
		homeDismissalHandler = handlers.NewHomeDismissalHandler(deps.UserStoreProvider)
		subtitlePrefHandler = handlers.NewSubtitlePrefHandler(deps.UserStoreProvider)
		audioPrefHandler = handlers.NewAudioPrefHandler(deps.UserStoreProvider)
		libraryPlaybackPrefHandler = handlers.NewLibraryPlaybackPrefHandler(deps.UserStoreProvider)
		if deps.FolderRepo != nil {
			libraryPlaybackPrefHandler.SetLibraryLookup(deps.FolderRepo)
		} else if deps.DB != nil {
			libraryPlaybackPrefHandler.SetLibraryLookup(catalog.NewFolderRepository(deps.DB))
		}
	}
	if deps.WatchProviderService != nil {
		watchProviderHandler = handlers.NewWatchProviderHandler(deps.WatchProviderService)
	}

	// Build ratings handler if both repo and itemRepo are available.
	var ratingsHandler *handlers.RatingsHandler
	var recsRepoForStale *recommendations.Repo
	if ratingsRepo != nil && itemRepo != nil {
		ratingsHandler = handlers.NewRatingsHandler(ratingsRepo, itemRepo)
		if deps.DB != nil {
			recsRepoForStale = recommendations.NewRepo(deps.DB)
			ratingsHandler.SetProfileStaler(recsRepoForStale)
			ratingsHandler.SetProfileRefreshRequester(deps.RecWorker)
			if personalDataHandler != nil {
				personalDataHandler.SetProfileStaler(recsRepoForStale)
				personalDataHandler.SetProfileRefreshRequester(deps.RecWorker)
			}
			if progressHandler != nil {
				progressHandler.SetProfileStaler(recsRepoForStale)
				progressHandler.SetProfileRefreshRequester(deps.RecWorker)
			}
			if itemsHandler != nil {
				itemsHandler.SetProfileStaler(recsRepoForStale)
				itemsHandler.SetProfileRefreshRequester(deps.RecWorker)
			}
		}
	}

	// Create subtitleRepo early — only needs DB, shared with playback handler and subtitle search handler.
	var subtitleRepo *subtitles.PgRepository
	if deps.DB != nil {
		subtitleRepo = subtitles.NewPgRepository(deps.DB)
	}

	// Build playback handler if session manager is available.
	var playbackHandler *handlers.PlaybackHandler
	var adminPlaybackControlHandler *handlers.AdminPlaybackControlHandler
	var streamHandler *handlers.StreamHandler
	var watchTogetherHandler *handlers.WatchTogetherHandler
	if deps.SessionMgr != nil {
		var playbackAdminStore handlers.PlaybackAdminStore
		if deps.DB != nil {
			playbackAdminStore = handlers.NewPGPlaybackAdminStore(deps.DB, deps.EventsHub)
		}
		if deps.FileRepo != nil {
			playbackHandler = handlers.NewPlaybackHandler(deps.SessionMgr, deps.FileRepo)
			streamHandler = handlers.NewStreamHandler(deps.SessionMgr, deps.FileRepo)
		} else {
			playbackHandler = handlers.NewPlaybackHandler(deps.SessionMgr)
		}

		// Wire UserStoreProvider for progress/history persistence.
		if deps.UserStoreProvider != nil {
			playbackHandler.StoreProvider = deps.UserStoreProvider
		}
		playbackHandler.StableIdentityResolver = watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo)
		if scrobbler, ok := deps.WatchProviderService.(handlers.PlaybackWatchScrobbler); ok {
			playbackHandler.WatchScrobbler = scrobbler
		}
		playbackHandler.AdminStore = playbackAdminStore
		playbackHandler.EventsHub = deps.EventsHub
		if deps.FileRepo != nil {
			playbackHandler.MissingMarker = deps.FileRepo
		}
		if deps.SessionSyncer != nil {
			playbackHandler.SessionSyncer = deps.SessionSyncer
		}
		if streamHandler != nil {
			streamHandler.AdminStore = playbackAdminStore
			streamHandler.EventsHub = deps.EventsHub
			streamHandler.SessionSyncer = deps.SessionSyncer
			if deps.FileRepo != nil {
				streamHandler.MissingMarker = deps.FileRepo
			}
		}

		// Wire optional proxy/transcode pools and JWT secret for node-aware stream URLs.
		if deps.ProxyPool != nil {
			playbackHandler.ProxyPool = deps.ProxyPool
		}
		if deps.TranscodePool != nil {
			playbackHandler.TranscodePool = deps.TranscodePool
		}
		if deps.Config != nil && deps.Config.Auth.JWTSecret != "" {
			playbackHandler.JWTSecret = deps.Config.Auth.JWTSecret
		}
		if deps.Config != nil {
			playbackHandler.FFmpegPath = deps.Config.Playback.FFmpegPath
			playbackHandler.HWAccel = deps.Config.Playback.HWAccel
			playbackHandler.TranscodeDir = deps.Config.Playback.TranscodeDir
			if cleaned, err := playbackHandler.CleanupOrphanedTranscodes(); err != nil {
				slog.Warn("playback transcode cleanup failed", "dir", playbackHandler.TranscodeDir, "error", err)
			} else if cleaned > 0 {
				slog.Info("playback transcode cleanup removed orphaned dirs", "dir", playbackHandler.TranscodeDir, "count", cleaned)
			}
		}
		playbackHandler.ProbeEnsurer = deps.ProbeEnsurer
		playbackHandler.ChapterThumbnailQueuer = deps.ChapterThumbnailQueuer
		if settingsRepo != nil {
			playbackHandler.SettingsRepo = settingsRepo
		}
		if deps.FileRepo != nil {
			playbackHandler.FileVersionFetcher = deps.FileRepo
		}
		if subtitleRepo != nil {
			playbackHandler.SubtitleRepo = subtitleRepo
		}
		if recsRepoForStale != nil {
			playbackHandler.SetProfileStaler(recsRepoForStale)
			playbackHandler.SetProfileRefreshRequester(deps.RecWorker)
		}

		realtimeHub := deps.PlaybackRealtimeHub
		if realtimeHub == nil {
			realtimeHub = playback.NewRealtimeHub()
		}
		commandTracker := playback.NewCommandTracker()
		playbackHandler.RealtimeHub = realtimeHub
		playbackHandler.CommandTracker = commandTracker
		playbackHandler.CommandDispatcher = playback.NewCommandDispatcher(deps.SessionMgr, realtimeHub, commandTracker)
		playbackHandler.IntroAnalyzer = deps.IntroAnalyzer
		playbackHandler.IntroRepository = deps.IntroRepository
		playbackHandler.MarkerRegistry = deps.MarkerRegistry
		playbackHandler.MarkerResolver = deps.MarkerResolver
		if deps.FileRepo != nil {
			playbackHandler.MarkerUpserter = deps.FileRepo
		}
		playbackHandler.MarkerUpdateNotifier = playback.NewMarkerUpdateNotifier(deps.SessionMgr, realtimeHub)
		adminPlaybackControlHandler = handlers.NewAdminPlaybackControlHandler(playbackHandler)

		if deps.DB != nil && deps.FileRepo != nil && viewerResolver != nil && deps.Config != nil && detailSvc != nil {
			roomTokenService := watchtogether.NewRoomTokenService(deps.Config.Auth.JWTSecret, 24*time.Hour)
			watchTogetherHandler = handlers.NewWatchTogetherHandler(
				watchtogether.NewService(
					watchtogether.NewRepository(deps.DB),
					deps.SessionMgr,
					deps.FileRepo,
					playbackHandler.CommandDispatcher,
					watchtogether.NewCatalogSelectionResolver(detailSvc),
					watchtogether.NewSuggestionRepository(deps.DB),
				),
				viewerResolver,
				roomTokenService,
			)
		}
	}

	// Wire subtitle repo and S3 client onto streamHandler for S3-stored subtitle serving.
	if streamHandler != nil && subtitleRepo != nil && deps.S3Public != nil {
		streamHandler.SubtitleRepo = subtitleRepo
		streamHandler.S3Client = deps.S3Public
		streamHandler.S3Bucket = deps.S3Public.Bucket()
	}
	if streamHandler != nil && deps.Config != nil {
		streamHandler.FFmpegPath = deps.Config.Playback.FFmpegPath
	}

	// Build admin handler if we have a user repo.
	var adminHandler *handlers.AdminHandler
	var catalogSeedHandler *handlers.CatalogSeedHandler
	var adminJobsHandler *handlers.AdminJobsHandler
	if userRepo != nil {
		adminHandler = handlers.NewAdminHandler(userRepo, deps.DB, deps.UserStoreProvider)
		adminHandler.SessionsLoader = playbackSessionsLoader
		adminHandler.DetailSvc = detailSvc
		adminHandler.EventBus = deps.EventBus
		adminHandler.ImpersonationService = authService
		adminHandler.StatsSource = deps.AdminStatsProvider
		adminHandler.RealtimeHub = deps.RealtimeHub
		adminHandler.BootstrapSensitiveConfigured = deps.BootstrapSensitiveConfigured
		adminHandler.BootstrapSensitiveValues = deps.BootstrapSensitiveValues
		if settingsRepo != nil {
			adminHandler.SettingsRepo = settingsRepo
		}
		if deps.OnUserSessionsRevoked != nil {
			adminHandler.OnUserSessionsRevoked = deps.OnUserSessionsRevoked
		}
		if deps.OnServerSettingUpdated != nil {
			adminHandler.OnServerSettingUpdated = deps.OnServerSettingUpdated
		}
	}
	if deps.DB != nil {
		jobRepo := adminjob.NewRepository(deps.DB)
		// Avoid wrapping a nil *s3client.Client in a non-nil interface;
		// handlers rely on interface-nil checks to gate S3 features.
		var privateStore handlers.CatalogSeedArtifactStore
		if deps.S3Private != nil {
			privateStore = deps.S3Private
		}
		catalogSeedHandler = handlers.NewCatalogSeedHandler(catalogseed.NewService(deps.DB, deps.PersonRepo, recommendations.NewRepo(deps.DB)), jobRepo, privateStore)
		catalogSeedHandler.RealtimeHub = deps.RealtimeHub
		adminJobsHandler = handlers.NewAdminJobsHandler(jobRepo, privateStore)
		if adminHandler != nil && deps.FolderRepo != nil && deps.FileRepo != nil && itemRepo != nil && episodeRepo != nil {
			adminHandler.JobRepo = jobRepo
			adminHandler.ItemRefreshResolver = adminjob.NewItemRefreshResolver(
				itemRepo,
				catalog.NewSeasonRepository(deps.DB),
				episodeRepo,
				deps.FolderRepo,
				deps.FileRepo,
			)
		}
	}

	// Build admin match handler if metadata service and item repo are available.
	var adminMatchHandler *handlers.AdminMatchHandler
	if deps.MetadataService != nil && itemRepo != nil && deps.DB != nil {
		adminMatchHandler = handlers.NewAdminMatchHandler(
			itemRepo,
			&handlers.PoolFolderLookup{Pool: deps.DB},
			deps.MetadataService,
		)
	}

	// Build admin image handler for poster/backdrop/logo selection.
	var adminImageHandler *handlers.AdminImageHandler
	if imageSvc, ok := deps.MetadataService.(handlers.ImageService); ok && itemRepo != nil && seasonRepo != nil && episodeRepo != nil && deps.DB != nil && detailSvc != nil {
		adminImageHandler = handlers.NewAdminImageHandler(
			itemRepo,
			seasonRepo,
			episodeRepo,
			&handlers.PoolFolderLookup{Pool: deps.DB},
			imageSvc,
			deps.PluginImageResolver,
			detailSvc,
		)
	}

	var adminIntroHandler *handlers.AdminIntroHandler
	if deps.IntroAnalyzer != nil && deps.IntroRepository != nil {
		adminIntroHandler = handlers.NewAdminIntroHandler(
			deps.IntroAnalyzer,
			deps.IntroRepository,
			deps.AppContext,
			slog.Default(),
		)
		adminIntroHandler.Settings = settingsRepo
		adminIntroHandler.FileResolver = deps.FileRepo
		if playbackHandler != nil {
			adminIntroHandler.MarkerUpdateNotifier = playbackHandler.MarkerUpdateNotifier
		}
	}

	// Admin subtitle config handler only needs the DB repo — no S3 required.
	var adminSubtitleHandler *handlers.AdminSubtitleHandler
	if subtitleRepo != nil {
		adminSubtitleHandler = handlers.NewAdminSubtitleHandler(subtitleRepo)
	}

	// Build subtitle search handler if we have DB and S3.
	var subtitleSearchHandler *handlers.SubtitleSearchHandler
	if deps.DB != nil && deps.S3Public != nil && subtitleRepo != nil {
		subtitleManager := subtitles.NewManager(subtitleRepo, deps.S3Public, deps.S3Public.Bucket())

		// Load provider configs from DB and register enabled providers.
		providerConfigs, _ := subtitleRepo.ListProviderConfigs(deps.AppContext)
		for _, cfg := range providerConfigs {
			if !cfg.Enabled {
				continue
			}
			switch cfg.ProviderName {
			case "opensubtitles":
				if cfg.Username == "" || cfg.Password == "" {
					continue
				}
				subtitleManager.RegisterProvider(opensubtitles.New(opensubtitles.Config{
					Username: cfg.Username,
					Password: cfg.Password,
				}))
			case "subdl":
				if cfg.APIKey == "" {
					continue
				}
				subtitleManager.RegisterProvider(subdl.New(subdl.Config{APIKey: cfg.APIKey}))
			case "subsource":
				if cfg.APIKey == "" {
					continue
				}
				subtitleManager.RegisterProvider(subsource.New(subsource.Config{APIKey: cfg.APIKey}))
			}
		}

		mediaResolver := &pgSubtitleMediaResolver{pool: deps.DB}
		subtitleSearchHandler = handlers.NewSubtitleSearchHandler(subtitleManager, subtitleRepo, mediaResolver)
	}

	// Build section handler if DB is available.
	var sectionHandler *handlers.SectionHandler
	var sectionSettingsHandler *handlers.SectionSettingsHandler
	var sectionBulkHandler *handlers.SectionBulkHandler
	var libraryCollectionHandler *handlers.LibraryCollectionHandler
	var libraryCollectionGroupHandler *handlers.LibraryCollectionGroupHandler
	libraryCollectionService := deps.CollectionService
	if deps.DB != nil {
		sectionRepo := sections.NewRepository(deps.DB)
		sectionBulkHandler = &handlers.SectionBulkHandler{Repo: sectionRepo}
		sectionFetcher := sections.NewFetcher(deps.DB)
		sectionFetcher.StoreProvider = deps.UserStoreProvider
		sectionFetcher.CollectionRepo = catalog.NewLibraryCollectionRepository(deps.DB)
		sectionFetcher.NextUpRepo = catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
		if deps.DB != nil {
			sectionFetcher.RecommendationRepo = recommendations.NewRepo(deps.DB)
			if ratingsRepo != nil {
				sectionFetcher.RecommendationReader = recommendations.NewReader(sectionFetcher.RecommendationRepo, ratingsRepo, deps.RecWorker, deps.UserStoreProvider)
			}
		}
		sections.InstallRecipeDelegate(sectionFetcher)
		sectionHandler = handlers.NewSectionHandler(sectionRepo, sectionFetcher)
		sectionHandler.CollectionRepo = sectionFetcher.CollectionRepo
		sectionHandler.FolderRepo = deps.FolderRepo
		if deps.UserStoreProvider != nil {
			sectionHandler.StoreProvider = deps.UserStoreProvider
		}
		sectionHandler.EpisodeRepo = episodeRepo
		sectionHandler.DetailSvc = detailSvc
		if userRepo != nil {
			sectionHandler.UserRepo = userRepo
		}
		if settingsRepo != nil {
			sectionHandler.Settings = settingsRepo
			sectionSettingsHandler = &handlers.SectionSettingsHandler{Settings: settingsRepo}
		}

		libraryCollectionRepo := catalog.NewLibraryCollectionRepository(deps.DB)
		if libraryCollectionService == nil {
			libraryCollectionService = catalog.NewLibraryCollectionService(
				libraryCollectionRepo,
				itemRepo,
				catalog.NewLibraryItemRepository(deps.DB),
				nil,
			)
		}
		if libraryCollectionService.TMDBCollections == nil {
			apiKey := ""
			if deps.Config != nil {
				apiKey = deps.Config.TMDBAPIKey
			}
			libraryCollectionService.TMDBCollections = &tmdbCollectionAdapter{
				client: tmdb.NewClient(apiKey, 40),
			}
		}
		if libraryCollectionService.TMDBFranchises == nil {
			apiKey := ""
			if deps.Config != nil {
				apiKey = deps.Config.TMDBAPIKey
			}
			libraryCollectionService.TMDBFranchises = &tmdbFranchiseAdapter{
				client: tmdb.NewClient(apiKey, 40),
			}
		}
		if libraryCollectionService.TMDBDiscovers == nil {
			apiKey := ""
			if deps.Config != nil {
				apiKey = deps.Config.TMDBAPIKey
			}
			libraryCollectionService.TMDBDiscovers = &tmdbDiscoverAdapter{
				client: tmdb.NewClient(apiKey, 40),
			}
		}
		traktClientID := ""
		if settingsRepo != nil {
			ctx := deps.AppContext
			if ctx == nil {
				ctx = context.Background()
			}
			if value, err := settingsRepo.Get(ctx, "watchsync.trakt.client_id"); err == nil {
				traktClientID = value
			}
		}
		if libraryCollectionService.TraktCollections == nil {
			libraryCollectionService.TraktCollections = &traktCollectionAdapter{
				client: metatrakt.NewClient(traktClientID, 5),
			}
		}
		if libraryCollectionService.TraktTokenResolver == nil && deps.DB != nil && settingsRepo != nil {
			libraryCollectionService.TraktTokenResolver = &traktCollectionTokenResolver{
				pool:     deps.DB,
				settings: settingsRepo,
				provider: watchtrakt.NewProvider(nil, ""),
			}
		}

		// Propagate the now-wired Trakt + TMDB fetchers to the user-side sync
		// service (constructed earlier in main.go before settingsRepo and the
		// Trakt adapters existed, so its fetcher fields started nil).
		if deps.UserCollectionSync != nil {
			if deps.UserCollectionSync.TraktCollections == nil {
				deps.UserCollectionSync.TraktCollections = libraryCollectionService.TraktCollections
			}
			if deps.UserCollectionSync.TraktTokenResolver == nil {
				deps.UserCollectionSync.TraktTokenResolver = libraryCollectionService.TraktTokenResolver
			}
			if deps.UserCollectionSync.TMDBCollections == nil {
				deps.UserCollectionSync.TMDBCollections = libraryCollectionService.TMDBCollections
			}
		}

		libraryCollectionHandler = handlers.NewLibraryCollectionHandler(
			libraryCollectionRepo,
			libraryCollectionService,
			itemRepo,
			4*time.Hour,
			nil,
			deps.S3Public,
		)
		libraryCollectionHandler.FrontendFS = deps.FrontendFS
		libraryCollectionHandler.Executor = &catalog.QueryExecutor{Pool: deps.DB}
		libraryCollectionHandler.SectionRepo = sectionRepo
		libraryCollectionHandler.UserCollectionPool = deps.DB
		libraryCollectionHandler.EventsHub = deps.EventsHub
		if deps.FolderRepo != nil {
			libraryCollectionHandler.FolderRepo = deps.FolderRepo
		} else {
			libraryCollectionHandler.FolderRepo = catalog.NewFolderRepository(deps.DB)
		}
		libraryCollectionGroupRepo := catalog.NewLibraryCollectionGroupRepository(deps.DB)
		libraryCollectionHandler.GroupRepo = libraryCollectionGroupRepo
		if deps.DB != nil {
			libraryCollectionHandler.JobRepo = adminjob.NewRepository(deps.DB)
		}
		libraryCollectionGroupHandler = handlers.NewLibraryCollectionGroupHandler(
			libraryCollectionGroupRepo,
			libraryCollectionRepo,
			deps.DB,
		)
		refresher := &catalog.SmartCountRefresher{
			Pool:     deps.DB,
			Executor: &catalog.QueryExecutor{Pool: deps.DB},
		}
		libraryCollectionHandler.SmartCountRefresher = refresher
		appCtx := deps.AppContext
		if appCtx == nil {
			appCtx = context.Background()
		}
		go func() {
			select {
			case <-time.After(15 * time.Second):
			case <-appCtx.Done():
				return
			}
			refreshed, errs := refresher.RefreshAll(appCtx)
			slog.Info("smart-count refresh complete", "refreshed", refreshed, "errors", errs)

			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-appCtx.Done():
					return
				case <-ticker.C:
					refreshed, errs := refresher.RefreshAll(appCtx)
					slog.Debug("smart-count refresh complete", "refreshed", refreshed, "errors", errs)
				}
			}
		}()
		if detailSvc != nil {
			libraryCollectionHandler.SetDetailService(detailSvc)
			libraryCollectionHandler.SetupCollage()
		}
	}

	// Build recommendations handler if ratings repo is available.
	var recsHandler *handlers.RecommendationsHandler
	if ratingsRepo != nil {
		var recsRepo *recommendations.Repo
		var recsReader *recommendations.Reader
		if deps.DB != nil {
			recsRepo = recommendations.NewRepo(deps.DB)
			recsReader = recommendations.NewReader(recsRepo, ratingsRepo, deps.RecWorker, deps.UserStoreProvider)
		}
		recsHandler = handlers.NewRecommendationsHandler(deps.Recommender, recsReader, deps.UserStoreProvider, ratingsRepo, recsRepo, deps.Recommender != nil)
		if deps.DB != nil {
			recsFetcher := sections.NewFetcher(deps.DB)
			recsFetcher.StoreProvider = deps.UserStoreProvider
			recsFetcher.NextUpRepo = catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
			recsHandler.Fetcher = recsFetcher
			recsHandler.WatchTonightFetcher = recsFetcher
		}
		if detailSvc != nil {
			recsHandler.DetailSvc = detailSvc
		}
		recsHandler.CalendarRepo = calendarRepo
		recsHandler.EpisodeRepo = episodeRepo
		if deps.PersonRepo != nil {
			recsHandler.CastFetcher = deps.PersonRepo
		}
		if deps.RecWorker != nil {
			recsHandler.RecWorker = deps.RecWorker
		}
	}

	// Build download handler.
	var downloadHandler *handlers.DownloadHandler
	if deps.DB != nil && deps.FileRepo != nil && deps.Config != nil {
		downloadRepo := download.NewRepository(deps.DB)
		downloadBandwidth := download.NewBandwidthManager(
			deps.Config.Download.ServerBandwidthBPS,
			deps.Config.Download.UserBandwidthBPS,
		)
		downloadLimiter := download.NewQuantityLimiter(
			downloadRepo,
			deps.Config.Download.MaxConcurrentPerUser,
			deps.Config.Download.MaxPerPeriod,
			deps.Config.Download.PeriodDuration,
		)
		downloadSvc := download.NewService(
			downloadRepo,
			downloadBandwidth,
			downloadLimiter,
			deps.FileRepo,
			itemRepo,
			episodeRepo,
			userRepo,
			itemRepo,
			settingsRepo,
			&deps.Config.Download,
		)
		downloadHandler = handlers.NewDownloadHandler(downloadSvc)
	} else {
		downloadHandler = handlers.NewDownloadHandler(nil)
	}

	var historyImportHandler *handlers.HistoryImportHandler
	var historyImportSvc *historyimport.Service
	if deps.DB != nil {
		historyRepo := historyimport.NewRepository(deps.DB)
		historyImportSvc = historyimport.NewService(deps.AppContext, historyRepo, deps.UserStoreProvider)
		historyIdentity := watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo)
		historyImportSvc.SetStableIdentityResolver(historyIdentity)
		if deps.EventsHub != nil {
			historyImportSvc.AddObserver(evt.NewHistoryImportObserver(deps.EventsHub))
		}
		historyImportHandler = handlers.NewHistoryImportHandler(historyImportSvc)
		if deps.UserStoreProvider != nil {
			webhookSyncSvc := webhooksync.NewService(webhooksync.NewRepository(deps.DB), historyRepo, deps.UserStoreProvider)
			webhookSyncSvc.SetStableIdentityResolver(historyIdentity)
			webhookSyncHandler = handlers.NewWebhookSyncHandler(webhookSyncSvc)
		}
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/health", healthHandler.ServeHTTP)
		r.Get("/ready", readyHandler.ServeHTTP)
		if webhookSyncHandler != nil {
			r.Post("/plex-sync/webhooks/{secret}", webhookSyncHandler.HandleWebhook)
			r.Post("/webhook-sync/webhooks/{secret}", webhookSyncHandler.HandleWebhook)
		}

		// Theme endpoints (admin-css is public for pre-login branding).
		if settingsRepo != nil {
			themeHandler := handlers.NewThemeHandler(settingsRepo)
			r.Get("/theme/admin-css", themeHandler.HandleAdminCSS)
			r.Get("/theme/branding", themeHandler.HandleBranding)

			// Catalog and download proxies require auth (to avoid open proxy).
			if authMiddleware != nil {
				r.Group(func(r chi.Router) {
					r.Use(authMiddleware.RequireAuth)
					r.Get("/theme/catalog", themeHandler.HandleCatalog)
					r.Get("/theme/download", themeHandler.HandleDownload)
					r.With(apimw.RequireAdmin).Post("/theme/catalog/refresh", themeHandler.HandleCatalogRefresh)
				})
			}
		}

		if deps.PluginHTTPProxy != nil {
			r.HandleFunc("/plugins/{installation_id}/*", func(w http.ResponseWriter, r *http.Request) {
				installationID, err := strconv.Atoi(chi.URLParam(r, "installation_id"))
				if err != nil {
					http.Error(w, "invalid installation id", http.StatusBadRequest)
					return
				}
				authenticated, admin, userID := resolveOptionalPluginAccessUser(r, jwtService, sessionRepo, apiKeyRepo, userRepo)
				ctx := plugins.WithPluginAccessUser(r.Context(), authenticated, admin, userID)
				deps.PluginHTTPProxy.ServeRoute(w, r.WithContext(ctx), installationID, authenticated, admin)
			})
			r.Get("/plugin-assets/{installation_id}/*", func(w http.ResponseWriter, r *http.Request) {
				installationID, err := strconv.Atoi(chi.URLParam(r, "installation_id"))
				if err != nil {
					http.Error(w, "invalid installation id", http.StatusBadRequest)
					return
				}
				assetPath := strings.TrimPrefix(chi.URLParam(r, "*"), "/")
				if assetPath == "" {
					http.NotFound(w, r)
					return
				}
				authenticated, admin := resolveOptionalPluginAccess(r, jwtService, sessionRepo)
				deps.PluginHTTPProxy.ServeAsset(w, r.WithContext(plugins.WithPluginAccess(r.Context(), authenticated, admin)), installationID, assetPath)
			})
		}

		// Auth routes: public (no auth required).
		if authHandler != nil {
			// OAuth handler is optional: it only stands up when PublicURL is
			// configured (we need a stable redirect_uri origin for IdPs) and
			// the DB is available (oauth_session storage).
			var oauthHandler *auth.OAuthHandler
			if deps.PublicURL != "" && deps.DB != nil && authService != nil && jwtService != nil {
				stateSecret := auth.DeriveOAuthStateSecret([]byte(deps.Config.Auth.JWTSecret))
				oauthStore := auth.NewPGOAuthStore(deps.DB, stateSecret)
				resolveClient := func(ctx context.Context, installationID int) (auth.OAuthClient, string, error) {
					pp := authService.FindOAuthInstallation(installationID)
					if pp == nil {
						return nil, "", errors.New("plugin not found")
					}
					c, err := pp.OAuthClient(ctx)
					if err != nil {
						return nil, "", err
					}
					return c, pp.CapabilityID(), nil
				}
				oauthHandler = auth.NewOAuthHandler(auth.OAuthHandlerDeps{
					Store:           oauthStore,
					CompletionStore: oauthStore,
					StateSecret:     stateSecret,
					ResolveClient:   resolveClient,
					LoginCompleter:  authService,
					HostBaseURL:     deps.PublicURL,
					StateTTL:        10 * time.Minute,
				})
			}

			r.Route("/auth", func(r chi.Router) {
				if deps.RateLimitMW != nil {
					r.With(deps.RateLimitMW.AuthEndpointHandler("login")).Post("/login", authHandler.HandleLogin)
					r.With(deps.RateLimitMW.AuthEndpointHandler("setup")).Post("/setup", authHandler.HandleSetup)
					r.With(deps.RateLimitMW.AuthEndpointHandler("signup")).Post("/signup", authHandler.HandleSignup)
				} else {
					r.Post("/login", authHandler.HandleLogin)
					r.Post("/setup", authHandler.HandleSetup)
					r.Post("/signup", authHandler.HandleSignup)
				}
				r.Get("/setup", authHandler.HandleSetupStatus)
				r.Get("/providers", authHandler.HandleProviders)
				r.Post("/refresh", authHandler.HandleRefresh)
				r.Get("/signup", authHandler.HandleSignupStatus)
				if authMiddleware != nil {
					r.With(authMiddleware.RequireAuth).Post("/plugin-launch", authHandler.HandlePluginLaunch)
				}
				if oauthHandler != nil {
					r.Post("/oauth/complete", oauthHandler.HandleComplete)
					r.Route("/oauth/{install_id}", func(r chi.Router) {
						r.Post("/init", oauthHandler.HandleInit)
						r.Get("/callback", oauthHandler.HandleCallback)
					})
				}
				if deps.RateLimitMW != nil {
					r.With(deps.RateLimitMW.AuthEndpointHandler("device_start")).Post("/device/start", authHandler.HandleDeviceStart)
					r.With(deps.RateLimitMW.AuthEndpointHandler("device_lookup")).Get("/device", authHandler.HandleDeviceLookup)
					r.With(deps.RateLimitMW.AuthEndpointHandler("device_poll")).Post("/device/poll", authHandler.HandleDevicePoll)
				} else {
					r.Post("/device/start", authHandler.HandleDeviceStart)
					r.Get("/device", authHandler.HandleDeviceLookup)
					r.Post("/device/poll", authHandler.HandleDevicePoll)
				}

				// Protected auth routes (require valid session).
				if authMiddleware != nil {
					r.Group(func(r chi.Router) {
						r.Use(authMiddleware.RequireAuth)
						r.Post("/logout", authHandler.HandleLogout)
						r.Post("/impersonation/end", authHandler.HandleEndImpersonation)
						r.Get("/me", authHandler.HandleMe)
						r.Get("/sessions", authHandler.HandleListSessions)
						r.Delete("/sessions/{id}", authHandler.HandleDeleteSession)
						r.Post("/device/approve", authHandler.HandleDeviceApprove)
						r.Post("/device/deny", authHandler.HandleDeviceDeny)
					})
				}
			})
		}

		// API key management routes (auth only, no viewer access needed).
		if apiKeyRepo != nil && authMiddleware != nil {
			r.Group(func(r chi.Router) {
				r.Use(authMiddleware.RequireAuth)
				if demoGuard != nil {
					r.Use(demoGuard.Guard)
				}

				apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo)
				r.Route("/api-keys", func(r chi.Router) {
					r.Post("/", apiKeyHandler.HandleCreateAPIKey)
					r.Get("/", apiKeyHandler.HandleListAPIKeys)
					r.Delete("/{id}", apiKeyHandler.HandleDeleteAPIKey)
				})
			})
		}

		// All remaining routes require auth.
		if authMiddleware != nil {
			r.Group(func(r chi.Router) {
				r.Use(authMiddleware.RequireAuth)
				if demoGuard != nil {
					r.Use(demoGuard.Guard)
				}
				if deps.RateLimitMW != nil {
					r.Use(deps.RateLimitMW.Handler)
				}
				if viewerAccessMiddleware != nil {
					r.Use(viewerAccessMiddleware.RequireViewerAccess)
				}

				// User-facing library route (all authenticated users).
				if libraryHandler != nil {
					r.Get("/user/libraries", libraryHandler.HandleListUserLibraries)
				}
				if deps.EventsHub != nil {
					eventsHandler := handlers.NewEventsHandler(
						deps.EventsHub,
						adminJobsHandler,
						adminHandler,
						deps.TaskManager,
						deps.ScanRegistry,
						deps.LibraryScanQueue,
						historyImportSvc,
					)
					r.Get("/events/ws", eventsHandler.HandleWebSocket)
				}

				// Library management routes (admin-only).
				if libraryHandler != nil {
					r.Group(func(r chi.Router) {
						r.Use(apimw.RequireAdmin)

						r.Route("/libraries", func(r chi.Router) {
							r.Get("/", libraryHandler.HandleListLibraries)
							r.Get("/roots", libraryHandler.HandleListRoots)
							r.Put("/roots/override", libraryHandler.HandleUpsertRootOverride)
							r.Delete("/roots/override", libraryHandler.HandleDeleteRootOverride)
							r.Get("/skipped-roots", libraryHandler.HandleListSkippedRoots)
							r.Get("/stale-ids", libraryHandler.HandleListStaleIDs)
							r.Post("/stale-ids/{contentID}/rematch", libraryHandler.HandleRematchStaleID)
							r.Get("/unmatched-items", libraryHandler.HandleListUnmatchedItems)
							r.Post("/", libraryHandler.HandleCreateLibrary)
							r.Put("/reorder", libraryHandler.HandleReorderLibraries)
							r.Put("/{id}", libraryHandler.HandleUpdateLibrary)
							r.Delete("/{id}", libraryHandler.HandleDeleteLibrary)
							r.Post("/{id}/check-mount", libraryHandler.HandleCheckLibraryMount)
							r.Post("/{id}/confirm-empty-root-cleanup", libraryHandler.HandleConfirmEmptyRootCleanup)
							r.Post("/{id}/refresh-metadata", libraryHandler.HandleRefreshLibraryMetadata)
							r.Get("/{id}/providers", libraryHandler.HandleGetLibraryProviders)
							r.Put("/{id}/providers", libraryHandler.HandleSetLibraryProviders)
							r.Put("/{id}/poster", libraryHandler.HandleUploadPoster)
							r.Delete("/{id}/poster", libraryHandler.HandleDeletePoster)
						})

						r.Post("/scan", libraryHandler.HandleScan)
						r.Post("/scan/cancel", libraryHandler.HandleScanCancel)
					})
				}

				// Browse, search, and item detail routes.
				if itemsHandler != nil {
					r.Get("/catalog", catalogHandler.HandleGetCatalog)
					r.Get("/catalog/filters", catalogHandler.HandleGetCatalogFilters)
					r.Post("/catalog/query", catalogHandler.HandlePostCatalogQuery)
					if catalogResourceHandler != nil {
						r.Get("/catalog/items/{id}", catalogResourceHandler.HandleGetItemDetail)
						r.Get("/catalog/items/{id}/episodes", catalogResourceHandler.HandleGetItemEpisodes)
						r.Get("/catalog/items/{id}/versions", catalogResourceHandler.HandleGetItemVersions)
						r.Get("/catalog/series/{id}/seasons", catalogResourceHandler.HandleGetSeasons)
						r.Get("/catalog/series/{id}/seasons/{num}", catalogResourceHandler.HandleGetSeason)
						r.Get("/catalog/series/{id}/seasons/{num}/episodes", catalogResourceHandler.HandleGetEpisodes)
					}
					r.Get("/watch/{id}", itemsHandler.HandleGetWatchDetail)
				}

				if calendarRepo != nil {
					calendarHandler := handlers.NewCalendarHandler(calendarRepo, detailSvc)
					r.With(apimw.RequireProfile).Get("/calendar", calendarHandler.HandleGetCalendar)
				}

				if peopleHandler != nil {
					r.Get("/people", peopleHandler.HandleSearch)
					r.Get("/people/{id}", peopleHandler.HandleGetPerson)
					r.Post("/people/{id}/refresh", peopleHandler.HandleRefreshPerson)
				}

				if libraryCollectionHandler != nil {
					r.Get("/library/{id}/collections", libraryCollectionHandler.HandleListLibraryCollections)
					r.Get("/library/{id}/collections/{collection_id}/items", libraryCollectionHandler.HandleGetLibraryCollectionItems)
					r.Get("/library/{id}/user-collections", libraryCollectionHandler.HandleListLibraryUserCollections)
				}

				// Profile routes.
				if profileHandler != nil {
					r.Route("/profiles", func(r chi.Router) {
						r.Get("/household/sessions", profileHandler.HandleListHouseholdSessions)
						r.Get("/", profileHandler.HandleListProfiles)
						r.Post("/", profileHandler.HandleCreateProfile)
						r.Put("/{id}", profileHandler.HandleUpdateProfile)
						r.Delete("/{id}", profileHandler.HandleDeleteProfile)
						r.Put("/{id}/avatar", profileHandler.HandleUploadAvatar)
						r.Delete("/{id}/avatar", profileHandler.HandleDeleteAvatar)
						r.Post("/{id}/verify-pin", profileHandler.HandleVerifyPIN)
					})
				}

				// Favorites, watchlist, and history routes (profile-scoped).
				if personalDataHandler != nil && itemsHandler != nil {
					r.Route("/watched", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Post("/{id}", itemsHandler.HandleMarkWatched)
						r.Delete("/{id}", itemsHandler.HandleMarkUnwatched)
					})

					r.Route("/favorites", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", personalDataHandler.HandleListFavorites)
						r.Get("/{item_id}", personalDataHandler.HandleCheckFavorite)
						r.Put("/{item_id}", personalDataHandler.HandleAddFavorite)
						r.Delete("/{item_id}", personalDataHandler.HandleRemoveFavorite)
					})

					r.Route("/watchlist", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", personalDataHandler.HandleListWatchlist)
						r.Get("/{item_id}", personalDataHandler.HandleCheckWatchlist)
						r.Put("/{item_id}", personalDataHandler.HandleAddToWatchlist)
						r.Delete("/{item_id}", personalDataHandler.HandleRemoveFromWatchlist)
					})

					r.Route("/history", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", personalDataHandler.HandleListHistory)
						r.Post("/remove", personalDataHandler.HandleRemoveHistory)
					})

					// Ratings routes (profile-scoped).
					if ratingsHandler != nil {
						r.Route("/ratings", func(r chi.Router) {
							r.Use(apimw.RequireProfile)
							r.Get("/", ratingsHandler.HandleListRatings)
							r.Get("/{item_id}", ratingsHandler.HandleGetRating)
							r.Put("/{item_id}", ratingsHandler.HandleSetRating)
							r.Delete("/{item_id}", ratingsHandler.HandleDeleteRating)
						})
					}
				}

				// Progress and sync routes (profile-scoped).
				if progressHandler != nil {
					r.Route("/progress", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", progressHandler.HandleListProgress)
					})

					r.Route("/sync", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Post("/progress", progressHandler.HandleSyncProgress)
					})
				}

				// Collection routes (profile-scoped).
				if collectionHandler != nil {
					var userImportHandler *handlers.UserCollectionImportHandler
					if deps.UserCollectionSync != nil {
						userImportHandler = handlers.NewUserCollectionImportHandler(
							deps.UserStoreProvider,
							deps.UserCollectionSync,
							deps.UserCollectionScheduler,
							nil,
							deps.MDBListClient,
							deps.S3Public,
							deps.FrontendFS,
							4*time.Hour,
						)
					}
					r.Route("/collections", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", collectionHandler.HandleListCollections)
						r.Post("/", collectionHandler.HandleCreateCollection)
						r.Post("/preview", collectionHandler.HandlePreviewCollection)
						r.Put("/order", collectionHandler.HandleReorderCollections)
						r.Post("/groups", collectionHandler.HandleCreateCollectionGroup)
						r.Put("/groups/order", collectionHandler.HandleReorderCollectionGroups)
						r.Put("/groups/{id}", collectionHandler.HandleUpdateCollectionGroup)
						r.Delete("/groups/{id}", collectionHandler.HandleDeleteCollectionGroup)
						if userImportHandler != nil {
							r.Get("/templates", userImportHandler.HandleListTemplates)
							r.Get("/import/mdblist/search", userImportHandler.HandleSearchMDBList)
							r.Get("/import/mdblist/top", userImportHandler.HandleTopMDBList)
							r.Post("/import/mdblist", userImportHandler.HandleImportMDBList)
							r.Post("/import/tmdb", userImportHandler.HandleImportTMDB)
							r.Post("/import/trakt", userImportHandler.HandleImportTrakt)
							r.Post("/{id}/sync", userImportHandler.HandleSync)
						}
						r.Put("/{id}", collectionHandler.HandleUpdateCollection)
						r.Delete("/{id}", collectionHandler.HandleDeleteCollection)
						r.Delete("/{id}/image", collectionHandler.HandleDeleteCollectionImage)
						r.Get("/{id}/items", collectionHandler.HandleListCollectionItems)
						r.Put("/{id}/items/order", collectionHandler.HandleReorderCollectionItems)
						r.Put("/{id}/items/{item_id}", collectionHandler.HandleAddCollectionItem)
						r.Delete("/{id}/items/{item_id}", collectionHandler.HandleRemoveCollectionItem)
					})
				}

				if homeDismissalHandler != nil {
					r.Route("/home/dismissals", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Put("/{surface}/{item_id}", homeDismissalHandler.HandleUpsertDismissal)
						r.Delete("/{surface}/{item_id}", homeDismissalHandler.HandleDeleteDismissal)
					})
				}

				if watchProviderHandler != nil {
					r.Route("/watch-providers", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", watchProviderHandler.HandleListProviders)
						r.Get("/{provider}/connection", watchProviderHandler.HandleGetConnection)
						r.Patch("/{provider}/connection", watchProviderHandler.HandleUpdateConnection)
						r.Delete("/{provider}/connection", watchProviderHandler.HandleDeleteConnection)
						r.Post("/{provider}/auth/device-code", watchProviderHandler.HandleStartDeviceAuth)
						r.Post("/{provider}/auth/poll", watchProviderHandler.HandlePollDeviceAuth)
						r.Post("/{provider}/auth/api-key", watchProviderHandler.HandleConnectAPIKey)
						r.Post("/{provider}/sync", watchProviderHandler.HandleManualSync)
						r.Get("/{provider}/sync-runs", watchProviderHandler.HandleListSyncRuns)
					})
				}

				// Settings routes (user-scoped, no profile required).
				if settingsHandler != nil {
					r.Route("/settings", func(r chi.Router) {
						if deps.PluginUserConfig != nil && deps.PluginService != nil {
							pluginHandler := handlers.NewPluginHandler(
								plugins.NewRepositoryStore(deps.DB),
								plugins.NewInstallationStore(deps.DB),
								plugins.NewRuntimeConfigStore(deps.DB),
								deps.PluginService,
								deps.PluginUserConfig,
								deps.PluginHTTPProxy,
								metadata.NewChainRepository(deps.DB),
								deps.PluginImageResolver,
							)
							r.Get("/plugins", pluginHandler.HandleListUserPluginSettings)
							r.Get("/plugins/{installation_id}", pluginHandler.HandleGetUserPluginSettings)
							r.Put("/plugins/{installation_id}", pluginHandler.HandlePutUserPluginSettings)
						}
						r.Get("/", settingsHandler.HandleListSettings)
						r.Get("/overlay-config", settingsHandler.HandleGetOverlayConfig)
						r.Group(func(r chi.Router) {
							r.Use(apimw.RequireProfile)
							r.Get("/effective", settingsHandler.HandleGetEffectiveSettings)
							r.Get("/subtitle_appearance/effective", settingsHandler.HandleGetEffectiveSubtitleAppearance)
							r.Put("/device/subtitle_appearance", settingsHandler.HandleSetSubtitleAppearanceDeviceOverride)
							r.Delete("/device/subtitle_appearance", settingsHandler.HandleDeleteSubtitleAppearanceDeviceOverride)
							r.Get("/device/{key}", settingsHandler.HandleGetDeviceSetting)
							r.Put("/device/{key}", settingsHandler.HandleSetDeviceSetting)
							r.Delete("/device/{key}", settingsHandler.HandleDeleteDeviceSetting)
						})
						r.Get("/{key}", settingsHandler.HandleGetSetting)
						r.Put("/{key}", settingsHandler.HandleSetSetting)
						r.Delete("/{key}", settingsHandler.HandleDeleteSetting)
					})
				}

				if historyImportHandler != nil {
					r.Route("/history-imports", func(r chi.Router) {
						r.Get("/sources", historyImportHandler.HandleListSources)
						r.Post("/emby-connect/login", historyImportHandler.HandleLoginConnect)
						r.Post("/plex/auth/pin", historyImportHandler.HandleCreatePlexPin)
						r.Post("/plex/auth/check", historyImportHandler.HandleCheckPlexPin)
						r.Get("/runs", historyImportHandler.HandleListRuns)
						r.Post("/runs", historyImportHandler.HandleCreateRun)
						r.Get("/runs/{id}", historyImportHandler.HandleGetRun)
					})
				}
				if webhookSyncHandler != nil {
					r.Route("/plex-sync", func(r chi.Router) {
						r.Get("/connections", webhookSyncHandler.HandleLegacyListConnections)
						r.Post("/connections", webhookSyncHandler.HandleLegacyCreateConnection)
						r.Delete("/connections/{id}", webhookSyncHandler.HandleLegacyDeleteConnection)
						r.Post("/connections/{id}/webhook/rotate", webhookSyncHandler.HandleLegacyRotateWebhook)
						r.Get("/connections/{id}/actors", webhookSyncHandler.HandleLegacyGetActors)
						r.Put("/connections/{id}/actors", webhookSyncHandler.HandleLegacyUpdateActors)
					})
					r.Route("/webhook-sync", func(r chi.Router) {
						r.Get("/connections", webhookSyncHandler.HandleListConnections)
						r.Post("/connections", webhookSyncHandler.HandleCreateConnection)
						r.Put("/connections/{id}", webhookSyncHandler.HandleUpdateConnection)
						r.Delete("/connections/{id}", webhookSyncHandler.HandleDeleteConnection)
						r.Post("/connections/{id}/webhook/rotate", webhookSyncHandler.HandleRotateWebhook)
						r.Get("/connections/{id}/events", webhookSyncHandler.HandleListEvents)
						r.Get("/connections/{id}/profile-mappings", webhookSyncHandler.HandleGetProfileMappings)
						r.Put("/connections/{id}/profile-mappings", webhookSyncHandler.HandleUpdateProfileMappings)
					})
				}

				// Subtitle preference routes (profile-scoped).
				if subtitlePrefHandler != nil {
					r.Route("/subtitle-prefs", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/{series_id}", subtitlePrefHandler.HandleGetSubtitlePref)
						r.Put("/{series_id}", subtitlePrefHandler.HandleSetSubtitlePref)
						r.Delete("/{series_id}", subtitlePrefHandler.HandleDeleteSubtitlePref)
					})
				}

				// Audio preference routes (profile-scoped).
				if audioPrefHandler != nil {
					r.Route("/audio-prefs", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/{series_id}", audioPrefHandler.HandleGetAudioPref)
						r.Put("/{series_id}", audioPrefHandler.HandleSetAudioPref)
						r.Delete("/{series_id}", audioPrefHandler.HandleDeleteAudioPref)
					})
				}

				// Library playback preference routes (profile-scoped).
				if libraryPlaybackPrefHandler != nil {
					r.Route("/library-playback-prefs", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", libraryPlaybackPrefHandler.HandleListLibraryPlaybackPrefs)
						r.Put("/{library_id}", libraryPlaybackPrefHandler.HandleSetLibraryPlaybackPref)
						r.Delete("/{library_id}", libraryPlaybackPrefHandler.HandleDeleteLibraryPlaybackPref)
					})
				}

				// Subtitle search routes.
				if subtitleSearchHandler != nil {
					r.Route("/subtitles", func(r chi.Router) {
						r.Post("/search", subtitleSearchHandler.HandleSearch)
						r.Post("/download", subtitleSearchHandler.HandleDownload)
						r.Get("/{media_file_id}", subtitleSearchHandler.HandleList)
						r.Delete("/{id}", subtitleSearchHandler.HandleDelete)
					})
				}

				// Playback routes.
				if playbackHandler != nil {
					playbackHandler.ItemAccess = itemRepo
					playbackHandler.EpisodeLookup = episodeRepo
					playbackHandler.OriginalLangLookup = itemRepo
					playbackHandler.FFmpegLogSink = deps.FFmpegLogSink

					r.Route("/playback", func(r chi.Router) {
						// HLS transcode delivery — no profile auth needed;
						// session ID (UUID) serves as the access token, same
						// pattern as /stream/{session_id}.
						r.Get("/transcode/{session_id}/master.m3u8", playbackHandler.HandleGetTranscodeManifest)
						r.Get("/transcode/{session_id}/segment/{name}", playbackHandler.HandleGetTranscodeSegment)

						// Playback realtime control socket — needs auth but not profile.
						r.Get("/sessions/{session_id}/control/ws", playbackHandler.HandleSessionWebSocket)

						// All mutation routes require profile auth.
						r.Group(func(r chi.Router) {
							r.Use(apimw.RequireProfile)
							r.Post("/start", playbackHandler.HandleStartPlayback)
							r.Post("/{session_id}/progress", playbackHandler.HandleUpdateProgress)
							r.Patch("/{session_id}/audio", playbackHandler.HandleChangeAudioTrack)
							r.Delete("/{session_id}", playbackHandler.HandleStopPlayback)
							r.Post("/transcode/start", playbackHandler.HandleStartTranscode)
						})
					})
				}

				if watchTogetherHandler != nil {
					r.Route("/watch-together", func(r chi.Router) {
						r.Get("/rooms/{room_id}/ws", watchTogetherHandler.HandleRoomWebSocket)
						r.Group(func(r chi.Router) {
							r.Use(apimw.RequireProfile)
							r.Post("/rooms", watchTogetherHandler.HandleCreateRoom)
							r.Post("/join", watchTogetherHandler.HandleJoinRoom)
							r.Get("/rooms/{room_id}", watchTogetherHandler.HandleGetRoom)
							r.Put("/rooms/{room_id}/selection", watchTogetherHandler.HandleSelectRoomItem)
							r.Patch("/rooms/{room_id}/policy", watchTogetherHandler.HandleUpdateRoomPolicy)
							r.Delete("/rooms/{room_id}", watchTogetherHandler.HandleCloseRoom)
							r.Get("/rooms/{room_id}/suggestions", watchTogetherHandler.HandleListSuggestions)
							r.Post("/rooms/{room_id}/suggestions", watchTogetherHandler.HandleCreateSuggestion)
							r.Delete("/rooms/{room_id}/suggestions/{suggestion_id}", watchTogetherHandler.HandleDeleteSuggestion)
							r.Post("/rooms/{room_id}/suggestions/{suggestion_id}/vote", watchTogetherHandler.HandleVote)
							r.Delete("/rooms/{room_id}/suggestions/{suggestion_id}/vote", watchTogetherHandler.HandleUnvote)
							r.Post("/rooms/{room_id}/suggestions/promote", watchTogetherHandler.HandlePromoteSuggestion)
						})
					})
				}

				// Stream routes.
				if streamHandler != nil {
					r.Get("/stream/{session_id}", streamHandler.HandleStream)
					r.Head("/stream/{session_id}", streamHandler.HandleStream)
					r.Get("/stream/{session_id}/subtitles/{track}", streamHandler.HandleSubtitle)
				}

				// Download routes.
				r.Route("/downloads", func(r chi.Router) {
					r.Post("/", downloadHandler.HandleCreateDownload)
					r.Get("/", downloadHandler.HandleListDownloads)
					r.Delete("/{id}", downloadHandler.HandleDeleteDownload)
					r.Get("/{id}/file", downloadHandler.HandleDownloadFile)
				})
				r.Get("/direct-download", downloadHandler.HandleDirectDownload)
				r.Head("/direct-download", downloadHandler.HandleDirectDownload)

				// Recipe gallery catalog (no profile required — purely static metadata).
				recipeHandler := &handlers.RecipeHandler{}
				r.Get("/sections/recipes", recipeHandler.HandleList)
				r.Get("/sections/recipes/{type}/candidates", recipeHandler.HandleCandidates)

				// Audiobook endpoints (no profile required — catalog-level list).
				if itemRepo != nil {
					audiobookHandler := &handlers.AudiobookHandler{Items: itemRepo}
					r.Get("/audiobooks", audiobookHandler.HandleListAudiobooks)
				}

				// Section endpoints (profile-scoped).
				if sectionHandler != nil {
					r.Group(func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/home/layout", sectionHandler.HandleHomeLayout)
						r.Get("/home/sections", sectionHandler.HandleHomeSections)
						r.Get("/home/sections/{id}/items", sectionHandler.HandleHomeSectionItems)
						r.Get("/library/{id}/layout", sectionHandler.HandleLibraryLayout)
						r.Get("/library/{id}/sections", sectionHandler.HandleLibrarySections)
						r.Get("/library/{id}/sections/{sectionId}/items", sectionHandler.HandleLibrarySectionItems)
					})

					r.Route("/profile/sections", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/", sectionHandler.HandleGetProfileOverrides)
						r.Put("/", sectionHandler.HandleSaveProfileOverrides)
						r.Delete("/reset", sectionHandler.HandleResetProfileOverrides)
						r.Get("/settings", sectionHandler.HandleSectionSettings)
						if sectionSettingsHandler != nil {
							r.Get("/flags", sectionSettingsHandler.HandleGetProfileFlag)
						}
					})
				}

				// Recommendation routes (profile-scoped).
				if recsHandler != nil {
					r.Route("/recommendations", func(r chi.Router) {
						r.Use(apimw.RequireProfile)
						r.Get("/for-you/main", recsHandler.HandleForYouMain)
						r.Get("/for-you/rows", recsHandler.HandleForYouRows)
						r.Get("/because-watched/{item_id}", recsHandler.HandleBecauseWatched)
						r.Get("/similar/{item_id}", recsHandler.HandleSimilar)
						r.Get("/similar-users", recsHandler.HandleSimilarUsers)
						r.Get("/taste-profile", recsHandler.HandleTasteProfile)
						r.Get("/popular", recsHandler.HandlePopular)
						r.Get("/recently-added", recsHandler.HandleRecentlyAdded)
						r.Get("/discover", recsHandler.HandleDiscover)
						r.Get("/section/{kind}", recsHandler.HandleSection)
						r.Get("/section/{kind}/{key}", recsHandler.HandleSection)
						r.Get("/watch-tonight", recsHandler.HandleWatchTonight)
						r.Get("/watch-tonight/cards", recsHandler.HandleWatchTonightCards)
						r.Get("/taste-seed/items", recsHandler.HandleTasteSeedItems)
						r.Post("/taste-seed", recsHandler.HandleTasteSeed)
					})
				}

				// Admin routes (admin-only).
				if adminHandler != nil {
					r.Route("/admin", func(r chi.Router) {
						r.Use(apimw.RequireAdmin)

						r.Get("/users", adminHandler.HandleListUsers)
						r.Post("/users", adminHandler.HandleCreateUser)
						r.Get("/users/{id}", adminHandler.HandleGetUser)
						r.Put("/users/{id}", adminHandler.HandleUpdateUser)
						r.Delete("/users/{id}", adminHandler.HandleDeleteUser)
						r.Post("/users/{id}/impersonate", adminHandler.HandleImpersonateUser)
						r.Get("/users/{id}/profiles", adminHandler.HandleListUserProfiles)
						r.Get("/users/{id}/settings", adminHandler.HandleListUserSettings)
						r.Get("/users/{id}/settings/{key}", adminHandler.HandleGetUserSetting)
						r.Put("/users/{id}/settings/{key}", adminHandler.HandleUpdateUserSetting)
						r.Delete("/users/{id}/settings/{key}", adminHandler.HandleDeleteUserSetting)
						r.Get("/users/{id}/device-settings", adminHandler.HandleListUserDeviceSettings)
						r.Get("/users/{id}/device-settings/{key}", adminHandler.HandleListUserDeviceSettingsByKey)
						r.Put("/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}", adminHandler.HandleUpdateUserDeviceSetting)
						r.Delete("/users/{id}/device-settings/{key}", adminHandler.HandleDeleteUserDeviceSettingsByKey)
						r.Delete("/users/{id}/profiles/{profile_id}/device-settings/{key}/{device_id}", adminHandler.HandleDeleteUserDeviceSetting)
						r.Delete("/users/{id}/profiles/{profile_id}/devices/{device_id}/settings", adminHandler.HandleDeleteAllUserDeviceSettings)
						r.Get("/devices", adminHandler.HandleListDevices)
						r.Get("/devices/{user_id}/{device_id}", adminHandler.HandleGetDevice)

						r.Get("/sessions", adminHandler.HandleListSessions)
						r.Get("/playback-history", adminHandler.HandleListPlaybackHistory)
						r.Get("/unmatched", adminHandler.HandleListUnmatched)
						r.Get("/stats", adminHandler.HandleGetStats)
						r.Get("/settings/sensitive-status", adminHandler.HandleGetSensitiveStatus)
						r.Post("/settings/check/{kind}", adminHandler.HandleCheckSettingsConnection)
						if sectionSettingsHandler != nil {
							r.Get("/settings/sections", sectionSettingsHandler.HandleGet)
							r.Put("/settings/sections", sectionSettingsHandler.HandlePut)
						}
						r.Get("/settings/{key}", adminHandler.HandleGetSetting)
						r.Get("/settings", adminHandler.HandleGetSettings)
						r.Put("/settings/{key}", adminHandler.HandleUpdateSetting)
						r.Post("/items/{id}/refresh-metadata", adminHandler.HandleRefreshItemMetadata)
						r.Patch("/items/{id}/metadata", adminHandler.HandleUpdateItemMetadata)
						if adminIntroHandler != nil {
							r.Post("/items/{id}/refresh-markers", adminIntroHandler.HandleRefreshEpisodeMarkers)
							r.Post("/items/{id}/redetect-intro", adminIntroHandler.HandleRedetectEpisodeIntro)
						}
						if peopleHandler != nil {
							r.Post("/people/{id}/refresh", peopleHandler.HandleAdminRefreshPerson)
							r.Patch("/people/{id}", peopleHandler.HandleAdminUpdatePerson)
						}

						if adminMatchHandler != nil {
							r.Post("/items/{id}/match/search", adminMatchHandler.HandleSearchItemMatchCandidates)
							r.Post("/items/{id}/match/apply", adminMatchHandler.HandleApplyItemMatch)
						}

						if adminImageHandler != nil {
							r.Get("/items/{id}/images", adminImageHandler.HandleGetItemImages)
							r.Post("/items/{id}/images/apply", adminImageHandler.HandleApplyItemImage)
						}

						filesystemHandler := handlers.NewFilesystemHandler()
						r.Get("/filesystem/browse", filesystemHandler.HandleBrowse)

						if catalogSeedHandler != nil {
							r.Route("/catalog", func(r chi.Router) {
								r.Post("/export", catalogSeedHandler.HandleExport)
								r.Post("/export-jobs", catalogSeedHandler.HandleCreateExportJob)
								r.Post("/export-jobs/{id}/publish", catalogSeedHandler.HandlePublishExportJob)
								r.Post("/import-jobs", catalogSeedHandler.HandleCreateImportJob)
								r.Get("/import-sources", catalogSeedHandler.HandleListImportSources)
								r.Get("/local-import-sources", catalogSeedHandler.HandleListLocalImportSources)
								r.Post("/import", catalogSeedHandler.HandleImport)
							})
						}

						if adminJobsHandler != nil {
							r.Route("/jobs", func(r chi.Router) {
								r.Get("/", adminJobsHandler.HandleList)
								r.Get("/{id}", adminJobsHandler.HandleGet)
							})
						}

						if deps.PluginService != nil && deps.PluginUserConfig != nil {
							pluginHandler := handlers.NewPluginHandler(
								plugins.NewRepositoryStore(deps.DB),
								plugins.NewInstallationStore(deps.DB),
								plugins.NewRuntimeConfigStore(deps.DB),
								deps.PluginService,
								deps.PluginUserConfig,
								deps.PluginHTTPProxy,
								metadata.NewChainRepository(deps.DB),
								deps.PluginImageResolver,
							)
							r.Route("/plugins", func(r chi.Router) {
								r.Get("/repositories", pluginHandler.HandleListRepositories)
								r.Post("/repositories", pluginHandler.HandleCreateRepository)
								r.Put("/repositories/{id}", pluginHandler.HandleUpdateRepository)
								r.Delete("/repositories/{id}", pluginHandler.HandleDeleteRepository)
								r.Get("/catalog", pluginHandler.HandleCatalog)
								r.Get("/installations", pluginHandler.HandleListInstallations)
								r.Post("/installations", pluginHandler.HandleCreateInstallation)
								r.Post("/uploads", pluginHandler.HandleUploadInstallation)
								r.Put("/installations/{id}", pluginHandler.HandleUpdateInstallation)
								r.Post("/installations/{id}/update", pluginHandler.HandleApplyUpdate)
								r.Post("/installations/{id}/config/test", pluginHandler.HandleTestInstallationConfig)
								r.Put("/installations/{id}/config", pluginHandler.HandlePutInstallationConfig)
								r.Put("/installations/{id}/auth-binding", pluginHandler.HandlePutAuthBinding)
								r.Put("/installations/{id}/task-bindings/{capability_id}", pluginHandler.HandlePutTaskBinding)
								r.Delete("/installations/{id}", pluginHandler.HandleDeleteInstallation)
							})
						}

						if historyImportHandler != nil {
							r.Route("/history-import-sources", func(r chi.Router) {
								r.Get("/", historyImportHandler.HandleAdminListSources)
								r.Post("/", historyImportHandler.HandleAdminCreateSource)
								r.Put("/{id}", historyImportHandler.HandleAdminUpdateSource)
								r.Delete("/{id}", historyImportHandler.HandleAdminDeleteSource)
							})

							r.Route("/history-imports", func(r chi.Router) {
								r.Post("/plex/login", historyImportHandler.HandleAdminPlexLogin)
								r.Put("/sources/{id}/token", historyImportHandler.HandleAdminSetSourceToken)
								r.Delete("/sources/{id}/token", historyImportHandler.HandleAdminClearSourceToken)
								r.Get("/sources/{id}/users", historyImportHandler.HandleAdminDiscoverUsers)
								r.Post("/sources/{id}/bulk-run", historyImportHandler.HandleAdminBulkRun)
								r.Get("/mappings", historyImportHandler.HandleAdminListMappings)
								r.Post("/mappings", historyImportHandler.HandleAdminCreateMapping)
								r.Put("/mappings/{id}", historyImportHandler.HandleAdminUpdateMapping)
								r.Delete("/mappings/{id}", historyImportHandler.HandleAdminDeleteMapping)
								r.Post("/mappings/{id}/run", historyImportHandler.HandleAdminCreateRun)
								r.Get("/runs", historyImportHandler.HandleAdminListRuns)
								r.Get("/runs/{id}", historyImportHandler.HandleAdminGetRun)
								r.Post("/runs/{id}/cancel", historyImportHandler.HandleAdminCancelRun)
							})
						}

						if sectionHandler != nil {
							r.Route("/sections", func(r chi.Router) {
								r.Get("/", sectionHandler.HandleListSections)
								r.Post("/", sectionHandler.HandleCreateSection)
								r.Post("/preview", sectionHandler.HandlePreview)
								r.Put("/reorder", sectionHandler.HandleReorderSections)
								r.Post("/restore-defaults", sectionHandler.HandleRestoreDefaults)
								r.Put("/{id}", sectionHandler.HandleUpdateSection)
								r.Delete("/{id}", sectionHandler.HandleDeleteSection)
								if sectionBulkHandler != nil {
									r.Post("/bulk-create", sectionBulkHandler.HandleBulkCreate)
								}
							})
						}

						if libraryCollectionHandler != nil {
							collectionTemplateHandler := handlers.NewCollectionTemplateHandler(nil)
							r.Route("/collections", func(r chi.Router) {
								r.Get("/", libraryCollectionHandler.HandleListAdminCollections)
								r.Get("/templates", collectionTemplateHandler.HandleListTemplates)
								r.Get("/template-bundles", libraryCollectionHandler.HandleListTemplateBundles)
								r.Post("/template-bundles/{bundleID}/apply", libraryCollectionHandler.HandleApplyTemplateBundle)
								r.Post("/template-bundles/{bundleID}/apply-job", libraryCollectionHandler.HandleApplyTemplateBundleJob)
								r.Post("/", libraryCollectionHandler.HandleCreateAdminCollection)
								r.Post("/preview", libraryCollectionHandler.HandlePreviewAdminCollection)
								r.Put("/order", libraryCollectionHandler.HandleReorderAdminCollections)
								r.Put("/{id}", libraryCollectionHandler.HandleUpdateAdminCollection)
								r.Delete("/{id}", libraryCollectionHandler.HandleDeleteAdminCollection)
								r.Post("/{id}/sync", libraryCollectionHandler.HandleSyncAdminCollection)
								r.Delete("/{id}/image", libraryCollectionHandler.HandleDeleteCollectionImage)
								r.Put("/{id}/items/order", libraryCollectionHandler.HandleReorderAdminCollectionItems)
								r.Put("/{id}/items/{item_id}", libraryCollectionHandler.HandleAddAdminCollectionItem)
								r.Delete("/{id}/items/{item_id}", libraryCollectionHandler.HandleRemoveAdminCollectionItem)
								r.Post("/import/mdblist", libraryCollectionHandler.HandleImportMDBList)
								r.Post("/import/tmdb", libraryCollectionHandler.HandleImportTMDBCollection)
								r.Post("/import/trakt", libraryCollectionHandler.HandleImportTraktCollection)
							})
						}
						if libraryCollectionGroupHandler != nil {
							r.Route("/libraries/{libraryID}/collection-groups", func(r chi.Router) {
								r.Get("/", libraryCollectionGroupHandler.HandleListGroups)
								r.Post("/", libraryCollectionGroupHandler.HandleCreateGroup)
								r.Put("/reorder", libraryCollectionGroupHandler.HandleReorderGroups)
							})
							r.Route("/collection-groups", func(r chi.Router) {
								r.Put("/{id}", libraryCollectionGroupHandler.HandleUpdateGroup)
								r.Delete("/{id}", libraryCollectionGroupHandler.HandleDeleteGroup)
								r.Put("/{groupID}/collections/reorder", libraryCollectionGroupHandler.HandleReorderCollectionsInGroup)
							})
						}

						if deps.NodeRepo != nil {
							jwtSecret := ""
							if deps.Config != nil {
								jwtSecret = deps.Config.Auth.JWTSecret
							}
							nodeHandler := handlers.NewNodeHandler(deps.NodeRepo, deps.ProxyPool, deps.TranscodePool, deps.NodeRepo, deps.EventBus, deps.RedisClient, jwtSecret)
							r.Route("/nodes", func(r chi.Router) {
								r.Get("/", nodeHandler.HandleListNodes)
								r.Post("/", nodeHandler.HandleCreateNode)
								r.Put("/{id}", nodeHandler.HandleUpdateNode)
								r.Delete("/{id}", nodeHandler.HandleDeleteNode)
								r.Post("/{id}/check", nodeHandler.HandleCheckNode)
								r.Post("/force-reload", nodeHandler.HandleForceReloadNodes)
								r.Post("/{id}/force-reload", nodeHandler.HandleForceReloadNode)
							})
							// Live node sessions (reads from Redis)
							// Note: /admin/sessions is already used for playback sessions from PostgreSQL.
							r.Get("/node-sessions", nodeHandler.HandleListSessions)
						}

						// System inspection.
						{
							sysJWTSecret := ""
							if deps.Config != nil {
								sysJWTSecret = deps.Config.Auth.JWTSecret
							}
							systemHandler := handlers.NewSystemHandler(deps.TranscodePool, sysJWTSecret)
							r.Route("/system", func(r chi.Router) {
								r.Get("/build", systemHandler.HandleBuildInfo)
								r.Get("/hw-accel", systemHandler.HandleHWAccel)
							})
						}

						if deps.RecWorker != nil {
							adminRecsHandler := handlers.NewAdminRecommendationsHandler(deps.RecWorker)
							r.Route("/recommendations", func(r chi.Router) {
								r.Get("/status", adminRecsHandler.HandleStatus)
								r.Post("/trigger/embeddings", adminRecsHandler.HandleTriggerEmbeddings)
								r.Post("/trigger/taste-profiles", adminRecsHandler.HandleTriggerTasteProfiles)
								r.Post("/trigger/cowatch", adminRecsHandler.HandleTriggerCowatch)
								r.Post("/trigger/recommendations", adminRecsHandler.HandleTriggerRecommendations)
							})
						}

						if inviteCodeRepo != nil {
							inviteCodeHandler := handlers.NewInviteCodeHandler(inviteCodeRepo)
							r.Route("/invite-codes", func(r chi.Router) {
								r.Get("/", inviteCodeHandler.HandleListInviteCodes)
								r.Post("/", inviteCodeHandler.HandleCreateInviteCode)
								r.Put("/{id}", inviteCodeHandler.HandleUpdateInviteCode)
								r.Post("/{id}/top-up", inviteCodeHandler.HandleTopUpInviteCode)
								r.Delete("/{id}", inviteCodeHandler.HandleDeleteInviteCode)
							})
						}

						if adminSubtitleHandler != nil {
							r.Route("/subtitle-providers", func(r chi.Router) {
								r.Get("/", adminSubtitleHandler.HandleListProviders)
								r.Route("/{provider}", func(r chi.Router) {
									r.Put("/", adminSubtitleHandler.HandleUpdateProvider)
									r.Post("/test", adminSubtitleHandler.HandleTestProvider)
								})
							})
						}

						// Rate limit admin routes
						if deps.RateLimitMW != nil && settingsRepo != nil {
							rateLimitHandler := handlers.NewRateLimitHandler(settingsRepo, deps.RateLimitMW, deps.EventBus)
							r.Route("/rate-limits", func(r chi.Router) {
								r.Get("/config", rateLimitHandler.HandleGetConfig)
								r.Put("/config", rateLimitHandler.HandleUpdateConfig)
							})
						}

						if apiKeyRepo != nil {
							apiKeyHandler := handlers.NewAPIKeyHandler(apiKeyRepo)
							r.Get("/users/{userId}/api-keys", apiKeyHandler.HandleAdminListUserAPIKeys)
							r.Get("/api-keys", apiKeyHandler.HandleAdminListAllAPIKeys)
							r.Post("/api-keys", apiKeyHandler.HandleAdminCreateAPIKey)
							r.Delete("/api-keys/{id}", apiKeyHandler.HandleAdminDeleteAPIKey)
							r.Put("/api-keys/{id}/tier", apiKeyHandler.HandleAdminUpdateTier)
						}

						if deps.ActivityLogRepo != nil {
							adminIPHandler := handlers.NewAdminIPHandler(deps.ActivityLogRepo)
							r.Get("/users/{id}/ips", adminIPHandler.HandleGetUserIPs)
							r.Get("/ips", adminIPHandler.HandleGetIPUsers)
						}
						if deps.OpsLogRepo != nil && deps.ActivityLogRepo != nil {
							adminLogsHandler := handlers.NewAdminLogsHandler(deps.OpsLogRepo, deps.ActivityLogRepo, deps.LogStreamHub)
							r.Get("/logs/app", adminLogsHandler.HandleListOperationalLogs)
							r.Get("/logs/audit", adminLogsHandler.HandleListAuditLogs)
							r.Get("/logs/ws", adminLogsHandler.HandleLogStreamWebSocket)
						}
						if adminPlaybackControlHandler != nil {
							r.Post("/sessions/{session_id}/pause", adminPlaybackControlHandler.HandlePauseSession)
							r.Post("/sessions/{session_id}/resume", adminPlaybackControlHandler.HandleResumeSession)
							r.Post("/sessions/{session_id}/stop", adminPlaybackControlHandler.HandleStopSession)
							r.Post("/sessions/{session_id}/terminate", adminPlaybackControlHandler.HandleTerminateSession)
							r.Post("/sessions/{session_id}/message", adminPlaybackControlHandler.HandleMessageSession)
						}

						if deps.TaskManager != nil {
							taskHistoryRepo := repository.NewPgExecutionRepository(deps.DB)
							taskMetrics := handlers.NewTaskMetricsService(metadata.NewRefreshDebtRepository(deps.DB))
							taskHandler := handlers.NewTaskHandler(deps.TaskManager, taskHistoryRepo, taskMetrics)
							r.Route("/tasks", func(r chi.Router) {
								r.Get("/", taskHandler.HandleListTasks)
								r.Get("/{key}", taskHandler.HandleGetTask)
								r.Get("/{key}/metrics", taskHandler.HandleGetMetrics)
								r.Post("/{key}/run", taskHandler.HandleRunTask)
								r.Post("/{key}/cancel", taskHandler.HandleCancelTask)
								r.Put("/{key}/triggers", taskHandler.HandleUpdateTriggers)
								r.Get("/{key}/history", taskHandler.HandleGetHistory)
							})
						}
					})
				}
			})
		}
	})

	return r
}

// pgSubtitleMediaResolver implements handlers.SubtitleMediaResolver using a direct PG query.
type pgSubtitleMediaResolver struct {
	pool *pgxpool.Pool
}

func (r *pgSubtitleMediaResolver) GetMediaFileWithMetadata(ctx context.Context, fileID int) (*handlers.MediaFileMetadata, error) {
	var meta handlers.MediaFileMetadata
	err := r.pool.QueryRow(ctx, `
		SELECT
			mf.id,
			mf.file_path,
			COALESCE(mf.file_size, 0),
			COALESCE(mf.file_hash, ''),
			COALESCE(mf.resolution, ''),
			COALESCE(mf.codec_video, ''),
			COALESCE(mf.codec_audio, ''),
			mi.title,
			COALESCE(mi.year, 0),
			COALESCE(mi.imdb_id, ''),
			COALESCE(e.season_number, 0),
			COALESCE(e.episode_number, 0)
		FROM media_files mf
		JOIN media_items mi ON mi.content_id = mf.content_id
		LEFT JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.id = $1
	`, fileID).Scan(
		&meta.FileID,
		&meta.FilePath,
		&meta.FileSize,
		&meta.FileHash,
		&meta.Resolution,
		&meta.VideoCodec,
		&meta.AudioCodec,
		&meta.Title,
		&meta.Year,
		&meta.IMDbID,
		&meta.Season,
		&meta.Episode,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}

func resolveOptionalPluginAccess(
	r *http.Request,
	jwtService *auth.JWTService,
	sessionRepo *auth.SessionRepository,
) (bool, bool) {
	authenticated, admin, _ := resolveOptionalPluginAccessUser(r, jwtService, sessionRepo, nil, nil)
	return authenticated, admin
}

// resolveOptionalPluginAccessUser is like resolveOptionalPluginAccess but also
// returns the authenticated user's ID, and accepts API-key bearer tokens
// (sa_*) when apiKeyRepo + userRepo are provided.
func resolveOptionalPluginAccessUser(
	r *http.Request,
	jwtService *auth.JWTService,
	sessionRepo *auth.SessionRepository,
	apiKeyRepo *auth.APIKeyRepository,
	userRepo *auth.UserRepository,
) (bool, bool, int) {
	if jwtService == nil || sessionRepo == nil {
		return false, false, 0
	}

	token := ""
	if header := r.Header.Get("Authorization"); header != "" {
		parts := strings.SplitN(header, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
			token = strings.TrimSpace(parts[1])
		}
	}
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	if token == "" {
		if cookie, err := r.Cookie(auth.PluginAccessCookieName); err == nil {
			token = strings.TrimSpace(cookie.Value)
		}
	}
	if token == "" {
		return false, false, 0
	}

	if strings.HasPrefix(token, "sa_") {
		if apiKeyRepo == nil || userRepo == nil {
			return false, false, 0
		}
		apiKey, err := apiKeyRepo.GetByKey(r.Context(), token)
		if err != nil {
			return false, false, 0
		}
		user, err := userRepo.GetByID(r.Context(), apiKey.UserID)
		if err != nil || !user.Enabled {
			return false, false, 0
		}
		return true, user.Role == "admin", user.ID
	}

	claims, err := jwtService.ValidateToken(token)
	if err != nil || (claims.TokenType != auth.TokenTypeAccess && claims.TokenType != auth.TokenTypePluginAccess) {
		return false, false, 0
	}
	valid, err := sessionRepo.IsValid(r.Context(), claims.SessionID)
	if err != nil || !valid {
		return false, false, 0
	}
	return true, claims.Role == "admin", claims.UserID
}

// NewTMDBCollectionFetcher creates a TMDBCollectionFetcher from an API key.
// Exported so main.go can construct it for the collection sync scheduler.
func NewTMDBCollectionFetcher(apiKey string) catalog.TMDBCollectionFetcher {
	return &tmdbCollectionAdapter{
		client: tmdb.NewClient(apiKey, 40),
	}
}

// tmdbCollectionAdapter adapts the tmdb.Client to the catalog.TMDBCollectionFetcher interface.
type tmdbCollectionAdapter struct {
	client *tmdb.Client
}

func (a *tmdbCollectionAdapter) GetCollectionPreset(ctx context.Context, preset, mediaType, timeWindow string, limit int) ([]catalog.TMDBCollectionEntry, error) {
	results, err := a.client.GetCollectionPreset(ctx, preset, mediaType, timeWindow, limit)
	if err != nil {
		return nil, err
	}
	entries := make([]catalog.TMDBCollectionEntry, len(results))
	for i, r := range results {
		entry := catalog.TMDBCollectionEntry{
			ID:        r.ID,
			MediaType: r.MediaType,
			Title:     r.Title,
		}

		// Fetch external IDs (IMDb, TVDB) for better matching against local library.
		if externalIDs, err := a.client.GetExternalIDs(ctx, r.MediaType, r.ID); err == nil && externalIDs != nil {
			entry.IMDbID = externalIDs.IMDbID
			entry.TVDBID = externalIDs.TVDBID
		}

		entries[i] = entry
	}
	return entries, nil
}

// tmdbFranchiseAdapter adapts tmdb.Client to catalog.TMDBCollectionByIDFetcher
// for the `tmdb_collection` sync mode. Like the preset adapter, it enriches
// each TMDB collection part with external IDs so the catalog matcher can fall
// back to IMDb/TVDB when a local item lacks a TMDB ID.
type tmdbFranchiseAdapter struct {
	client *tmdb.Client
}

func (a *tmdbFranchiseAdapter) GetCollection(ctx context.Context, id int) ([]catalog.TMDBCollectionEntry, error) {
	collection, err := a.client.GetCollection(ctx, id)
	if err != nil {
		return nil, err
	}
	if collection == nil {
		return nil, nil
	}
	entries := make([]catalog.TMDBCollectionEntry, len(collection.Parts))
	for i, p := range collection.Parts {
		mediaType := p.MediaType
		if mediaType == "" {
			mediaType = "movie"
		}
		entry := catalog.TMDBCollectionEntry{
			ID:        p.ID,
			MediaType: mediaType,
			Title:     p.Title,
		}
		if externalIDs, err := a.client.GetExternalIDs(ctx, mediaType, p.ID); err == nil && externalIDs != nil {
			entry.IMDbID = externalIDs.IMDbID
			entry.TVDBID = externalIDs.TVDBID
		}
		entries[i] = entry
	}
	return entries, nil
}

// tmdbDiscoverAdapter adapts tmdb.Client to catalog.TMDBDiscoverFetcher for
// the `tmdb_discover` sync mode. Like the preset adapter, it enriches each
// result with external IDs so the catalog matcher can fall back to IMDb/TVDB
// when a local item lacks a TMDB ID.
type tmdbDiscoverAdapter struct {
	client *tmdb.Client
}

func (a *tmdbDiscoverAdapter) Discover(ctx context.Context, mediaType string, params catalog.TMDBDiscoverParams, limit int) ([]catalog.TMDBCollectionEntry, error) {
	results, err := a.client.Discover(ctx, mediaType, tmdb.DiscoverParams{
		WithGenres:       params.WithGenres,
		WithoutGenres:    params.WithoutGenres,
		SortBy:           params.SortBy,
		VoteCountGte:     params.VoteCountGte,
		VoteAverageGte:   params.VoteAverageGte,
		ReleaseDateGte:   params.ReleaseDateGte,
		ReleaseDateLte:   params.ReleaseDateLte,
		Certifications:   params.Certifications,
		CertificationLte: params.CertificationLte,
		WithRuntimeGte:   params.WithRuntimeGte,
		WithRuntimeLte:   params.WithRuntimeLte,
		OriginalLanguage: params.OriginalLanguage,
		Limit:            limit,
	})
	if err != nil {
		return nil, err
	}
	entries := make([]catalog.TMDBCollectionEntry, len(results))
	for i, r := range results {
		entry := catalog.TMDBCollectionEntry{
			ID:        r.ID,
			MediaType: r.MediaType,
			Title:     r.Title,
		}
		if externalIDs, err := a.client.GetExternalIDs(ctx, r.MediaType, r.ID); err == nil && externalIDs != nil {
			entry.IMDbID = externalIDs.IMDbID
			entry.TVDBID = externalIDs.TVDBID
		}
		entries[i] = entry
	}
	return entries, nil
}

type traktCollectionAdapter struct {
	client *metatrakt.Client
}

func (a *traktCollectionAdapter) GetCollectionPreset(ctx context.Context, preset, mediaType string, limit int, accessToken string) ([]catalog.TraktCollectionEntry, error) {
	results, err := a.client.GetCollectionPreset(ctx, preset, mediaType, limit, accessToken)
	if err != nil {
		return nil, err
	}
	entries := make([]catalog.TraktCollectionEntry, len(results))
	for i, r := range results {
		entries[i] = catalog.TraktCollectionEntry{
			TraktID:   r.TraktID,
			TMDBID:    r.TMDBID,
			TVDBID:    r.TVDBID,
			IMDbID:    r.IMDbID,
			MediaType: r.MediaType,
			Title:     r.Title,
			Year:      r.Year,
			Rank:      r.Rank,
		}
	}
	return entries, nil
}
