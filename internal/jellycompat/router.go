package jellycompat

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// NewRouter builds the Jellyfin-compatibility router.
func NewRouter(deps Dependencies) chi.Router {
	deps = withDefaults(deps)

	r := chi.NewRouter()
	r.Use(stripSlashesExceptWeb)
	r.Use(middleware.RequestID)
	if deps.ClientIPResolver != nil {
		r.Use(clientip.Middleware(deps.ClientIPResolver))
	}
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type",
			"X-Emby-Authorization", "X-Emby-Token", "X-Mediabrowser-Token",
		},
		AllowCredentials: false,
		MaxAge:           86400,
	}))
	r.Use(normalizeCompatPathMiddleware)
	r.Use(middleware.Compress(5, "application/json"))
	if debugPath := os.Getenv("JELLYCOMPAT_DEBUG_LOG"); debugPath != "" {
		rotator := &lumberjack.Logger{
			Filename:   debugPath,
			MaxSize:    50, // megabytes
			MaxBackups: 100,
			MaxAge:     30, // days
			Compress:   true,
		}
		uaFilter := os.Getenv("JELLYCOMPAT_DEBUG_USER_AGENT")
		r.Use(newDebugLogMiddleware(rotator, uaFilter))
		logAttrs := []any{"path", debugPath}
		if uaFilter != "" {
			logAttrs = append(logAttrs, "user_agent_filter", uaFilter)
		}
		slog.Info("jellycompat debug logging enabled", logAttrs...)
	}
	r.Use(compatImageProxyTagVariantMiddleware(deps.IDCodec))
	r.Use(requestLoggerMiddleware)
	r.Use(middleware.Recoverer)

	systemHandler := NewSystemHandler(deps.CurrentConfig)
	authHandler := NewAuthHandler(deps.CurrentConfig, deps.LoginResolver, deps.Authenticator)
	nextUpRepo := catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
	var subtitleRepo subtitles.Repository
	if deps.SubtitleRepo != nil {
		subtitleRepo = deps.SubtitleRepo
	} else if deps.DB != nil {
		subtitleRepo = subtitles.NewPgRepository(deps.DB, deps.SecretCipher)
	}
	itemsHandler := NewItemsHandler(deps.ContentService, deps.UserDataService, deps.IDCodec, deps.Config, deps.ImageCache, nextUpRepo, deps.BrowseRepo, deps.PersonRepo, deps.DetailSvc, deps.ItemRepo, deps.EpisodeRepo, deps.SeasonRepo, deps.AccessFilterFn, subtitleRepo)
	itemsHandler.recommender = deps.Recommender
	if deps.DB != nil {
		itemsHandler.collections = catalog.NewLibraryCollectionRepository(deps.DB)
		// Smart (live-query) collections derive membership at read time, so the
		// BoxSet children path needs a query executor to resolve them.
		itemsHandler.queryExecutor = &catalog.QueryExecutor{Pool: deps.DB}
		// Continue Watching (Resume) fast path: serve via the capped native
		// continue-watching fetcher instead of an unbounded progress scan. This is
		// the section subsystem's read-time fetcher only — no hub-section/virtual-
		// library exposure is wired here. The continue-watching path needs only
		// StoreProvider (progress); CollectionRepo/NextUpRepo are deliberately
		// left unset as they serve other section types.
		sf := sections.NewFetcher(deps.DB)
		sf.StoreProvider = deps.UserStoreProvider
		itemsHandler.sectionsFetcher = sf
	}
	itemsHandler.posterPresigner = deps.PosterPresigner
	itemsHandler.presignTTL = deps.PresignTTL
	autoscanHandler := NewAutoscanHandler(deps.FolderRepo, deps.ScanQueue, deps.IDCodec, itemsHandler)
	adminAPIKeyAuth := NewAdminAPIKeyAuthenticator(deps.APIKeyValidator, deps.APIKeyUserLoader, deps.UserStoreProvider, deps.Now)
	autoscanVirtualFoldersRegistered := false
	if deps.Authenticator != nil && adminAPIKeyAuth != nil && autoscanHandler != nil {
		r.With(RequireSessionOrAdminAPIKey(deps.Authenticator, adminAPIKeyAuth)).
			Get("/Library/VirtualFolders", autoscanHandler.HandleVirtualFolders)
		r.With(adminAPIKeyAuth.RequireAdminAPIKey).
			Post("/Library/Media/Updated", autoscanHandler.HandleMediaUpdated)
		autoscanVirtualFoldersRegistered = true
	}
	userDataHandler := NewUserDataHandler(deps.ContentService, deps.UserDataService, deps.IDCodec, deps.Config)
	playbackHandler := NewPlaybackHandler(deps.Config, deps.ContentService, deps.IDCodec, deps.DeviceProfiles, deps.PlaybackStore, deps.SessionMgr, deps.FileResolver, deps.UserStoreProvider)
	if deps.DB != nil {
		playbackHandler.profileStaler = recommendations.NewRepo(deps.DB)
	}
	playbackHandler.NodePlanner = deps.NodePlanner
	playbackHandler.JWTSecret = deps.JWTSecret
	// Compat transcode reconstruct is driven by the recipe carried in the durable
	// compat playback store (jellycompat_playback_sessions); no separate native
	// recipe table is needed.
	if cleaned, err := playbackHandler.CleanupOrphanedTranscodes(); err != nil {
		slog.Warn("jellycompat transcode cleanup failed", "dir", playbackHandler.TranscodeDir, "error", err)
	} else if cleaned > 0 {
		slog.Info("jellycompat transcode cleanup removed orphaned dirs", "dir", playbackHandler.TranscodeDir, "count", cleaned)
	}
	playbackHandler.profileRefreshRequester = deps.RecWorker
	playbackHandler.SettingsRepo = deps.SettingsRepo
	playbackHandler.RecipeNodeStore = deps.RecipeNodeStore
	playbackHandler.SessionSyncer = deps.SessionSyncer
	if subtitleRepo != nil {
		playbackHandler.SubtitleRepo = subtitleRepo
		playbackHandler.S3Client = deps.S3Client
		playbackHandler.S3Bucket = deps.S3Bucket
	}
	imagesHandler := NewImagesHandler(deps.ContentService, deps.IDCodec, deps.SessionStore, deps.ImageCache, deps.PersonRepo, deps.DetailSvc, deps.ItemRepo, deps.FolderRepo, deps.SeasonRepo, deps.EpisodeRepo, deps.AccessFilterFn, deps.PosterPresigner, deps.PresignTTL, deps.JWTSecret, deps.HTTPClient)
	imagesHandler.collections = itemsHandler.collections
	imagesHandler.frontendFS = deps.FrontendFS
	displayPrefsHandler := NewDisplayPreferencesHandler(deps.UserStoreProvider)
	recsHandler := NewRecommendationsHandler(deps.Recommender, deps.ItemRepo, deps.ContentService, deps.UserDataService, deps.IDCodec, deps.Config, deps.AccessFilterFn)

	r.Get("/System/Info/Public", systemHandler.HandlePublicInfo)
	r.Get("/System/Info", systemHandler.HandleInfo)
	r.Get("/System/Ping", systemHandler.HandlePing)
	r.Get("/System/Endpoint", systemHandler.HandleEndpoint)
	r.Get("/Branding/Configuration", systemHandler.HandleBrandingConfiguration)
	r.Get("/QuickConnect/Enabled", systemHandler.HandleQuickConnectEnabled)
	r.Get("/Users/Public", authHandler.HandlePublicUsers)
	r.Post("/Users/AuthenticateByName", authHandler.HandleAuthenticateByName)
	r.Get("/Items/{id}/Images/{imageType}", imagesHandler.HandleItemImage)
	r.Get("/Items/{id}/Images/{imageType}/{index}", imagesHandler.HandleItemImage)
	// Jellyfin user-avatar images are anonymous: clients fetch them via plain
	// <img> tags that carry no auth, so the route is registered top-level rather
	// than inside the session-auth group.
	r.Get("/Users/{id}/Images/Primary", imagesHandler.HandleUserImage)
	r.Method(http.MethodHead, "/Users/{id}/Images/Primary", http.HandlerFunc(imagesHandler.HandleUserImage))
	// Modern Jellyfin clients fetch the current user's avatar via /UserImage?userId=
	// (the path form above is [Obsolete] upstream). Same anonymous palette handler.
	r.Get("/UserImage", imagesHandler.HandleUserImage)
	r.Method(http.MethodHead, "/UserImage", http.HandlerFunc(imagesHandler.HandleUserImage))
	webHandler := http.StripPrefix("/web", newDynamicCompatWebHandler(deps))
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusFound)
	})
	r.Get("/web", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/web/", http.StatusFound)
	})
	r.Handle("/web/*", webHandler)

	if deps.Authenticator != nil {
		r.Group(func(r chi.Router) {
			r.Use(RequireSessionOrAPIKeySession(deps.Authenticator, adminAPIKeyAuth))
			r.Get("/Users/Me", authHandler.HandleCurrentUser)
			r.Get("/Users", authHandler.HandleUsers)
			r.Get("/Users/{id}", authHandler.HandleUserByID)
			r.Get("/UserViews", itemsHandler.HandleViews)
			r.Get("/UserViews/GroupingOptions", itemsHandler.HandleGroupingOptionsStub)
			if !autoscanVirtualFoldersRegistered {
				r.Get("/Library/VirtualFolders", itemsHandler.HandleVirtualFolders)
			}
			r.Get("/Users/{userId}/Views", itemsHandler.HandleViews)
			r.Get("/Items", itemsHandler.HandleItems)
			r.Get("/Users/{id}/Items", itemsHandler.HandleItems)
			r.Get("/Items/Latest", itemsHandler.HandleLatest)
			r.Get("/Items/Filters", itemsHandler.HandleFiltersStub)
			r.Get("/Items/Filters2", itemsHandler.HandleFilters2Stub)
			r.Get("/Items/Suggestions", itemsHandler.HandleSuggestions)
			r.Get("/Users/{id}/Items/Latest", itemsHandler.HandleLatest)
			r.Get("/Items/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Movies/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Shows/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Items/{id}/ThemeMedia", itemsHandler.HandleItemStub)
			r.Get("/Items/{id}/ThemeSongs", itemsHandler.HandleThemeSongsStub)
			r.Get("/Items/{id}/SpecialFeatures", itemsHandler.HandleSpecialFeatures)
			r.Get("/Items/{id}/Intros", itemsHandler.HandleItemStub)
			r.Get("/Items/{id}/LocalTrailers", itemsHandler.HandleLocalTrailers)
			r.Get("/Users/{userId}/Items/{id}/ThemeMedia", itemsHandler.HandleItemStub)
			r.Get("/Users/{userId}/Items/{id}/ThemeSongs", itemsHandler.HandleThemeSongsStub)
			r.Get("/Users/{userId}/Items/{id}/SpecialFeatures", itemsHandler.HandleSpecialFeatures)
			r.Get("/Users/{userId}/Items/{id}/Intros", itemsHandler.HandleItemStub)
			r.Get("/Users/{userId}/Items/{id}/LocalTrailers", itemsHandler.HandleLocalTrailers)
			r.Get("/Items/{id}", itemsHandler.HandleItem)
			r.Get("/Users/{userId}/Items/Resume", itemsHandler.HandleResume)
			r.Get("/Users/{userId}/Items/{id}", itemsHandler.HandleItem)
			r.Get("/Genres", itemsHandler.HandleGenres)
			r.Get("/Genres/{name}", itemsHandler.HandleGenreByName)
			r.Get("/Shows/{id}/Seasons", itemsHandler.HandleSeasons)
			r.Get("/Shows/{id}/Episodes", itemsHandler.HandleEpisodes)
			r.Get("/Shows/NextUp", itemsHandler.HandleNextUp)
			r.Get("/Shows/Upcoming", itemsHandler.HandleUpcoming)
			r.Get("/MediaSegments/{id}", itemsHandler.HandleMediaSegments)
			r.Get("/Episode/{id}/Timestamps", itemsHandler.HandleItemStub)
			r.Get("/Episode/{id}/IntroTimestamps", itemsHandler.HandleItemStub)
			r.Get("/UserItems/Resume", itemsHandler.HandleResume)
			r.Get("/Search/Hints", itemsHandler.HandleSearchHints)
			r.Get("/UserItems/{itemId}/UserData", userDataHandler.HandleGetUserData)
			r.Post("/UserFavoriteItems/{itemId}", userDataHandler.HandleAddFavorite)
			r.Delete("/UserFavoriteItems/{itemId}", userDataHandler.HandleRemoveFavorite)
			r.Post("/UserPlayedItems/{itemId}", userDataHandler.HandleMarkPlayed)
			r.Delete("/UserPlayedItems/{itemId}", userDataHandler.HandleMarkUnplayed)
			r.Post("/Users/{userId}/FavoriteItems/{itemId}", userDataHandler.HandleAddFavoriteLegacy)
			r.Delete("/Users/{userId}/FavoriteItems/{itemId}", userDataHandler.HandleRemoveFavoriteLegacy)
			r.Post("/Users/{userId}/PlayedItems/{itemId}", userDataHandler.HandleMarkPlayedLegacy)
			r.Delete("/Users/{userId}/PlayedItems/{itemId}", userDataHandler.HandleMarkUnplayedLegacy)
			r.Get("/Users/{userId}/Items/{itemId}/UserData", userDataHandler.HandleGetUserDataLegacy)
			r.Post("/Users/{userId}/Items/{itemId}/UserData", userDataHandler.HandleUpdateUserDataLegacy)
			r.Get("/DisplayPreferences/{displayPreferencesId}", displayPrefsHandler.HandleGetDisplayPreferences)
			r.Post("/DisplayPreferences/{displayPreferencesId}", displayPrefsHandler.HandleUpdateDisplayPreferences)
			if deps.PersonRepo != nil {
				personsHandler := NewPersonsHandler(deps.PersonRepo, deps.ContentService, deps.IDCodec, deps.ImageCache, deps.Config.JellyfinCompat.ServerID)
				r.Get("/Persons", personsHandler.HandleGetPersons)
				r.Get("/Persons/{name}", personsHandler.HandleGetPerson)
			} else {
				r.Get("/Persons", itemsHandler.HandleItemStub)
			}
			r.Get("/Studios", itemsHandler.HandleItemStub)
			r.Get("/Artists", itemsHandler.HandleItemStub)
			r.Get("/Movies/Recommendations", recsHandler.HandleRecommendations)
			r.Get("/Sessions", HandleSessions)
			r.Post("/Sessions/Capabilities", playbackHandler.HandleCapabilitiesFull)
			r.Post("/Sessions/Capabilities/Full", playbackHandler.HandleCapabilitiesFull)
			r.Get("/Playback/BitrateTest", playbackHandler.HandleBitrateTest)
			r.Get("/Items/{id}/PlaybackInfo", playbackHandler.HandlePlaybackInfo)
			r.Post("/Items/{id}/PlaybackInfo", playbackHandler.HandlePlaybackInfo)
			r.Get("/Users/{userId}/Items/{id}/PlaybackInfo", playbackHandler.HandlePlaybackInfo)
			r.Post("/Users/{userId}/Items/{id}/PlaybackInfo", playbackHandler.HandlePlaybackInfo)
			r.Post("/Sessions/Playing", playbackHandler.HandleSessionPlaying)
			r.Post("/Sessions/Playing/Progress", playbackHandler.HandleSessionPlayingProgress)
			r.Post("/Sessions/Playing/Stopped", playbackHandler.HandleSessionPlayingStopped)
			r.Delete("/Videos/ActiveEncodings", playbackHandler.HandleDeleteActiveEncodings)
			r.Post("/Sessions/Logout", authHandler.HandleLogout)
			r.Post("/ClientLog/Document", HandleClientLogDocument)
			r.Get("/socket", HandleSocket)
		})
	}

	// Stream routes: use playback-session auth fallback for media players
	// (e.g. libmpv) that don't forward auth headers or query parameters.
	r.Group(func(r chi.Router) {
		r.Use(PlaybackSessionAuth(deps.SessionStore, deps.PlaybackStore, adminAPIKeyAuth))
		r.Method(http.MethodHead, "/Items/{id}/Download", http.HandlerFunc(playbackHandler.HandleDownload))
		r.Get("/Items/{id}/Download", playbackHandler.HandleDownload)
		r.Method(http.MethodHead, "/Videos/{id}/stream", http.HandlerFunc(playbackHandler.HandleVideoStream))
		r.Get("/Videos/{id}/stream", playbackHandler.HandleVideoStream)
		r.Method(http.MethodHead, "/Videos/{id}/stream.{container}", http.HandlerFunc(playbackHandler.HandleVideoStream))
		r.Get("/Videos/{id}/stream.{container}", playbackHandler.HandleVideoStream)
		r.Get("/Videos/{id}/master.m3u8", playbackHandler.HandleMasterManifest)
		r.Get("/Videos/{id}/hls/{playlistId}/stream.m3u8", playbackHandler.HandleHLSManifest)
		r.Get("/Videos/{id}/hls/{playlistId}/{segmentId}.{segmentContainer}", playbackHandler.HandleHLSSegment)
		r.Get("/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/stream.{routeFormat}", playbackHandler.HandleSubtitleStream)
		// Infuse probes external subtitles with an extra numeric path component before stream.{format}.
		r.Get("/Videos/{routeItemId}/{routeMediaSourceId}/Subtitles/{routeIndex}/{routeDeliveryIndex}/stream.{routeFormat}", playbackHandler.HandleSubtitleStream)
	})

	r.Method(http.MethodHead, "/System/Info/Public", http.HandlerFunc(systemHandler.HandlePublicInfo))
	r.Method(http.MethodHead, "/System/Ping", http.HandlerFunc(systemHandler.HandlePing))
	r.Head("/", systemHandler.HandlePing)

	return r
}

