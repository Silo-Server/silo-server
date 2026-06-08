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
	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/recommendations"
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
	r.Use(requestLoggerMiddleware)
	r.Use(middleware.Recoverer)

	systemHandler := NewSystemHandler(deps.Config)
	webFS, err := resolveCompatWebFS(deps)
	if err != nil {
		slog.Error("jellycompat web bundle unavailable", "error", err)
	}
	authHandler := NewAuthHandler(deps.Config, deps.LoginResolver, deps.Authenticator)
	nextUpRepo := catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
	var subtitleRepo subtitles.Repository
	if deps.SubtitleRepo != nil {
		subtitleRepo = deps.SubtitleRepo
	} else if deps.DB != nil {
		subtitleRepo = subtitles.NewPgRepository(deps.DB)
	}
	itemsHandler := NewItemsHandler(deps.ContentService, deps.UserDataService, deps.IDCodec, deps.Config, deps.ImageCache, nextUpRepo, deps.BrowseRepo, deps.PersonRepo, deps.DetailSvc, deps.ItemRepo, deps.EpisodeRepo, deps.AccessFilterFn, subtitleRepo)
	itemsHandler.recommender = deps.Recommender
	autoscanHandler := NewAutoscanHandler(deps.FolderRepo, deps.ScanQueue, deps.IDCodec, itemsHandler)
	adminAPIKeyAuth := NewAdminAPIKeyAuthenticator(deps.APIKeyValidator, deps.APIKeyUserLoader)
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
	playbackHandler.ProxyPool = deps.ProxyPool
	playbackHandler.TranscodePool = deps.TranscodePool
	playbackHandler.JWTSecret = deps.JWTSecret
	playbackHandler.profileRefreshRequester = deps.RecWorker
	playbackHandler.SettingsRepo = deps.SettingsRepo
	if subtitleRepo != nil {
		playbackHandler.SubtitleRepo = subtitleRepo
		playbackHandler.S3Client = deps.S3Client
		playbackHandler.S3Bucket = deps.S3Bucket
	}
	imagesHandler := NewImagesHandler(deps.ContentService, deps.IDCodec, deps.HTTPClient, deps.SessionStore, deps.ImageCache, deps.PersonRepo, deps.DetailSvc, deps.ItemRepo, deps.FolderRepo, deps.SeasonRepo, deps.EpisodeRepo, deps.AccessFilterFn, deps.PosterPresigner, deps.PresignTTL, deps.JWTSecret)
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
	if webFS != nil {
		webHandler := http.StripPrefix("/web", newCompatWebHandler(webFS, compatWebVersion(deps.Config)))
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusFound)
		})
		r.Get("/web", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/web/", http.StatusFound)
		})
		r.Handle("/web/*", webHandler)
	}

	if deps.Authenticator != nil {
		r.Group(func(r chi.Router) {
			r.Use(deps.Authenticator.RequireSession)
			r.Get("/Users/Me", authHandler.HandleCurrentUser)
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
			r.Get("/Items/Suggestions", itemsHandler.HandleSuggestions)
			r.Get("/Users/{id}/Items/Latest", itemsHandler.HandleLatest)
			r.Get("/Items/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Movies/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Shows/{id}/Similar", itemsHandler.HandleSimilar)
			r.Get("/Items/{id}/ThemeMedia", itemsHandler.HandleItemStub)
			r.Get("/Items/{id}/SpecialFeatures", itemsHandler.HandleItemStub)
			r.Get("/Items/{id}/Intros", itemsHandler.HandleItemStub)
			r.Get("/Users/{userId}/Items/{id}/ThemeMedia", itemsHandler.HandleItemStub)
			r.Get("/Users/{userId}/Items/{id}/SpecialFeatures", itemsHandler.HandleItemStub)
			r.Get("/Users/{userId}/Items/{id}/Intros", itemsHandler.HandleItemStub)
			r.Get("/Items/{id}", itemsHandler.HandleItem)
			r.Get("/Users/{userId}/Items/Resume", itemsHandler.HandleResume)
			r.Get("/Users/{userId}/Items/{id}", itemsHandler.HandleItem)
			r.Get("/Genres", itemsHandler.HandleGenres)
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
			r.Get("/Users/{id}/Images/Primary", imagesHandler.HandleUserImage)
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
			r.Post("/Sessions/Logout", authHandler.HandleLogout)
			r.Get("/socket", HandleSocket)
		})
	}

	// Stream routes: use playback-session auth fallback for media players
	// (e.g. libmpv) that don't forward auth headers or query parameters.
	r.Group(func(r chi.Router) {
		r.Use(PlaybackSessionAuth(deps.SessionStore, deps.PlaybackStore))
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
				NewSessionRepository(deps.DB),
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
	playbackTTL := 6 * time.Hour
	if deps.Config != nil && deps.Config.JellyfinCompat.PlaybackSessionTTL > 0 {
		playbackTTL = deps.Config.JellyfinCompat.PlaybackSessionTTL
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if deps.DeviceProfiles == nil {
		deps.DeviceProfiles = NewDeviceProfileStore(playbackTTL, deps.Now)
	}
	if deps.PlaybackStore == nil {
		deps.PlaybackStore = NewPlaybackSessionStore(playbackTTL, deps.Now)
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
		deps.UserDataService = newDirectUserDataService(
			deps.UserStoreProvider,
			deps.ItemRepo,
			deps.EpisodeRepo,
			deps.ProviderIDRepo,
			deps.DetailSvc,
			staler,
			deps.RecWorker,
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
// request, except those targeting the bundled Jellyfin-web mount at /web.
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