func withDefaults(deps Dependencies) Dependencies {
	if deps.Now == nil {
		deps.Now = timeNow
	}
	// Stamp the compat surface's media-type exclusions onto every resolved
	// access filter so all consumers (content service, items/images handlers,
	// recommendations) inherit them without per-call-site guards.
	deps.AccessFilterFn = compatAccessFilterResolver(deps.AccessFilterFn)
	if deps.JWTSecret == "" && deps.Config != nil {
		deps.JWTSecret = deps.Config.Auth.JWTSecret
	}
	if deps.TokenGenerator == nil {
		deps.TokenGenerator = uuidNewString
	}
	if deps.SessionStore == nil && deps.Config != nil {
		if deps.DB != nil {
			deps.SessionStore = NewPersistentSessionStore(
				deps.Config.JellyfinCompat.SessionTTL,
				deps.Now,
				NewSessionRepository(deps.DB, deps.SecretCipher),
			)
		} else {
			deps.SessionStore = NewSessionStore(deps.Config.JellyfinCompat.SessionTTL, deps.Now)
		}
	}
	if deps.IDCodec == nil {
		deps.IDCodec = NewResourceIDCodec()
	}
	if deps.ImageCache == nil {
		cacheTTL := 24 * time.Hour
		if deps.Config != nil && deps.Config.JellyfinCompat.SessionTTL > 0 {
			cacheTTL = deps.Config.JellyfinCompat.SessionTTL
		}
		deps.ImageCache = NewImageCache(cacheTTL, deps.Now)
	}
	// Align the compat playback session's absolute lifetime with the absolute
	// stream-token TTL (playback.MaxTokenTTL, 24h). This is an ABSOLUTE window
	// from creation, not sliding/idle: the session need not outlive its token
	// while token re-mint is unimplemented, and at 6h long content (audiobooks,
	// movies) and paused-overnight sessions expired mid-playback even though the
	// stream token was still valid. Overridable per-deployment via config.
	playbackTTL := playback.MaxTokenTTL
	if deps.Config != nil && deps.Config.JellyfinCompat.PlaybackSessionTTL > 0 {
		playbackTTL = deps.Config.JellyfinCompat.PlaybackSessionTTL
	}
	if deps.DeviceProfiles == nil {
		deps.DeviceProfiles = NewDeviceProfileStore(playbackTTL, deps.Now)
	}
	if deps.PlaybackStore == nil {
		// Back the compat playback store with Postgres when a pool is available so
		// the PlaySessionId -> upstream-session mapping survives a restart and a
		// Jellyfin client can resume; fall back to in-memory otherwise.
		if deps.DB != nil {
			deps.PlaybackStore = NewDurableCompatPlaybackStore(deps.DB, playbackTTL, deps.Now)
		} else {
			deps.PlaybackStore = NewPlaybackSessionStore(playbackTTL, deps.Now)
		}
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}

	// Build ContentService from repos if not provided
	if deps.ContentService == nil && deps.BrowseRepo != nil && deps.ItemRepo != nil && deps.DetailSvc != nil {
		svc := newDirectContentService(
			deps.BrowseRepo,
			deps.ItemRepo,
			deps.SeasonRepo,
			deps.EpisodeRepo,
			deps.DetailSvc,
			deps.FolderRepo,
			deps.UserStoreProvider,
			deps.AccessFilterFn,
			deps.CatalogSearchProvider,
		)
		if deps.PosterPresigner != nil {
			svc.posterPresigner = deps.PosterPresigner
			svc.presignTTL = deps.PresignTTL
		}
		deps.ContentService = svc
	}

	// Build UserDataService from store provider if not provided
	if deps.UserDataService == nil && deps.UserStoreProvider != nil && deps.ItemRepo != nil {
		var staler profileStaler
		if deps.DB != nil {
			staler = recommendations.NewRepo(deps.DB)
		}
		var pool *pgxpool.Pool
		if deps.BrowseRepo != nil {
			pool = deps.BrowseRepo.Pool()
		}
		deps.UserDataService = newDirectUserDataService(
			deps.UserStoreProvider,
			deps.ItemRepo,
			deps.EpisodeRepo,
			deps.ProviderIDRepo,
			deps.DetailSvc,
			catalog.NewContinueWatchingProgressFilter(pool),
			staler,
			deps.RecWorker,
			deps.WatchCompletionObserver,
		)
	}

	// Build LoginResolver from auth service if not provided
	if deps.LoginResolver == nil && deps.AuthService != nil && deps.UserStoreProvider != nil && deps.SessionStore != nil {
		deps.LoginResolver = NewLoginResolver(deps.AuthService, deps.UserStoreProvider, deps.SessionStore, deps.TokenGenerator, deps.Now)
	}

	if deps.Authenticator == nil && deps.SessionStore != nil {
		deps.Authenticator = NewAuthenticator(deps.SessionStore, deps.AuthService)
	}
	return deps
}

// stripSlashesExceptWeb behaves like chi's middleware.StripSlashes for every
// request, except those targeting the configured Jellyfin-compatible web mount at /web.
// Trailing slashes there are load-bearing: the static handler chain
// (StripPrefix → /web/* → newCompatWebHandler) needs the slash to distinguish
// "/web" (redirect to /web/) from "/web/" (serve index.html). Stripping it
// would collapse "/web/" to "/web" and trigger an infinite 302 loop against
// the /web → /web/ redirect.
func stripSlashesExceptWeb(next http.Handler) http.Handler {
	stripped := middleware.StripSlashes(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/web" || strings.HasPrefix(r.URL.Path, "/web/") {
			next.ServeHTTP(w, r)
			return
		}
		stripped.ServeHTTP(w, r)
	})
}
