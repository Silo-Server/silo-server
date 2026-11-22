// Package api provides the HTTP router and middleware setup for Vio.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/ai/chatgpt"
	"github.com/Silo-Server/silo-server/internal/ai/jobrunner"
	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/animeids"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/apiusage"
	"github.com/Silo-Server/silo-server/internal/apiv2"
	"github.com/Silo-Server/silo-server/internal/artworkurl"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/backup"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/downloads"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/invitations"
	"github.com/Silo-Server/silo-server/internal/keptsub"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/literaryworks"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/mail"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	metadatatranslation "github.com/Silo-Server/silo-server/internal/metadata/translation"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/nodeauth"
	"github.com/Silo-Server/silo-server/internal/nodemetrics"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/noderecipe"
	"github.com/Silo-Server/silo-server/internal/nodesource"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/onboarding"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/overlays"
	"github.com/Silo-Server/silo-server/internal/passwordreset"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/playback/connectionstore"
	"github.com/Silo-Server/silo-server/internal/playback/planstore"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/progresssync"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/ratingsources"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/remotestream"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/shuffle"
	"github.com/Silo-Server/silo-server/internal/storagetransition"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	subtitleai "github.com/Silo-Server/silo-server/internal/subtitles/ai"
	"github.com/Silo-Server/silo-server/internal/subtitles/opensubtitles"
	"github.com/Silo-Server/silo-server/internal/subtitles/subdl"
	"github.com/Silo-Server/silo-server/internal/subtitles/subsource"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/taskmanager/repository"
	"github.com/Silo-Server/silo-server/internal/taskmanager/tasks"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/Silo-Server/silo-server/internal/trickplay"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	virtuallibrary "github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/watchlist"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/Silo-Server/silo-server/internal/webhooksync"
)

// The media request service answers the administrator request-usage read.
var _ apiv2.AdminRequestUsageService = (*mediarequests.Service)(nil)

// SeriesTVDBSeasonSource is the metadata service's TVDB season method. It is
// declared here so internal/requests need not import internal/metadata, which
// would close an import cycle through internal/notifications.
type SeriesTVDBSeasonSource interface {
	FetchSeriesTVDBSeasons(ctx context.Context, tvdbID int, language string) ([]metadata.SeasonResult, error)
}

// NewSeriesSeasonProvider adapts a TVDB season source to the request service's
// season-provider seam.
func NewSeriesSeasonProvider(source SeriesTVDBSeasonSource) mediarequests.SeriesSeasonProvider {
	if source == nil {
		return nil
	}
	return seriesSeasonProviderAdapter{source: source}
}

type seriesSeasonProviderAdapter struct {
	source SeriesTVDBSeasonSource
}

func (a seriesSeasonProviderAdapter) FetchSeriesTVDBSeasons(ctx context.Context, tvdbID int, language string) ([]mediarequests.SeriesSeason, error) {
	results, err := a.source.FetchSeriesTVDBSeasons(ctx, tvdbID, language)
	if err != nil {
		return nil, err
	}
	out := make([]mediarequests.SeriesSeason, 0, len(results))
	for _, season := range results {
		if season.SeasonNumber <= 0 {
			continue
		}
		episodeCount := 0
		for _, episode := range season.Episodes {
			if episode.EpisodeNumber > 0 && episode.SeasonNumber == season.SeasonNumber {
				episodeCount++
			}
		}
		out = append(out, mediarequests.SeriesSeason{
			Number:       season.SeasonNumber,
			Name:         season.Title,
			EpisodeCount: episodeCount,
			AirDate:      season.AirDate,
			// TVDB supplies absolute artwork URLs, which do not fit the v2
			// poster_path field or the TMDB image helper; PosterURL carries
			// them. A relative path is not a URL this surface can serve, so it
			// is left out rather than handed to clients as artwork.
			PosterPath: "",
			PosterURL:  absoluteArtworkURL(season.PosterPath),
		})
	}
	return out, nil
}

// absoluteArtworkURL returns raw when it is an absolute http or https URL, and
// "" for anything else: provider-relative paths and other schemes are not
// artwork clients can load directly.
func absoluteArtworkURL(raw string) string {
	if raw = strings.TrimSpace(raw); raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return ""
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return ""
	}
	return raw
}

// Dependencies holds all shared dependencies that handlers need.
// ArtworkDelivery describes how clients read artwork. External is true only
// when reads go through a separately configured public or token endpoint, the
// one case seek-preview sheets confirm before clients read them there (see
// internal/trickplay). Scope changes when the delivery configuration does,
// invalidating earlier confirmations.
type ArtworkDelivery struct {
	Scope    string
	External bool
}

type Dependencies struct {
	Config *config.Config
	// SubtitlePlaySync receives subtitles a player is served, so one never
	// synced is aligned the first time it is played. The routes here connect
	// it to the subtitle sync service; the Jellyfin routes share it. May be nil.
	SubtitlePlaySync *subtitles.PlaySyncHook
	// LiveConfig returns the current hot-reloaded config. May be nil (tests,
	// worker modes); read through CurrentConfig(), which falls back to Config.
	LiveConfig func() *config.Config
	// ShuttingDown reports whether this process has begun shutting down.
	// /api/v1/ready fails once it does, while the server still answers
	// requests. May be nil.
	ShuttingDown func() bool
	// OnConfigChange registers a callback fired after a live config reload
	// actually changes the config. May be nil when hot reload is not wired.
	OnConfigChange func(fn func(old, updated *config.Config))
	// BootstrapEnvVars names the environment variables that seeded this
	// process's bootstrap settings, so the admin UI can say so.
	BootstrapEnvVars             map[string]string
	BootstrapSensitiveConfigured map[string]bool
	BootstrapSensitiveValues     map[string]string
	RedisBootstrapAvailable      bool
	AppContext                   context.Context
	// RegisterShutdownWork retains asynchronous cleanup completion until main's
	// graceful-shutdown deadline. Nil is valid in tests and embedded routers.
	RegisterShutdownWork func(<-chan struct{})
	// RegisterShutdownFunc registers a named, idempotent application-shutdown
	// step that main invokes explicitly under its own bounded timeout, before
	// waiting on the context-triggered RegisterShutdownWork channels. Use it for
	// work whose drain must be awaited by shutdown rather than only triggered by
	// service-context cancellation. Nil is valid in tests and embedded routers.
	RegisterShutdownFunc func(name string, run func())
	// OnVirtualRefreshExecutor receives the router-built asynchronous virtual
	// candidates refresh executor so main can install it on the admin job
	// runner, which is constructed after the router. Nil is valid in tests and
	// embedded routers.
	OnVirtualRefreshExecutor func(adminjob.VirtualCandidatesRefreshExecutor)

	DB              *pgxpool.Pool
	SecretCipher    *secret.Cipher // at-rest credential cipher (required when DB is set)
	FrontendFS      fs.FS
	S3Public        *s3client.Client // public assets bucket client (may be nil)
	Blobs           blobstore.Stores // backend-neutral blob stores (assets and operational)
	ArtworkBackend  string           // resolved blob storage backend name
	ArtworkDelivery ArtworkDelivery
	ArtworkSigner   *artworkurl.Signer
	ArtworkResolver artworkurl.Resolver
	ArtworkRepair   interface {
		EnqueueArtworkRepair(context.Context, []string, int) (int, error)
	}
	S3Private         *s3client.Client              // private internal bucket client (may be nil)
	BrandingService   *branding.Service             // white-label branding (nil when DB unavailable)
	OverlayImages     *overlays.ImageLibrary        // card overlay image library (nil when DB unavailable)
	EmailBrand        *mail.BrandLoader             // server branding for outgoing email (nil sends Silo's default)
	FolderRepo        *catalog.FolderRepository     // media folder repository (may be nil)
	FileRepo          *scanner.FileRepository       // media file repository (may be nil)
	Scanner           *scanner.Scanner              // scanner instance (may be nil)
	LibraryIngester   *libraryingest.Executor       // shared library ingest executor (may be nil)
	ProbeEnsurer      handlers.PlaybackProbeEnsurer // on-demand probe repair for playback/detail (may be nil)
	UserStoreProvider userstore.UserStoreProvider   // user store provider (may be nil)
	SessionMgr        *playback.SessionManager      // playback session manager (may be nil)
	StreamTelemetry   *streamtelemetry.Registry     // local observation-only stream telemetry (may be nil)
	// StreamTelemetryViewCache serves the merged global view with bounded
	// staleness so the admin parity endpoint never rebuilds it per request.
	StreamTelemetryViewCache *streamtelemetry.ViewCache
	SkippedRootRepo          *metadata.SkippedRootRepository  // skipped root repository (may be nil)
	StaleIDRepo              *metadata.StaleMediaIDRepository // stale media ID repository (may be nil)
	MovieMatchQueueRepo      *metadata.MovieMatchQueueRepository
	SeriesRootMatchQueueRepo *metadata.SeriesRootMatchQueueRepository
	Refresher                handlers.AdminMetadataRefresher // metadata refresher (may be nil)
	NodeRepo                 *nodepool.Repository            // stream node repository (may be nil)
	ProxyPool                *nodepool.ProxyPool             // proxy node pool (may be nil)
	TranscodePool            *nodepool.TranscodePool         // transcode node pool (may be nil)
	NodePlanner              *nodepool.Planner               // group/cap-aware node selection (may be nil)
	NodeHealthChecker        *nodepool.HealthChecker         // periodic node health/capability sweep (may be nil)
	NodeKeyRegistry          *nodepool.KeyRegistry           // every stream node's credential (may be nil)
	NodeKeys                 nodeauth.Keyring                // signs calls to stream nodes
	SourceGrants             *nodesource.Grants              // mints node source grants
	// NodeCapabilityInvalidator drops one node's cached capability inventory
	// outside the playback handler — the prepared-download preparer holds its
	// own. nil where downloads are not wired; set before NewRouter runs.
	NodeCapabilityInvalidator func(nodeURL string)
	ResourceSampler           *nodemetrics.Sampler           // this host's own resource sampler (may be nil)
	SessionSyncer             handlers.PlaybackSessionSyncer // optional; immediate playback session sync trigger
	// LiveSessionRowJSON renders a session as the live row a sync would
	// store for it, so a session.started event can carry it before a sync.
	LiveSessionRowJSON      func(*playback.Session) (json.RawMessage, error)
	EventBus                cache.EventBus
	AdminStatsProvider      handlers.AdminStatsSource
	Recommender             recommendations.Recommender // nil when disabled
	RecWorker               *recommendations.Worker     // nil when disabled
	CatalogSearchVectorizer catalog.CatalogSearchQueryVectorizer
	// CatalogSearchSettings is the process-lifetime startup snapshot shared by
	// every native/jellycompat provider and the index maintenance worker.
	CatalogSearchSettings *catalog.CatalogSearchSettings
	RatingsRepo           *catalog.RatingsRepo
	PersonRepo            *catalog.PersonRepository
	PersonRefreshQueue    handlers.PersonRefreshQueue
	PersonRefresher       handlers.PersonRefresher
	RateLimitMW           *ratelimit.Middleware
	// ProfilePINAttempts bounds wrong profile PIN guesses per profile,
	// shared with the Jellyfin login so both count against one budget.
	// Nil gets a process-local limiter.
	ProfilePINAttempts *ratelimit.AttemptLimiter
	// QuickConnectCodeAttempts bounds the six-digit Jellyfin Quick Connect
	// codes each account tries, shared with the Jellyfin-compatible API's
	// Authorize route. Nil gets a process-local limiter.
	QuickConnectCodeAttempts *ratelimit.AttemptLimiter
	// SignInAttempts bounds password sign-ins per sign-in name, shared with
	// the Jellyfin and Audiobookshelf sign-ins so every surface counts against
	// one budget. Nil gets a process-local limiter.
	SignInAttempts   *ratelimit.AttemptLimiter
	ClientIPResolver *clientip.Resolver
	// NetworkAccess is the ingress-token registry and provider status cache
	// for network access provider plugins on this host. The token middleware
	// runs on every native request and connected overlay origins are accepted
	// by WebSocket handshakes. Nil disables both (tests, worker modes).
	NetworkAccess             *netaccess.Broker
	NodeID                    string
	LogStreamHub              *logstream.Hub
	RealtimeHub               *notifications.Hub
	Notifications             *notifications.System // user-facing release notifications (may be nil)
	PolicySystem              *policy.System        // policy engine lifecycle (may be nil)
	EventsHub                 *evt.Hub
	ScanRegistry              *evt.ScanRegistry
	LibraryScanQueue          *scanqueue.Service
	LibraryMonitor            interface{ Poke() }          // real-time library monitor, reconciled after library mutations (nil when this node runs none)
	Trickplay                 interface{ ReconcileSoon() } // seek preview service, reconciled after a library's trickplay setting changes (nil when not configured)
	TrickplayReader           *trickplay.Reader            // published seek previews for players (nil when not configured)
	TrickplayAdmin            *trickplay.Admin             // seek preview status and regeneration for administrators (nil when not configured)
	KeptSubtitles             *keptsub.Store
	SubtitlePrefetch          *keptsub.Prefetcher
	LibraryMonitoring         apiv2.LibraryMonitoringService // real-time monitoring status for the v2 admin read (may be nil)
	ActivityLogWriter         activitylog.Writer
	ActivityLogRepo           *activitylog.Repo
	APIUsage                  *apiusage.Store // per-session API request summaries (nil without a database)
	OpsLogRepo                *opslog.Repo
	FFmpegLogSink             playback.FFmpegLogSink
	RedisClient               *redis.Client              // for session listing (may be nil)
	DownloadBandwidthRedis    *redis.Client              // deadline-enabled client for shared download budgets (may be nil)
	TaskManager               *taskmanager.TaskManager   // task manager (may be nil)
	ArtifactManager           *downloads.ArtifactManager // download prepare-to-file pipeline (may be nil)
	AdminJobCancelRegistry    *adminjob.CancelRegistry
	IntroRepository           *intromarkers.Repository
	IntroAnalyzer             *intromarkers.Analyzer
	MarkerRegistry            *markers.Registry
	MarkerResolver            markers.ExternalIDResolver
	MarkerProviderConfig      *markers.ProviderConfigStore
	MarkerContributionStore   *markers.ContributionStore
	MarkerContributionService *markers.ContributionService
	MarkerPopulation          *markers.PopulationService
	MarkerUpdateNotifier      *playback.MarkerUpdateNotifier
	WatchProviderService      handlers.WatchProviderService
	// WatchProviderRegistry is the watchsync registry, used by the admin stats
	// to list every provider — built-in or plugin-contributed — even when none
	// of them has any activity yet.
	WatchProviderRegistry   handlers.WatchProviderLister
	WatchCompletionObserver watchstate.CompletionObserver
	PluginService           *plugins.Service
	PluginHTTPProxy         *plugins.HTTPProxy
	PluginUserConfig        *plugins.UserConfigStore
	AuthProviders           []auth.RegisteredProvider
	// AuthProviderSource supplies the auth-plugin sign-in providers, rebuilt
	// without a restart; OnAuthProvidersChanged rebuilds them on every node
	// after an auth binding write.
	AuthProviderSource     auth.PluginProviderSource
	OnAuthProvidersChanged func(context.Context)
	// AuthProviderRecheck re-checks sessions opened through an external
	// sign-in provider at refresh (nil skips it).
	AuthProviderRecheck *auth.ProviderRecheck
	// PublicURL is the externally-reachable origin (scheme + host) for this
	// silo instance. Used to build redirect_uri values handed to OAuth
	// IdPs. Empty disables the /oauth/{install_id}/{init,callback} routes.
	PublicURL           string
	ImageResolver       catalog.ImageResolver             // plugin-based image URL resolver (may be nil)
	PluginImageResolver *metadata.PluginImageResolver     // concrete resolver for runtime source registration (may be nil)
	MetadataService     handlers.MatchMetadataService     // metadata search+process (may be nil)
	CollectionService   *catalog.LibraryCollectionService // collection service (may be nil)
	// VirtualLibraryService is the core virtual library service, populated
	// after successful migrate -> validate -> activate. When set, playback
	// resolution routes directly to core without a plugin.
	VirtualLibraryService *virtuallibrary.Service
	// VirtualLibraryVariants is the core virtual-library Variants closure
	// (virtuallibrary.Service.VirtualVariants), set by main after a
	// successful migrate -> validate -> activate. Nil means the core
	// service is dormant/unavailable and the plugin path stays.
	VirtualLibraryVariants func(ctx context.Context, virtualURI, mediaType string) ([]catalog.VirtualPlaybackVariant, error)
	ChapterThumbnailQueuer catalog.ChapterThumbnailQueuer
	PlaybackRealtimeHub    *playback.RealtimeHub
	OnUserSessionsRevoked  func(ctx context.Context, userID int)
	// OnDownloadService receives the configured download service when the
	// router builds one, so the Jellyfin-compatible listener serves its
	// downloads under the same policy and bandwidth limiters. Optional.
	OnDownloadService func(*downloads.Service)
	// v2Wiring observes the sealed v2 dependency set right before
	// apiv2.NewHandler consumes it; tests only. It is the one way to assert
	// that a v1 handler reached the v2 listener, since NewRouter returns a
	// sealed handler.
	v2Wiring               func(apiv2.Dependencies)
	v2RouteSnapshot        func([]streamtelemetry.WalkedRoute)
	OnServerSettingUpdated func(ctx context.Context, key, value string)
	// BeforeServerSettingCommit runs inside an admin settings save's
	// transaction for each changed setting; an error rolls the save back.
	BeforeServerSettingCommit func(ctx context.Context, tx pgx.Tx, key, value string) error
	// AdminDefaults is the node's shared snapshot of the administrator's
	// setting defaults. Without it the router builds one from its settings
	// repo, which then refreshes only on its own admin writes and its TTL.
	AdminDefaults        *config.AdminDefaults
	RequestServerRestart func(ctx context.Context) error
	StorageTransition    *storagetransition.Service
	// Backups owns the Backups page: listing, back up now, delete, and
	// downloads. Nil when no database is wired.
	Backups             *backup.Service
	ServerRestartStatus *handlers.ServerRestartStatusTracker
	// ProfileTransfer exports and imports a profile's own data; nil on the
	// SQLite user store.
	ProfileTransfer *backup.ProfileTransfer

	// UserCollectionSync handles per-profile imported collections (TMDB /
	// MDBList) — the user-facing counterpart of CollectionService.
	UserCollectionSync      *usercollections.Service
	UserCollectionScheduler *usercollections.Scheduler
	// PersonalCollectionCollages serves and builds personal collection
	// collages. main.go builds it beside UserCollectionSync, before scheduled
	// syncs start, and hands it to both; the router gives it its generator
	// once the poster signer exists. Nil builds one here when artwork
	// storage is configured.
	PersonalCollectionCollages *catalog.PersonalCollectionCollages

	// TrendingRefresher refreshes the persisted trending_discover snapshots.
	// Built in main.go with TMDB wired.
	TrendingRefresher *sections.TrendingRefresher

	// SeasonalListRefresher downloads the MDBList lists seasonal rows show.
	// Built in main.go alongside the trending refresher.
	SeasonalListRefresher *sections.SeasonalListRefresher

	// MDBListClient is used by user-facing list discovery endpoints
	// (search/top). May be nil; the handlers report "not configured" in
	// that case rather than failing.
	MDBListClient *mdblist.Client

	// TMDB is the process's one TMDB client, built in main.go: request
	// discovery, the collection sources, and their syncs share its response
	// caches and its request budget. Nil builds one for this router.
	TMDB *tmdb.Client

	// Admin dashboard aggregates, cached like AdminStatsProvider. Each is
	// optional: without one, the matching route queries Postgres per request.
	AdminPlaybackActivityProvider handlers.AdminPlaybackActivitySource
	AdminTopActivityProvider      handlers.AdminTopActivitySource
	AdminTimeseriesProvider       handlers.AdminTimeseriesSource
	AdminDownloadsStatsProvider   handlers.AdminDownloadsStatsSource

	// ABSHandler is the Audiobookshelf-compatible HTTP handler. When non-nil
	// it is mounted at the root router level (not under /api/v1/) so that ABS
	// clients hitting /login, /api/*, /abs/api/*, and /abs/socket.io/* all
	// resolve correctly. May be nil; no ABS routes are registered in that case.
	ABSHandler absHandler

	// LocalTLSPin is the pin of the key the API listener presents over TLS on
	// its own port (internal/lantls), published in the connections document.
	// Empty when the listener does not answer TLS (tests).
	LocalTLSPin string

	// PlaybackControlRedisClient records playback control lane ownership. Its
	// sockets honor context deadlines (cache.NewDeadlineRedisClientForRole),
	// so a stalled Redis cannot hold an administrator request or the
	// ownership worker past the relay's one-second bound. Without it there is
	// no control relay (may be nil).
	PlaybackControlRedisClient *redis.Client
}

// absHandler is the narrow interface the router needs from the ABS handler.
// Using an interface avoids a direct import of the abs sub-package from router.go.
type absHandler interface {
	Mount(r chi.Router)
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

// themeRouter routes theme audio with the same planner, token secret, recipe
// store and routing policy as video playback. The local AAC recipe is read
// from the playback handler's cached FFmpeg registry.
func (deps Dependencies) themeRouter(playbackHandler *handlers.PlaybackHandler) *themedelivery.Router {
	router := &themedelivery.Router{
		Secret:  func() string { return deps.CurrentConfig().Auth.JWTSecret },
		Recipes: noderecipe.NewStore(deps.RedisClient, 0),
		Policy:  func() config.PlaybackRoutingPolicy { return deps.CurrentConfig().Playback.Routing },
		LocalConversion: func(ctx context.Context) bool {
			return playbackHandler.LocalTransformationAvailableV3(ctx, playback.TransformationAudioToAACV3)
		},
	}
	// Assigned only when present: a nil *Planner in the interface would read
	// as a worker pool.
	if deps.NodePlanner != nil {
		router.Planner = deps.NodePlanner
	}
	return router
}

// recommendationReader builds a cache-backed recommendations reader. Its
// reads queue profile refreshes only while recommendations are enabled.
func (deps Dependencies) recommendationReader(repo *recommendations.Repo, ratingsRepo *catalog.RatingsRepo) *recommendations.Reader {
	var refresh recommendations.ReadRefreshRequester
	if deps.RecWorker != nil {
		refresh = deps.RecWorker
	}
	return recommendations.NewReader(repo, ratingsRepo, refresh, deps.UserStoreProvider).
		WithUserStoreOutsidePostgres(deps.Config != nil && deps.Config.UserDB.Backend == "sqlite").
		WithPersonalRows(deps.Config == nil || deps.Config.Recommendations.Enabled)
}

// invalidateNodeCapabilities drops every cached view of one node's hardware.
//
// There is more than one: protocol-v3 planning holds an inventory, and prepared
// downloads hold their own with its own TTL. A policy edit or a capability hash
// change invalidates the node itself, not one reader of it, so anything that
// caches the answer has to be told — otherwise a QSV-to-NVENC edit keeps
// selecting the node for a tone-map executor it no longer has, and the
// reconfigured worker rejects the recipe or the download falls back locally for
// no reason.
func (deps Dependencies) invalidateNodeCapabilities(playbackHandler *handlers.PlaybackHandler) func(nodeURL string) {
	return func(nodeURL string) {
		playbackHandler.RefreshNodeCapabilitiesV3(nodeURL)
		if deps.NodeCapabilityInvalidator != nil {
			deps.NodeCapabilityInvalidator(nodeURL)
		}
	}
}

// sealedHandler is what NewRouter hands out: the finished router behind an
// unexported field and a ServeHTTP method, nothing else. Its dynamic type is
// never a router, so no type assertion, alias, embedded interface, type switch
// or generic instantiation recovers a registration surface from it, and the
// route inventory refuses the reflect calls that could (MethodByName, Method,
// NumMethod, NewAt, UnsafePointer, UnsafeAddr, Pointer) and any import of
// unsafe in this package: short of unsafe, nothing gets the router back. That
// is the route inventory's guarantee that every route the API listener serves
// was registered inside newChiRouter, where the generator enumerates it. Do
// not embed http.Handler here: embedding exports the field and promotes its
// methods.
type sealedHandler struct {
	h http.Handler
}

func (s sealedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.h.ServeHTTP(w, r) }

// NewRouter builds the API listener's handler: the base middleware stack, every
// route under /api/v1/, and the version-neutral paths registered beside it.
//
// It returns a sealed http.Handler, never the chi surface. A caller holding the
// router could register routes on it after the fact — outside the route
// inventory's walk of newChiRouter and outside any gate — leaving the
// inventory short by exactly those routes with nothing to notice. The
// generator checks this shape: NewRouter must return sealedHandler wrapping the
// unexported constructor, and nothing else may call newChiRouter. A test that
// needs to walk the tree calls newChiRouter directly.
func NewRouter(deps Dependencies) http.Handler {
	return sealedHandler{h: newChiRouter(deps)}
}

// newChiRouter is the API listener's registration surface. The route
// inventory generator walks this function; every registration must be
// reachable from its body.
func newChiRouter(deps Dependencies) chi.Router {
	declareNativeMediaRoutes()
	r := chi.NewRouter()

	if deps.TMDB == nil {
		tmdbAPIKey := ""
		if deps.Config != nil {
			tmdbAPIKey = deps.Config.TMDBAPIKey
		}
		deps.TMDB = tmdb.NewClient(tmdbAPIKey, tmdb.RequestsPerSecond)
	}

	useBaseMiddleware(r, deps)
	if overlay := deps.overlayOrigins(); overlay != nil {
		handlers.SetWebSocketOverlayOrigins(overlay)
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

	readyHandler := handlers.NewReadyHandler(pgPinger, s3Checker, deps.Blobs.Assets)
	readyHandler.SetShuttingDown(deps.ShuttingDown)

	// Resolves whether a declared profile belongs to the user and is the
	// household primary profile. Nil (no user store) disables the
	// acting-admin profile policy, degrading admin routes to the plain
	// role check.
	var checkPrimaryProfile apimw.PrimaryProfileChecker
	// Reports whether a household has a PIN-protected or access-limited
	// profile, which withholds admin powers from a profile-less admin
	// request. Wired with checkPrimaryProfile; nil disables that check.
	var householdRequiresProfile apimw.HouseholdProfileRequirement
	// lookupProfile resolves a profile for the given user, returning nil when it
	// does not exist. Shared by the acting-admin primary check and the
	// diagnostics profile-attribution validator so both read profile state
	// (IsPrimary, IsChild) the same way.
	var lookupProfile func(ctx context.Context, userID int, profileID string) (*userstore.Profile, error)
	if deps.UserStoreProvider != nil {
		userStores := deps.UserStoreProvider
		lookupProfile = func(ctx context.Context, userID int, profileID string) (*userstore.Profile, error) {
			store, err := userStores.ForUser(ctx, userID)
			if err != nil {
				return nil, err
			}
			return store.GetProfile(ctx, profileID)
		}
		checkPrimaryProfile = func(ctx context.Context, userID int, profileID string) (bool, bool, error) {
			profile, err := lookupProfile(ctx, userID, profileID)
			if err != nil {
				return false, false, err
			}
			if profile == nil {
				return false, false, nil
			}
			return profile.IsPrimary, true, nil
		}
		householdRequiresProfile = func(ctx context.Context, userID int) (bool, error) {
			store, err := userStores.ForUser(ctx, userID)
			if err != nil {
				return false, err
			}
			profiles, err := store.ListProfiles(ctx)
			if err != nil {
				return false, err
			}
			return access.HouseholdRequiresProfile(profiles), nil
		}
	}

	var permissionPDP apimw.PermissionDecider
	if deps.PolicySystem != nil {
		permissionPDP = deps.PolicySystem.PDP()
	}

	// Admin authorization for routes: admin role, exercised through the
	// account's primary household profile.
	var actingAdmin apimw.ActingAdminDecider
	if deps.PolicySystem != nil {
		actingAdmin = apimw.NewPolicyActingAdminDecider(permissionPDP, checkPrimaryProfile, householdRequiresProfile)
	} else {
		// Legacy gate: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
		actingAdmin = apimw.NewActingAdminDecider(checkPrimaryProfile, householdRequiresProfile)
	}
	// Routes outside the admin groups that widen what an admin sees or may do
	// ask apimw.ActingAdmin, which answers with this same decision.
	r.Use(apimw.ProvideActingAdmin(actingAdmin))

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
	// Wrap it in the encrypting decorator so sensitive keys rest as ciphertext
	// and every consumer transparently reads plaintext.
	var settingsRepo catalog.SettingsStore
	if deps.DB != nil {
		settingsRepo = catalog.NewEncryptedSettingsRepo(catalog.NewServerSettingsRepo(deps.DB), deps.SecretCipher)
	}
	// One reader for access.unrated_content shared by every resolver built here.
	unratedContent := config.NewUnratedContentPolicy(settingsRepo)
	adminDefaults := deps.AdminDefaults
	if adminDefaults == nil {
		// Embeddings without cmd/silo's shared instance (tests, tools) get
		// their own, refreshed by this router's admin writes and its TTL.
		var reader config.ManySettingReader
		if many, ok := settingsRepo.(config.ManySettingReader); ok {
			reader = many
		}
		adminDefaults = config.NewAdminDefaults(reader)
	}
	var accessGroupStore *access.GroupStore
	if deps.DB != nil {
		accessGroupStore = access.NewGroupStore(deps.DB)
	}
	// Private S3 keeps its existing keys and presigned delivery. Without it,
	// bundles go to the operational blob store and download by streaming.
	var diagnosticsStore diagnostics.ObjectStore
	if deps.S3Private != nil {
		diagnosticsStore = diagnostics.NewS3ObjectStore(deps.S3Private)
	} else {
		diagnosticsStore = diagnostics.NewLocalObjectStore(deps.Blobs.Operational)
	}
	var diagnosticsHandler *handlers.DiagnosticsHandler
	if deps.DB != nil {
		diagnosticsService := diagnostics.NewService(
			diagnostics.NewPostgresRepository(deps.DB),
			settingsRepo,
			diagnosticsStore,
			slog.Default(),
		)
		if lookupProfile != nil {
			// Attribute reports only to a profile that belongs to the account and
			// is not a child profile; child profiles must not perform diagnostics
			// actions per the design, so reject their attribution here.
			diagnosticsService.SetProfileAttributionValidator(diagnostics.NewProfileAttributionValidator(
				func(ctx context.Context, userID int, profileID string) (bool, bool, error) {
					profile, err := lookupProfile(ctx, userID, profileID)
					if err != nil {
						return false, false, err
					}
					if profile == nil {
						return false, false, nil
					}
					return true, profile.IsChild, nil
				},
			))
		}
		diagnosticsHandler = handlers.NewDiagnosticsHandler(
			diagnosticsService,
		)
	}

	// Build auth handler and auth middleware if DB and config are available.
	var userRepo *auth.UserRepository
	var inviteCodeRepo *auth.InviteCodeRepository
	var invitationService *invitations.Service
	var passwordResetService *passwordreset.Service
	var apiKeyRepo *auth.APIKeyRepository
	var authService *auth.Service
	var authHandler *handlers.AuthHandler
	var authMiddleware *apimw.AuthMiddleware
	var viewerAccessMiddleware *apimw.ViewerAccessMiddleware
	var metadataCurationAccess func(http.Handler) http.Handler
	var markerEditAccess func(http.Handler) http.Handler
	var viewerResolver apimw.ViewerResolver
	var collectionOwners catalog.PersonalCollectionAccess
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
		if deps.OnConfigChange != nil {
			jwtForReload := jwtService
			deps.OnConfigChange(func(_, updated *config.Config) {
				jwtForReload.SetExpiries(updated.Auth.AccessTokenExpiry, updated.Auth.RefreshTokenExpiry)
			})
		}
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
		authService.OnSessionsRevoked(deps.OnUserSessionsRevoked)
		for _, registration := range deps.AuthProviders {
			authService.RegisterProvider(registration.Info, registration.Provider)
		}
		if deps.AuthProviderSource != nil {
			authService.SetPluginProviderSource(deps.AuthProviderSource)
		}
		if deps.AuthProviderRecheck != nil {
			authService.SetProviderRecheck(deps.AuthProviderRecheck)
		}
		signInAttempts := deps.SignInAttempts
		if signInAttempts == nil {
			signInAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.SignInNamePolicy)
		}
		authService.SetSignInAttempts(signInAttempts)
		if settingsRepo != nil {
			invitationService = invitations.NewService(
				invitations.NewRepository(deps.DB),
				userRepo,
				auth.NewAccountProvisioner(userRepo, deps.UserStoreProvider),
				authService,
				mail.NewSMTPSender(settingsRepo),
				settingsRepo,
				deps.EmailBrand,
				"",
			)
			passwordResetService = passwordreset.NewService(
				passwordreset.NewRepository(deps.DB),
				userRepo,
				authService,
				mail.NewSMTPSender(settingsRepo),
				settingsRepo,
				deps.EmailBrand,
				"",
			)
			passwordResetService.OnSessionsRevoked(deps.OnUserSessionsRevoked)
		}
		profileTokenService = access.NewProfileTokenService(deps.Config.Auth.JWTSecret, 0)
		deviceLoginService = auth.NewDeviceLoginService(
			deps.DB,
			userRepo,
			jwtService,
			sessionRepo,
			deps.UserStoreProvider,
			profileTokenService,
		)
		if deviceLoginService != nil {
			quickConnectAttempts := deps.QuickConnectCodeAttempts
			if quickConnectAttempts == nil {
				quickConnectAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.QuickConnectCodePolicy)
			}
			deviceLoginService.WithQuickConnectAttempts(quickConnectAttempts)
		}
		authHandler = handlers.NewAuthHandler(authService, jwtService, deviceLoginService)
		authHandler.SetPrimaryProfileChecker(checkPrimaryProfile)
		if accessGroupStore != nil {
			authHandler.SetAccessGroupProvider(accessGroupStore)
		}
		authMiddleware = apimw.NewAuthMiddleware(jwtService, sessionRepo, apiKeyRepo, userRepo)
		if deps.UserStoreProvider != nil {
			if deps.PolicySystem != nil {
				viewerResolver = policy.NewViewerResolver(userRepo, deps.UserStoreProvider, profileTokenService, deps.PolicySystem.PDP(), accessGroupStore).WithUnratedContentPolicy(unratedContent).WithAdminDefaults(adminDefaults).WithSharedTables(deps.DB)
			} else {
				// Legacy resolver: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
				viewerResolver = access.NewResolver(userRepo, deps.UserStoreProvider, profileTokenService, accessGroupStore).WithUnratedContentPolicy(unratedContent).WithAdminDefaults(adminDefaults)
			}
			viewerAccessMiddleware = apimw.NewViewerAccessMiddleware(viewerResolver)
			// Shared personal collections are limited to their owner's
			// access, resolved by the same resolver the request gates use.
			collectionOwners = usercollections.NewOwnerAccess(viewerResolver)
		}
		if deps.DB != nil {
			metadataLibraries := apimw.NewPGMetadataTargetLibraryResolver(deps.DB)
			if deps.PolicySystem != nil {
				metadataCurationAccess = apimw.NewPolicyPermissionMiddleware(
					userRepo,
					metadataLibraries,
					checkPrimaryProfile,
					householdRequiresProfile,
					permissionPDP,
					accessGroupStore,
				).RequireMetadataCurationForItem
			} else {
				// Legacy permission middleware: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
				metadataCurationAccess = apimw.NewPermissionMiddleware(
					userRepo,
					metadataLibraries,
					checkPrimaryProfile,
					householdRequiresProfile,
					accessGroupStore,
				).RequireMetadataCurationForItem
			}
		}
		if deps.PolicySystem != nil {
			markerEditAccess = apimw.NewPolicyPermissionMiddleware(
				userRepo,
				nil, // marker gate does not resolve target libraries
				checkPrimaryProfile,
				householdRequiresProfile,
				permissionPDP,
				accessGroupStore,
			).RequireMarkerEdit
		} else {
			// Legacy gate: proxy/test wiring without a policy system. Production integrated/api modes always take the policy path. Removed with the legacy cleanup phase.
			markerEditAccess = apimw.NewPermissionMiddleware(
				userRepo,
				nil,
				checkPrimaryProfile,
				householdRequiresProfile,
			).RequireMarkerEdit
		}
	}
	if deps.SessionMgr != nil && userRepo != nil {
		deps.SessionMgr.SetLimitProvider(func(ctx context.Context, userID int) (playback.SessionLimits, error) {
			user, err := userRepo.GetByID(ctx, userID)
			if err != nil {
				return playback.SessionLimits{}, err
			}
			effective, err := access.EffectivePolicyForUser(ctx, user, accessGroupStore)
			if err != nil {
				return playback.SessionLimits{}, err
			}
			return playback.SessionLimits{
				MaxStreams:               effective.MaxStreams,
				MaxTranscodes:            effective.MaxTranscodes,
				TranscodingDisabled:      !effective.TranscodeAllowed,
				AudioTranscodingDisabled: !effective.AudioTranscodeAllowed,
			}, nil
		})
		if deps.PolicySystem != nil {
			deps.SessionMgr.SetAdmissionDecider(policy.NewPlaybackAdmissionDecider(deps.PolicySystem.PDP()))
		}
	}

	// Build library handler if folder repo is available.
	var libraryHandler *handlers.LibraryHandler
	if deps.FolderRepo != nil {
		libraryHandler = handlers.NewLibraryHandler(deps.FolderRepo, deps.LibraryIngester, userRepo, deps.DB, deps.Refresher, deps.AppContext)
		if accessGroupStore != nil {
			libraryHandler.AccessGroups = accessGroupStore
		}
		libraryHandler.EventBus = deps.EventBus
		libraryHandler.EventsHub = deps.EventsHub
		libraryHandler.ScanRegistry = deps.ScanRegistry
		libraryHandler.ScanQueue = deps.LibraryScanQueue
		libraryHandler.RealtimeMonitor = deps.LibraryMonitor
		libraryHandler.Trickplay = deps.Trickplay
		if deps.TaskManager != nil {
			libraryHandler.BoxSets = boxSetSyncStarter{tasks: deps.TaskManager}
		}
		libraryHandler.MovieMatchQueueRepo = deps.MovieMatchQueueRepo
		libraryHandler.SeriesMatchQueueRepo = deps.SeriesRootMatchQueueRepo
		libraryHandler.RawMatchBacklogRepo = deps.FileRepo
		if deps.Config != nil {
			libraryHandler.TVSeriesRootQueue = deps.Config.Matcher.TVSeriesRootQueueEnabled()
		}
		if deps.DB != nil {
			libraryHandler.JobRepo = adminjob.NewRepository(deps.DB)
		}

		// Library poster uploads are writable client-facing assets, so they
		// belong in the public assets bucket.
		libraryHandler.ArtworkStore = deps.Blobs.Assets
		libraryHandler.ArtworkResolver = deps.ArtworkResolver

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
	var catalogResolver *catalog.CatalogResolver
	var shuffleService *shuffle.Service
	var literaryWorkHandler *handlers.LiteraryWorkHandler
	var peopleHandler *handlers.PeopleHandler
	var itemRepo *catalog.ItemRepository
	var episodeRepo *catalog.EpisodeRepository
	var extraRepo *catalog.ExtraRepository
	var providerIDRepo *catalog.ProviderIDRepository
	var seasonRepo *catalog.SeasonRepository
	var detailSvc *catalog.DetailService
	var calendarRepo *catalog.CalendarRepository
	var calendarHandler *handlers.CalendarHandler
	var catalogSearchService *catalog.CatalogSearchService
	var webhookSyncHandler *handlers.WebhookSyncHandler
	var requestHandler *handlers.RequestsHandler
	// watchlistTitles is the one watchlist.Titles every surface shares: the
	// v2 title operations, promotion on library watchlist reads, the title
	// detail's repair observer and the profile purge.
	var watchlistTitles *watchlist.Titles
	var onboardingHandler *handlers.OnboardingHandler
	// Declared here (assigned in the playback block below) so the onboarding
	// gates closure can reference it before that block runs.
	var watchTogetherHandler *handlers.WatchTogetherHandler
	var autoscanHandler *handlers.AutoscanHandler
	var ebookReaderHandler *handlers.EbookReaderHandler
	var ebookProgressStore *handlers.PGEbookReaderProgressStore
	var ebookConfigStore *handlers.PGEbookReaderConfigStore
	var ebookAnnotationStore *handlers.PGEbookReaderAnnotationStore
	var watchlistRequestWithdrawer *mediarequests.Service
	if deps.DB != nil {
		ebookProgressStore = handlers.NewPGEbookReaderProgressStore(deps.DB)
		ebookConfigStore = handlers.NewPGEbookReaderConfigStore(deps.DB)
		ebookAnnotationStore = handlers.NewPGEbookReaderAnnotationStore(deps.DB)
		browseRepo := catalog.NewBrowseRepository(deps.DB)
		itemRepo = catalog.NewItemRepository(deps.DB)
		searchIndexEvents := catalog.NewSearchIndexEventRepository(deps.DB)
		if deps.CatalogSearchSettings != nil {
			catalogSearchService = catalog.NewCatalogSearchServiceFromSettings(
				*deps.CatalogSearchSettings,
				itemRepo,
				searchIndexEvents,
				deps.CatalogSearchVectorizer,
			)
		} else {
			catalogSearchService = catalog.NewCatalogSearchService(
				context.Background(),
				settingsRepo,
				itemRepo,
				searchIndexEvents,
				deps.CatalogSearchVectorizer,
			)
		}
		if catalogSearchService != nil {
			if deps.RedisClient != nil {
				catalogSearchService.WithSearchSessionStore(deps.RedisClient)
			}
			catalogSearchService.StartCoverageRefresh(deps.AppContext)
		}
		activeSearchProvider := catalog.SearchProviderPostgres
		if _, ok := catalogSearchService.Provider().(*catalog.MeilisearchSearchProvider); ok {
			activeSearchProvider = catalog.SearchProviderMeilisearch
		}
		searchIndexEvents.WithActiveProvider(activeSearchProvider)
		// Latch the provider for the package-level enqueue helpers used by
		// metadata/scanner/etc. so they skip the per-call settings lookup.
		catalog.SetActiveSearchIndexProvider(activeSearchProvider)
		itemRepo.WithSearchIndexEvents(searchIndexEvents)
		episodeRepo = catalog.NewEpisodeRepository(deps.DB)
		extraRepo = catalog.NewExtraRepository(deps.DB)
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
		literaryRepo := literaryworks.NewRepository(deps.DB)
		literaryService := literaryworks.NewService(literaryRepo)
		literaryWorkHandler = &handlers.LiteraryWorkHandler{Service: literaryService}
		detailSvc = catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, deps.PersonRepo, fileFetcher)
		detailSvc.SetFolderRepository(folderRepo)
		detailSvc.SetRootClaimRepository(rootClaimRepo)
		detailSvc.SetGroupClaimRepository(groupClaimRepo)
		detailSvc.SetWorkSummaryProvider(literaryRepo)
		detailSvc.SetLiteraryWorkLinker(literaryService)
		detailSvc.SetProbeEnsurer(deps.ProbeEnsurer)
		detailSvc.SetChapterThumbnailQueuer(deps.ChapterThumbnailQueuer)
		detailSvc.SetVirtualCandidateScoreSource(virtualCandidateScoreSource(deps.VirtualLibraryService))
		detailSvc.SetVirtualRankingSource(virtualCandidateRankingSource(deps.VirtualLibraryService))
		// Per-version source-mismatch rides the same process-local probe
		// verdict the versions check endpoint reports, so a detail page can
		// show "served X of declared Y" without a second probe or a second
		// verdict implementation.
		detailSvc.SetVersionSourceMismatchSource(handlers.VersionSourceMismatch)
		if deps.TrickplayReader != nil {
			detailSvc.SetTrickplayAvailability(deps.TrickplayReader)
		}
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
		if catalogSearchService != nil {
			itemsHandler.SetCatalogSearchProvider(catalogSearchService.Provider())
		}
		itemsHandler.SetPersonalCollectionAccess(collectionOwners)
		if deps.MarkerPopulation != nil {
			itemsHandler.MarkerPopulation = deps.MarkerPopulation
		}
		itemsHandler.MarkerFileResolver = deps.FileRepo
		itemsHandler.MarkerImageURLs = deps.ArtworkResolver
		itemsHandler.EventsHub = deps.EventsHub
		itemsHandler.UserRepo = userRepo
		if deps.UserStoreProvider != nil {
			itemsHandler.DeviceProfileReader = handlers.NewProviderDeviceCapabilitySource(deps.UserStoreProvider)
		}
		if accessGroupStore != nil {
			itemsHandler.AccessGroups = accessGroupStore
		}
		if requester, ok := deps.MetadataService.(handlers.MetadataRefreshRequester); ok {
			itemsHandler.SetMetadataRefreshRequester(requester)
		}
		if requester, ok := deps.MetadataService.(handlers.TrailerRefreshRequester); ok {
			// Share the process's configured limiter so the per-user budget
			// is one budget on Redis deployments rather than one per
			// instance. Nil when rate limiting is disabled; the handler then
			// keeps its private in-memory fallback.
			itemsHandler.SetTrailerRefreshLimiter(deps.RateLimitMW.SharedLimiter())
			itemsHandler.SetTrailerRefreshRequester(requester)
		}
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalWatchEventDispatcher); ok {
			itemsHandler.SetLocalWatchEventDispatcher(dispatcher)
		}
		if deps.WatchCompletionObserver != nil {
			itemsHandler.SetCompletionObserver(deps.WatchCompletionObserver)
		}
		if ebookProgressStore != nil {
			itemsHandler.SetEbookReaderProgressStore(ebookProgressStore)
		}
		if deps.FileRepo != nil {
			ebookReaderHandler = handlers.NewEbookReaderHandler(&handlers.MediaFileAuthorizer{
				FileResolver:  deps.FileRepo,
				ItemAccess:    itemRepo,
				EpisodeLookup: episodeRepo,
				ExtraLookup:   extraRepo,
			})
			if ebookProgressStore != nil {
				ebookReaderHandler.ProgressStore = ebookProgressStore
			}
			if ebookConfigStore != nil {
				ebookReaderHandler.ConfigStore = ebookConfigStore
			}
			if ebookAnnotationStore != nil {
				ebookReaderHandler.AnnotationStore = ebookAnnotationStore
			}
			if conv := buildEbookConversion(deps, settingsRepo); conv != nil {
				ebookReaderHandler.Conversion = conv
			}
		}
		// deps.UserStoreProvider is the notification-wrapped provider, so a
		// promoted series queues an interest recompute. The promotion effects
		// are the personal data handler, wired once it exists.
		watchlistTitles = watchlist.NewTitles(deps.DB, itemRepo, deps.UserStoreProvider, deps.TMDB, nil)

		catalogResourceHandler = handlers.NewCatalogResourceHandler(itemsHandler)
		catalogResourceHandler.SetWatchlistPromoter(watchlistTitles)
		catalogResolver = catalog.NewCatalogResolver(browseRepo, itemRepo).
			WithEpisodeRepository(episodeRepo).
			WithUserStoreProvider(deps.UserStoreProvider).
			WithSearchProvider(catalogSearchService.Provider()).
			WithWatchlistPromoter(watchlistTitles).
			WithPersonalCollectionAccess(collectionOwners)
		catalogHandler = handlers.NewCatalogHandler(catalogResolver, itemsHandler)
		shuffleService = shuffle.NewService(deps.DB, catalogResolver)
		catalogHandler.SetWorkSummaryProvider(literaryRepo)
		catalogHandler.SetBoxSetRepository(catalog.NewBoxSetRepository(deps.DB))

		requestsRepo := mediarequests.NewRepository(deps.DB, deps.SecretCipher)
		requestSvc := mediarequests.NewService(
			requestsRepo,
			deps.TMDB,
			mediarequests.NewCatalogPresence(itemRepo, providerIDRepo),
		)
		AttachRequestRouter(requestSvc, deps.PluginService, deps.VirtualLibraryService)
		requestSvc.SetTitleObserver(watchlistTitles)
		discoverLists := discoverrows.NewSnapshotRepository(deps.DB)
		requestSvc.SetDiscoverLists(discoverLists, discoverrows.NewRefresher(requestsRepo, discoverLists, deps.TMDB))
		watchlistRequestWithdrawer = requestSvc
		requestSvc.SetWatchlistPreference(mediarequests.StoreWatchlistPreference{Stores: deps.UserStoreProvider})
		requestSvc.SetAnimeIndex(animeids.NewStore(deps.DB))
		requestSvc.SetCatalogChangeNotifier(sections.InvalidateResolvedListCache)
		requestSvc.SetVirtualMediaCleanup(func(ctx context.Context, req mediarequests.Request) error {
			tvdbID := ""
			if req.TVDBID != nil {
				tvdbID = strconv.Itoa(*req.TVDBID)
			}
			return itemRepo.CleanupRequestVirtualMedia(ctx, string(req.MediaType), req.TMDBID, tvdbID, req.IMDbID)
		})
		requestSvc.SetGroupPolicyProvider(accessGroupStore)
		requestSvc.SetProfileStores(deps.UserStoreProvider)
		if userRepo != nil {
			requestSvc.SetUserRepository(userRepo)
		}
		requestSvc.SetRequesterIdentityResolver(plugins.RequesterIdentityFromLookup(plugins.NewPgUserIdentityLookup(deps.DB)))
		if tvdbResolver, ok := deps.MetadataService.(mediarequests.TVDBIDResolver); ok {
			requestSvc.SetTVDBIDResolver(tvdbResolver)
		}
		if source, ok := deps.MetadataService.(SeriesTVDBSeasonSource); ok {
			requestSvc.SetSeriesSeasonProvider(NewSeriesSeasonProvider(source))
		}
		if viewerResolver != nil {
			requestSvc.SetEntitlementResolver(scopeEntitlementResolver{resolver: viewerResolver})
		}
		// Request lifecycle notifications (submitted / approved / declined):
		// server-channel broadcasts plus personal deliveries to the requester
		// on manual approve/decline. Fulfilled rides the reconcile service's
		// fulfillment notifier instead.
		if lifecycle := notifications.NewRequestLifecycleNotifier(deps.Notifications); lifecycle != nil {
			requestSvc.SetLifecycleNotifier(lifecycle)
		}
		requestHandler = handlers.NewRequestsHandler(requestSvc)
		requestHandler.ReleaseOverrides = catalog.NewReleaseOverrideRepository(deps.DB)

		// Onboarding tour manifest: gates consult live state at request time
		// so admin toggles apply without a restart. The watch-together gate
		// reads the handler variable assigned later in this function — by the
		// time requests are served it is settled.
		if deps.UserStoreProvider != nil {
			onboardingGates := onboarding.Gates{
				Requests: func(ctx context.Context) bool {
					settings, err := requestsRepo.GetSettings(ctx)
					return err == nil && settings.RequestsEnabled
				},
				WatchTogether: func(context.Context) bool {
					return watchTogetherHandler != nil
				},
				Recommendations: func(ctx context.Context) bool {
					if settingsRepo == nil {
						return false
					}
					enabled, err := settingsRepo.Get(ctx, "recommendations.enabled")
					return err == nil && enabled == "true"
				},
				Notifications: func(ctx context.Context) bool {
					// The in-app inbox always exists; the step is about the
					// wider system, so require a configured delivery channel.
					return settingsRepo != nil && mail.NewSMTPSender(settingsRepo).Enabled(ctx)
				},
				JellyfinCompat: func(ctx context.Context) bool {
					if settingsRepo == nil {
						return false
					}
					// Unset means the default applies, and the default is on
					// (config.DefaultAdminSettings) — only an explicit "false"
					// hides the step.
					enabled, err := settingsRepo.Get(ctx, "jellyfin_compat.enabled")
					if err != nil {
						return false
					}
					return strings.TrimSpace(enabled) != "false"
				},
			}
			onboardingHandler = handlers.NewOnboardingHandler(deps.UserStoreProvider, onboardingGates)
		}

		autoscanRepo := autoscan.NewRepository(deps.DB, deps.SecretCipher)
		if deps.FolderRepo != nil && deps.LibraryScanQueue != nil && deps.PluginService != nil {
			autoscanSvc := BuildAutoscanService(
				autoscanRepo,
				deps.PluginService,
				plugins.NewInstallationStore(deps.DB),
				requestsRepo,
				deps.FolderRepo,
				deps.LibraryScanQueue,
				deps.RedisClient,
			)
			autoscanHandler = handlers.NewAutoscanHandler(autoscanRepo, autoscanSvc)
			if deps.OnConfigChange != nil {
				deps.OnConfigChange(func(_, updated *config.Config) { autoscanHandler.SetPublicURL(updated.Server.PublicURL) })
			}
			// Wire the optional poll-task rescheduler so a settings change
			// re-applies the poll interval without a restart.
			if deps.TaskManager != nil {
				autoscanHandler.SetTriggerUpdater(deps.TaskManager)
			}
			// Fully qualify webhook URLs when the public base URL is known.
			autoscanHandler.SetPublicURL(deps.PublicURL)
			// The webhook route's autoscan_webhook budget only refuses unknown
			// tokens: arr does not retry a refused delivery, so one with a real
			// token over it is still admitted.
			if deps.RateLimitMW != nil {
				deps.RateLimitMW.AdmitOverEndpointBudget("autoscan_webhook", autoscanHandler.KnowsWebhookToken)
			}
		}

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
	var userImportHandler *handlers.UserCollectionImportHandler
	var settingsHandler *handlers.SettingsHandler
	var settingValuesHandler *handlers.SettingValuesHandler
	// One device-sightings recorder for every surface that registers the
	// request's device (legacy and canonical settings, playback start), so
	// they share one throttle window per (profile, device).
	deviceSightings := handlers.NewDeviceSightings()
	// userPluginSettingsHandler is the plugin handler the user-scoped
	// /settings/plugins routes are registered on; v2 shares it.
	var userPluginSettingsHandler *handlers.PluginHandler
	var deviceHandler *handlers.DeviceHandler
	var homeDismissalHandler *handlers.HomeDismissalHandler
	var subtitlePrefHandler *handlers.SubtitlePrefHandler
	var audioPrefHandler *handlers.AudioPrefHandler
	var libraryPlaybackPrefHandler *handlers.LibraryPlaybackPrefHandler
	var playbackSessionsLoader *handlers.PlaybackSessionsLoader
	// Personal collections without an uploaded or imported poster show a
	// collage of their titles; it needs artwork storage and poster signing.
	var personalCollages *catalog.PersonalCollectionCollages
	var personalMemberCounts *catalog.PersonalCollectionMemberCounts
	if deps.DB != nil {
		personalMemberCounts = catalog.NewPersonalCollectionMemberCounts(deps.DB)
	}
	if deps.DB != nil && detailSvc != nil {
		if gen := handlers.NewPersonalCollectionCollageGenerator(deps.Blobs.Assets, detailSvc, deps.ArtworkSigner, deps.ArtworkRepair, nil); gen != nil {
			personalCollages = deps.PersonalCollectionCollages
			if personalCollages == nil {
				personalCollages = catalog.NewPersonalCollectionCollages(deps.DB, nil)
			}
			personalCollages.SetCollageGenerator(gen)
		}
	}
	if deps.DB != nil {
		playbackSessionsLoader = handlers.NewPlaybackSessionsLoader(deps.DB, deps.UserStoreProvider, detailSvc)
	}

	if deps.UserStoreProvider != nil {
		profileHandler = handlers.NewProfileHandler(deps.UserStoreProvider)
		profileHandler.UserRepo = userRepo
		if accessGroupStore != nil {
			profileHandler.AccessGroups = accessGroupStore
		}
		profileHandler.EventsHub = deps.EventsHub
		if deps.RecWorker != nil {
			profileHandler.RecWorker = deps.RecWorker
		}
		if deps.DB != nil {
			// Drops live in Postgres whichever store holds the profile.
			profileHandler.DroppedSeriesPurger = catalog.NewDroppedSeriesRepo(deps.DB)
			profileHandler.WatchlistTitlesPurger = watchlistTitles
			// So do ratings and recommendation state; the Postgres store
			// already deleted them with the profile.
			profileHandler.RecommendationsPurger = recommendations.NewRepo(deps.DB)
			// Ebook reading data lives there too.
			profileHandler.EbookReaderPurger = handlers.NewPGEbookReaderProgressStore(deps.DB)
			profileHandler.Connections = &connectionstore.Store{Pool: deps.DB}
		}
		if watchlistRequestWithdrawer != nil {
			profileHandler.WatchlistRequestWithdrawer = watchlistRequestWithdrawer
		}
		profileHandler.ProfileTokens = profileTokenService
		profileHandler.PINAttempts = deps.ProfilePINAttempts
		if profileHandler.PINAttempts == nil {
			profileHandler.PINAttempts = ratelimit.NewMemoryAttemptLimiter(ratelimit.ProfilePINPolicy)
		}
		// Private S3 preserves existing avatar keys and presigned delivery. Local
		// avatars use the signed artwork endpoint. Never use public S3 here.
		profileHandler.AvatarStore = handlers.NewProfileAvatarStore(deps.Blobs)
		profileHandler.AvatarResolver = deps.ArtworkResolver
		profileHandler.SessionsReader = playbackSessionsLoader
		personalDataHandler = handlers.NewPersonalDataHandler(deps.UserStoreProvider, itemRepo)
		if detailSvc != nil {
			personalDataHandler.SetDetailService(detailSvc)
		}
		if ebookProgressStore != nil {
			personalDataHandler.SetEbookReaderProgressStore(ebookProgressStore)
		}
		personalDataHandler.SetEpisodeRepo(episodeRepo)
		personalDataHandler.SetSeasonRepo(seasonRepo)
		if watchlistTitles != nil {
			personalDataHandler.SetWatchlistTitles(watchlistTitles)
			watchlistTitles.SetEffects(personalDataHandler)
		}
		personalDataHandler.EventsHub = deps.EventsHub
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalListEventDispatcher); ok {
			personalDataHandler.SetLocalListEventDispatcher(dispatcher)
		}
		progressHandler = handlers.NewProgressHandler(deps.UserStoreProvider)
		progressHandler.EventsHub = deps.EventsHub
		if settingsRepo != nil {
			progressHandler.SettingsRepo = settingsRepo
		}
		if deps.DB != nil {
			progressHandler.LibraryLookup = catalog.NewLibraryItemRepository(deps.DB)
			progressHandler.EpisodeAliasLookup = catalog.NewEpisodeAliasRepository(deps.DB)
		}
		if episodeRepo != nil {
			progressHandler.EpisodeLookup = episodeRepo
		}
		collectionHandler = handlers.NewCollectionHandler(deps.UserStoreProvider)
		if deps.DB != nil {
			collectionHandler.Executor = &catalog.QueryExecutor{Pool: deps.DB}
		}
		collectionHandler.ArtworkStore = deps.Blobs.Assets
		collectionHandler.ArtworkResolver = deps.ArtworkResolver
		if detailSvc != nil {
			collectionHandler.ItemPosters = detailSvc
		}
		collectionHandler.CollectionOwners = collectionOwners
		collectionHandler.Collages = personalCollages
		collectionHandler.MemberCounts = personalMemberCounts
		// The import handler is built beside the collection handler so the v1
		// route group and the v2 operations share one instance; the v1 routes
		// keep their userImportHandler != nil condition.
		if deps.UserCollectionSync != nil {
			userImportHandler = handlers.NewUserCollectionImportHandler(
				deps.UserStoreProvider,
				deps.UserCollectionSync,
				deps.UserCollectionScheduler,
				nil,
				deps.MDBListClient,
				deps.FrontendFS,
			)
			userImportHandler.ArtworkStore = deps.Blobs.Assets
			userImportHandler.ArtworkResolver = deps.ArtworkResolver
		}
		settingsHandler = handlers.NewSettingsHandler(deps.UserStoreProvider)
		settingsHandler.DeviceSightings = deviceSightings
		settingsHandler.EventsHub = deps.EventsHub
		// A profile's hidden libraries are part of the scope its
		// recommendations are built under.
		if deps.RecWorker != nil {
			settingsHandler.RecWorker = deps.RecWorker
		}
		if settingsRepo != nil {
			settingsHandler.SetServerSettings(settingsRepo)
		}
		// The canonical settings API. main.go has already loaded and validated
		// the contract by the time the router is built, so a failure here is
		// unreachable — but the handler is simply omitted rather than panicking,
		// which degrades to "no typed settings routes" instead of no server.
		if contract, err := settingscontract.Load(); err == nil {
			settingValuesHandler = handlers.NewSettingValuesHandler(deps.UserStoreProvider, contract)
			settingValuesHandler.DeviceSightings = deviceSightings
			settingValuesHandler.EventsHub = deps.EventsHub
			if deps.RecWorker != nil {
				settingValuesHandler.RecWorker = deps.RecWorker
			}
			// Household management: a primary profile acting for another
			// profile on its own account. Without the token service a
			// PIN-locked primary cannot widen rather than widening unguarded.
			settingValuesHandler.SetAdminDefaults(adminDefaults)
			if deps.FolderRepo != nil {
				settingValuesHandler.SetLibraryLookup(deps.FolderRepo)
			} else if deps.DB != nil {
				settingValuesHandler.SetLibraryLookup(catalog.NewFolderRepository(deps.DB))
			}
			if deps.DB != nil {
				settingValuesHandler.SetLanguageSuggestionSource(catalog.NewBrowseRepository(deps.DB))
			}
		}
		deviceHandler = handlers.NewDeviceHandler(deps.UserStoreProvider)
		deviceHandler.EventsHub = deps.EventsHub
		if deps.DB != nil {
			deviceHandler.Connections = &connectionstore.Store{Pool: deps.DB}
		}
		homeDismissalHandler = handlers.NewHomeDismissalHandler(deps.UserStoreProvider)
		homeDismissalHandler.EventsHub = deps.EventsHub
		if deps.DB != nil {
			homeDismissalHandler.SetSeriesDrops(notifications.TrackDroppedSeries(catalog.NewDroppedSeriesRepo(deps.DB), deps.Notifications), itemRepo)
		}
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalDroppedEventDispatcher); ok {
			homeDismissalHandler.SetLocalDroppedEventDispatcher(dispatcher)
		}
		subtitlePrefHandler = handlers.NewSubtitlePrefHandler(deps.UserStoreProvider)
		subtitlePrefHandler.EventsHub = deps.EventsHub
		audioPrefHandler = handlers.NewAudioPrefHandler(deps.UserStoreProvider)
		audioPrefHandler.EventsHub = deps.EventsHub
		libraryPlaybackPrefHandler = handlers.NewLibraryPlaybackPrefHandler(deps.UserStoreProvider)
		libraryPlaybackPrefHandler.EventsHub = deps.EventsHub
		if deps.FolderRepo != nil {
			libraryPlaybackPrefHandler.SetLibraryLookup(deps.FolderRepo)
		} else if deps.DB != nil {
			libraryPlaybackPrefHandler.SetLibraryLookup(catalog.NewFolderRepository(deps.DB))
		}
	}

	// Build ratings handler if both repo and itemRepo are available.
	var ratingsHandler *handlers.RatingsHandler
	if ratingsRepo != nil && itemRepo != nil {
		ratingsHandler = handlers.NewRatingsHandler(ratingsRepo, itemRepo)
		// Fire rating.set outbound deliveries (webhooks with notify_ratings)
		// when notifications are wired. Best-effort and non-blocking.
		if deps.Notifications != nil && episodeRepo != nil {
			ratingsHandler.SetRatingNotifier(notifications.NewRatingNotifier(deps.Notifications, itemRepo, episodeRepo, seasonRepo))
		}
		if dispatcher, ok := deps.WatchProviderService.(handlers.LocalRatingEventDispatcher); ok {
			ratingsHandler.SetLocalRatingEventDispatcher(dispatcher)
		}
	}
	// Ratings, favorites, watchlist, progress and watched-state writes rebuild
	// the profile's recommendations when they are enabled.
	if deps.RecWorker != nil { //nolint:nestif // upstream newChiRouter in router.go exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		if ratingsHandler != nil {
			ratingsHandler.SetSignalsChangedNotifier(deps.RecWorker)
		}
		if personalDataHandler != nil {
			personalDataHandler.SetSignalsChangedNotifier(deps.RecWorker)
		}
		if progressHandler != nil {
			progressHandler.SetSignalsChangedNotifier(deps.RecWorker)
		}
		if itemsHandler != nil {
			itemsHandler.SetSignalsChangedNotifier(deps.RecWorker)
		}
	}

	// Create subtitleRepo early — only needs DB, shared with playback handler and subtitle search handler.
	var subtitleRepo *subtitles.PgRepository
	var subtitleManager *subtitles.Manager
	if deps.DB != nil && deps.S3Public != nil {
		subtitleRepo = subtitles.NewPgRepository(deps.DB, deps.SecretCipher)
		subtitleManager = subtitles.NewManager(subtitleRepo, blobstore.NewByteStore(blobstore.NewS3(deps.S3Public)))

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
	} else if deps.DB != nil {
		subtitleRepo = subtitles.NewPgRepository(deps.DB, deps.SecretCipher)
	}

	// Notifier that pushes "subtitle ready" events to active sessions when an AI
	// translation completes. Assigned inside the playback handler block where the
	// realtime hub and session manager are in scope; nil when playback is off.
	var subtitleAINotifier *playback.SubtitleReadyNotifier

	// Build playback handler if session manager is available.
	var playbackHandler *handlers.PlaybackHandler
	var adminPlaybackControlHandler *handlers.AdminPlaybackControlHandler
	var playbackCommandDispatcher *playback.CommandDispatcher
	var streamHandler *handlers.StreamHandler
	// virtualCandidatesRefresh is the explicit "Refresh List" service, wired to
	// the v2 media-candidates endpoint when the core virtual library and the
	// candidate sink are both available.
	var virtualCandidatesRefresh *handlers.VirtualCandidatesRefreshService
	var virtualIndexerReleases *handlers.VirtualIndexerReleaseService
	var virtualRefreshExecutor *handlers.VirtualCandidatesRefreshExecutor
	var virtualRefreshJobs *handlers.VirtualCandidatesRefreshJobService
	if deps.SessionMgr != nil {
		remoteStreamRelay := remotestream.NewRelay()
		if deps.AppContext != nil && deps.AppContext.Done() != nil {
			go func() {
				<-deps.AppContext.Done()
				closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := remoteStreamRelay.Close(closeCtx); err != nil {
					slog.Warn("close remote stream relay", "error", err)
				}
			}()
		}
		var playbackAdminStore handlers.PlaybackAdminStore
		if deps.DB != nil {
			playbackAdminStore = handlers.NewPGPlaybackAdminStore(deps.DB, deps.EventsHub)
		}
		if deps.FileRepo != nil {
			playbackHandler = handlers.NewPlaybackHandler(deps.SessionMgr, deps.FileRepo)
			// Virtual playback resolves exclusively through the core virtual
			// library: the virtual-library plugin is retired, so every virtual
			// URI — including rows left over from plugin ownership — resolves
			// by path through core.
			if deps.VirtualLibraryService != nil {
				playbackHandler.VirtualPlaybackResolver = handlers.VirtualPlaybackResolverFunc(func(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) (string, error) {
					// Serve-time single-URL resolve: no provider list runs
					// here, so no fetch log needs correlating. The request
					// id is still threaded for the refusal logs downstream.
					return deps.VirtualLibraryService.Resolve(virtuallibrary.WithRequestID(ctx, middleware.GetReqID(ctx)), path)
				})
				playbackHandler.VirtualPlaybackStreamLister = handlers.VirtualPlaybackStreamListerFunc(func(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) ([]handlers.VirtualPlaybackStream, error) {
					streams, err := deps.VirtualLibraryService.ListStreams(virtuallibrary.WithRequestID(ctx, middleware.GetReqID(ctx)), path)
					if err != nil {
						return nil, err
					}
					return virtualPlaybackStreamsFromCore(streams), nil
				})
				playbackHandler.VirtualPlaybackStreamSink = func(ctx context.Context, source *models.MediaFile, streams []handlers.VirtualPlaybackStream) error {
					candidates := make([]scanner.VirtualCandidate, 0, len(streams))
					for _, stream := range streams {
						if !strings.HasPrefix(stream.URI, "virtual://") || stream.URI == source.FilePath {
							continue
						}
						candidates = append(candidates, scanner.VirtualCandidate{
							OwnerInstallationID: stream.OwnerInstallationID,
							URI:                 stream.URI, Label: stream.Label, Resolution: stream.Resolution,
							CodecVideo: stream.CodecVideo, CodecAudio: stream.CodecAudio,
							HDR: stream.HDR, FileSize: stream.FileSize, Bitrate: stream.Bitrate,
							AudioLanguages: stream.AudioLanguages, SubtitleLanguages: stream.SubtitleLanguages,
							AudioChannels: stream.AudioChannels, HasAtmos: stream.HasAtmos,
							Is10Bit: stream.Is10Bit, IsMultiAudio: stream.IsMultiAudio,
							VisualTags:             stream.VisualTags,
							ResolvedURL:            stream.ProviderURL,
							ResolvedURLExpiresAt:   stream.ProviderExpiresAt,
							ProviderVideoHash:      stream.ProviderVideoHash,
							ProviderGUID:           stream.ProviderGUID,
							ProviderReleaseName:    stream.ProviderReleaseName,
							ProviderReleaseSize:    stream.FileSize,
							ProviderRequestHeaders: stream.RequestHeaders,
						})
					}
					return deps.FileRepo.ReplaceVirtualCandidates(ctx, source, candidates)
				}
				// The explicit "Refresh List" action re-lists the provider and
				// persists through the same sink, then answers the post-refresh
				// watch-detail versions. It needs the items handler for the
				// access-checked detail read.
				if itemsHandler != nil {
					virtualCandidatesRefresh = &handlers.VirtualCandidatesRefreshService{
						ListCached: playbackHandler.VirtualPlaybackStreamLister,
						ListFresh: handlers.VirtualPlaybackStreamListerFunc(func(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) ([]handlers.VirtualPlaybackStream, error) {
							// Explicit user refresh: a provider list does run,
							// so the fetch log is correlated like the start
							// path's listing.
							streams, err := deps.VirtualLibraryService.ListStreamsFresh(virtuallibrary.WithRequestID(ctx, middleware.GetReqID(ctx)), path)
							if err != nil {
								return nil, err
							}
							return virtualPlaybackStreamsFromCore(streams), nil
						}),
						Persist:      playbackHandler.VirtualPlaybackStreamSink,
						ContentFiles: deps.FileRepo.GetByContentID,
						EpisodeFiles: deps.FileRepo.GetByEpisodeID,
						Detail:       itemsHandler,
						// Catalog detail answers from the persisted list and lets
						// this discovery finish detached, so the persisted listing
						// must announce itself: the versions_updated event is what
						// refreshes an open detail page.
						Events: deps.RealtimeHub,
					}
					// The discovery path schedules the pre-play probe chain
					// after a successful detached persistence. Both sides stay
					// decoupled: the service knows only the callback, the
					// handler resolves its own target through the catalog's
					// version ordering (the same top entry the browse picker
					// defaults to) under the discovery request's access filter,
					// and the file repository.
					if detailSvc != nil && deps.FileRepo != nil {
						virtualCandidatesRefresh.ScheduleDefaultFileProbe = playbackHandler.ScheduleDefaultFileProbe
						// The same detail discovery resolves the default version
						// the watch page will play. Warm its provider fetch from
						// the resolved row, reusing the explicit-selection
						// warm-up entry point so admission and fill are identical.
						virtualCandidatesRefresh.ScheduleDefaultFileWarm = playbackHandler.ScheduleDefaultFileWarm
						playbackHandler.DefaultProbeTarget = handlers.NewVirtualDefaultProbeTarget(detailSvc, deps.FileRepo)
					}
					// Whatever paid for a provider listing — a detail read's
					// discovery, the explicit refresh job, or the async job —
					// hands that listing to the playback handler, so the start
					// that follows resolves from it instead of listing the
					// provider a second time for the same candidates.
					virtualCandidatesRefresh.SeedListing = playbackHandler.SeedVirtualListing
					// The indexer-release read/request surface and the async
					// refresh executor share the store the service owns.
					if store := deps.VirtualLibraryService.IndexerReleases(); store != nil {
						virtualIndexerReleases = &handlers.VirtualIndexerReleaseService{
							Store: store,
							Enqueue: handlers.VirtualIndexerReleaseEnqueue(func(ctx context.Context, downloadURL, name string) (string, error) {
								return deps.VirtualLibraryService.EnqueueIndexerRelease(ctx, downloadURL, name)
							}),
							Detail:       itemsHandler,
							ContentFiles: deps.FileRepo.GetByContentID,
							EpisodeFiles: deps.FileRepo.GetByEpisodeID,
						}
						virtualRefreshExecutor = &handlers.VirtualCandidatesRefreshExecutor{
							Refresh:            virtualCandidatesRefresh,
							ProviderRefresher:  deps.VirtualLibraryService,
							ReleaseDownloading: deps.VirtualLibraryService.ReleaseDownloading,
							ReleaseFailed:      deps.VirtualLibraryService.ReleaseFailed,
							MarkFailed:         deps.FileRepo.MarkVirtualCandidateFailed,
							MarkFailedDetailed: deps.FileRepo.MarkVirtualCandidateFailedDetailed,
							Pruner: &handlers.CandidateDeadPruner{
								ContentFiles:   deps.FileRepo.GetByContentID,
								EpisodeFiles:   deps.FileRepo.GetByEpisodeID,
								Delete:         deps.FileRepo,
								ProviderFailed: deps.VirtualLibraryService.ReleaseFailed,
							},
							Store:    store,
							Searcher: deps.VirtualLibraryService,
							Events:   deps.RealtimeHub,
						}
						virtualRefreshJobs = &handlers.VirtualCandidatesRefreshJobService{
							Detail:         itemsHandler,
							Items:          itemRepo,
							Episodes:       episodeRepo,
							ContentFiles:   deps.FileRepo.GetByContentID,
							EpisodeFiles:   deps.FileRepo.GetByEpisodeID,
							Jobs:           adminjob.NewRepository(deps.DB),
							CancelRegistry: deps.AdminJobCancelRegistry,
						}
					}
				}
				playbackHandler.VirtualFileLookup = func(ctx context.Context, path string) (*models.MediaFile, error) {
					file, err := deps.FileRepo.GetByPath(ctx, path)
					if err != nil && errors.Is(err, scanner.ErrFileNotFound) {
						return nil, handlers.ErrVirtualCandidateNotFound
					}
					return file, err
				}
				playbackHandler.VirtualCandidateFileLookup = func(ctx context.Context, path, contentID, episodeID string, ownerInstallationID int) (*models.MediaFile, error) {
					file, err := deps.FileRepo.GetVirtualCandidateByNeutralPath(ctx, path, contentID, episodeID, ownerInstallationID)
					if err != nil && errors.Is(err, scanner.ErrFileNotFound) {
						return nil, handlers.ErrVirtualCandidateNotFound
					}
					return file, err
				}
				playbackHandler.VirtualEpisodeFileLookup = func(ctx context.Context, episodeID string) (*models.MediaFile, error) {
					files, err := deps.FileRepo.GetByEpisodeID(ctx, episodeID)
					if err == nil && len(files) > 0 {
						return files[0], nil
					}
					return nil, errors.New("media file not found for episode")
				}
				playbackHandler.VirtualContentFileLookup = func(ctx context.Context, contentID string) (*models.MediaFile, error) {
					files, err := deps.FileRepo.GetByContentID(ctx, contentID)
					if err == nil && len(files) > 0 {
						return files[0], nil
					}
					return nil, errors.New("media file not found for content")
				}
			}
			streamHandler = handlers.NewStreamHandler(deps.SessionMgr, deps.FileRepo)
		} else {
			playbackHandler = handlers.NewPlaybackHandler(deps.SessionMgr)
			if deps.VirtualLibraryService != nil {
				playbackHandler.VirtualPlaybackResolver = handlers.VirtualPlaybackResolverFunc(func(ctx context.Context, path string, userID int, profileID string, ownerInstallationID int) (string, error) {
					return deps.VirtualLibraryService.Resolve(ctx, path)
				})
			}
		}
		if deps.AppContext != nil {
			// Parent detached virtual work to the server lifecycle so shutdown
			// cancels outstanding probes, revalidations, and searches.
			playbackHandler.ServiceContext = deps.AppContext
		}
		if deps.RegisterShutdownFunc != nil {
			// Primary trigger for evidence draining. The handler also has a
			// service-context watcher, but that is only a safety net: this
			// registration makes application shutdown call the drain and wait
			// for accepted, in-memory evidence rather than firing it detached.
			// The deferred-probe convergence watchdog is stopped in the same
			// step so a handler whose service context is never canceled still
			// terminates it; both stop methods are idempotent, and one step
			// keeps the handler's shutdown surface a single named unit.
			deps.RegisterShutdownFunc("virtual-evidence-drain", func() {
				playbackHandler.StopDeferredProbeWatchdog()
				playbackHandler.StopVirtualEvidence()
			})
		}
		if deps.VirtualLibraryService != nil {
			// The detailed resolver threads the edge id itself
			// (virtual_detailed_resolver.go); the plain adapters below are
			// serve-time single-URL paths with no provider list, so they
			// thread the id only for the refusal logs downstream.
			playbackHandler.VirtualMediaResolver = handlers.VirtualMediaResolverFunc(func(ctx context.Context, path string, ownerInstallationID int, userID int, profileID string) (string, error) {
				return deps.VirtualLibraryService.Resolve(virtuallibrary.WithRequestID(ctx, middleware.GetReqID(ctx)), path)
			})
			playbackHandler.VirtualMediaRefreshResolver = handlers.VirtualMediaRefreshResolverFunc(func(ctx context.Context, path string, ownerInstallationID int, userID int, profileID string) (string, error) {
				return deps.VirtualLibraryService.Refresh(virtuallibrary.WithRequestID(ctx, middleware.GetReqID(ctx)), path)
			})
			playbackHandler.VirtualMediaDetailedResolver = newVirtualMediaDetailedResolver(deps.VirtualLibraryService)
			// Expose the provider-staleness capability the resolve probes before
			// it lists a provider. The monitor aggregates AltMount's
			// completed/failed snapshot and Prowlarr's RSS index staleness; the
			// wrapper puts it behind the resolve port so production exercises the
			// bounded refresh seam instead of no-oping it.
			if monitor := deps.VirtualLibraryService.Monitor; monitor != nil {
				playbackHandler.VirtualMediaDetailedResolver = virtualProviderStaleResolver{
					VirtualMediaDetailedResolver: playbackHandler.VirtualMediaDetailedResolver,
					stale:                        monitor.ProviderStale,
					refresh:                      monitor.RefreshStaleProvider,
				}
			}
		}
		playbackHandler.BestResultCache = handlers.NewVirtualBestResultCache(30*time.Minute, 512)
		// Watch-detail prefetch: warm virtual listings on detail view so the
		// later start finds a warm BestResultCache. A nil *FileRepository
		// must not be boxed into the interface (typed-nil would panic on
		// GetByID); prefetch stays disabled without it.
		if itemsHandler != nil && deps.FileRepo != nil {
			itemsHandler.SetVirtualPrefetcher(playbackHandler, deps.FileRepo)
			// Probe on select: a watch-detail read that names an unprobed
			// virtual version schedules the shared pre-play chain for that
			// row, so its track menus are populated before the first play.
			itemsHandler.SetVirtualSelectionProber(playbackHandler)
			// The versions check's single-file inventory mode runs that same
			// probe inline under its own short request bound, so a client that
			// asked for the tracks gets them without a second round trip.
			itemsHandler.SetVirtualSelectionPreprober(playbackHandler)
		}
		if deps.UserStoreProvider != nil {
			// Device-aware candidate ranking: resolve the caller's persisted
			// capability profile through the store provider, TTL-cached so
			// Postgres is never on the playback critical path.
			playbackHandler.DeviceCapabilitySource = handlers.NewProviderDeviceCapabilitySource(deps.UserStoreProvider)
		}
		if deps.PluginService != nil {
			// Provider config changes (new manifest URL, reconfiguration) make
			// cached result= URIs stale; drop them so the next play re-lists.
			deps.PluginService.AddLifecycleHook(func(context.Context) { playbackHandler.BestResultCache.Clear() })
		}
		playbackHandler.RemoteStreamRelay = remoteStreamRelay
		if settingsRepo != nil {
			store := settingsRepo
			plugins.CoreInsecureAllowed = func(ctx context.Context) bool {
				if ctx == nil {
					ctx = context.Background()
				}
				raw, err := store.Get(ctx, "virtual_library.allow_insecure_http")
				if err != nil || raw == "" {
					return false
				}
				on, err := strconv.ParseBool(strings.TrimSpace(raw))
				if err != nil {
					return false
				}
				return on
			}
			// Private stream destination posture for virtual traffic. Read
			// lazily from the settings store so an admin change to
			// virtual_library.allow_private_streams applies without restart.
			plugins.CorePrivateStreamsAllowed = func(ctx context.Context) bool {
				if ctx == nil {
					ctx = context.Background()
				}
				raw, err := store.Get(ctx, "virtual_library.allow_private_streams")
				if err != nil || raw == "" {
					return false
				}
				on, err := strconv.ParseBool(strings.TrimSpace(raw))
				if err != nil {
					return false
				}
				return on
			}
			// Candidate store window: how long a persisted virtual candidate
			// stays trusted for replay after its last listing or resolution.
			// 0 (the default) disables the window. Read lazily so an admin
			// change applies without a restart.
			virtualCandidateStoreWindow := func() time.Duration {
				raw, err := store.Get(context.Background(), "virtual_library.candidate_store_hours")
				if err != nil {
					// A settings-store outage disables the window rather than
					// guessing: the pre-window behavior is always safe.
					return 0
				}
				return config.VirtualCandidateStoreWindow(raw)
			}
			playbackHandler.VirtualCandidateTrustWindow = virtualCandidateStoreWindow
			if deps.FileRepo != nil {
				deps.FileRepo.SetVirtualCandidateStoreWindow(virtualCandidateStoreWindow)
			}
		}
		playbackHandler.AllowInsecureVirtual = func(installationID int) bool {
			// HTTP-manifest posture for virtual traffic comes solely from the
			// core virtual_library.allow_insecure_http opt-in: resolution is
			// core-only, so per-installation plugin config no longer applies.
			return plugins.CoreVirtualInsecureAllowed(context.Background())
		}
		playbackHandler.AllowPrivateStreams = func(installationID int) bool {
			// Private stream destination posture comes solely from the core
			// virtual_library.allow_private_streams opt-in: resolution is
			// core-only, so per-installation plugin config no longer applies.
			return plugins.CoreVirtualPrivateStreamsAllowed(context.Background())
		}
		if streamHandler != nil {
			streamHandler.RemoteStreamRelay = remoteStreamRelay
			streamHandler.VirtualMediaResolver = playbackHandler.VirtualMediaResolver
			streamHandler.VirtualMediaRefreshResolver = playbackHandler.VirtualMediaRefreshResolver
			streamHandler.VirtualMediaDetailedResolver = playbackHandler.VirtualMediaDetailedResolver
			streamHandler.AllowInsecureVirtual = playbackHandler.AllowInsecureVirtual
			streamHandler.AllowPrivateStreams = playbackHandler.AllowPrivateStreams
			streamHandler.VirtualCandidateTrustWindow = playbackHandler.VirtualCandidateTrustWindow
		}
		if deps.DB != nil {
			// Transport no-bytes failure path. The marker goes through the
			// scanner's fenced stamp rather than a bare `WHERE id=$1` update,
			// because a virtual candidate row's file_path is rewritten in place
			// when the session rotates to a sibling (ReplaceVirtualResultPin).
			// A no-bytes verdict for candidate A that lands after that rewrite
			// must not brand the healthy replacement B: the scanner fence
			// requires the row to still name the candidate the caller inspected
			// (file_path) and to still carry the failure state it observed
			// (failed_at). The same delivered-grace rule as
			// scanner.MarkVirtualCandidateFailed applies.
			if streamHandler != nil {
				streamHandler.VirtualCandidateFailMarker = scanner.NewFileRepository(deps.DB).MarkVirtualCandidateFailed
				// The recovered marker clears a known-bad stamp after the candidate
				// actually delivered media bytes. Fenced on the delivered candidate
				// identity AND the failure state observed at transport start: a row
				// rotated to a different candidate while the stream was being
				// delivered, or a newer failure on the delivered candidate, is
				// never cleared by a late delivery signal.
				streamHandler.VirtualCandidateRecoveredMarker = scanner.NewFileRepository(deps.DB).MarkVirtualCandidateRecovered
			}
			// Repeated input demux failures from a local transcode mean the
			// virtual candidate is bad, not that the transport should keep
			// rebuilding. Stamp the effective row known-bad (CAS-fenced on its
			// file_path) so the next failure recovery rotates candidates.
			//
			// This callback is wired only for the integrated/local executor
			// (TranscodeManager.OnDemuxFailure, forwarded in
			// playback_transport.go). A remote transcode node runs its own
			// ffmpeg and returns only manifest/segment bytes to this process:
			// its stderr is captured by the node's own log sink
			// (transcodenode.Server.SetFFmpegLogSink) and never reaches the
			// server, so the same bad source selected on a node is not stamped
			// and the session can loop rebuilding it. Closing that gap needs
			// node-side work to detect the repeated demux failure and report
			// the candidate identity; there is deliberately no stderr
			// forwarding protocol invented here.
			//
			// observedFailedAt is deliberately nil. The transcode-manager
			// callback carries only the candidate identity, and nil is the
			// correct comparison for this verdict: the file_path argument
			// already stops a rotated row from being stamped, and the
			// `failed_at IS NULL` arm refuses to overwrite a newer failure
			// stamp that landed while the transcode ran. If the row recovered
			// mid-transcode, the demux failure is fresh evidence against the
			// bytes that just failed, so stamping it again is correct.
			playbackHandler.TranscodeManager().OnDemuxFailure = func(ctx context.Context, fileID int, expectedFilePath string) error {
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(expectedFilePath)), "virtual://") {
					return nil
				}
				return scanner.NewFileRepository(deps.DB).MarkVirtualCandidateFailed(ctx, fileID, expectedFilePath, nil)
			}
			// A decoder-rejected source is the same verdict as a repeated demux
			// failure: the candidate is bad and the auto-pick must skip it. The
			// write goes through the one existing failed_at mechanism, fenced on
			// the candidate identity, but a decode verdict bypasses the
			// delivered-grace rule (MarkVirtualCandidateDecodeRejected): bytes
			// that cannot be decoded are not playable regardless of how
			// recently they delivered, so a repeatable decode rejection must not
			// be re-selected forever. The dropdown still shows the row for a
			// manual retry, and a later successful delivery clears the stamp.
			// As with OnDemuxFailure, observedFailedAt is deliberately nil: the
			// callback carries only the identity, the file_path arm rejects a
			// rotated row, and nil refuses to overwrite a newer failure stamp.
			playbackHandler.TranscodeManager().OnSourceRejected = func(ctx context.Context, fileID int, expectedFilePath string) error {
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(expectedFilePath)), "virtual://") {
					return nil
				}
				return scanner.NewFileRepository(deps.DB).MarkVirtualCandidateDecodeRejected(ctx, fileID, expectedFilePath, nil)
			}
			// The recovered marker clears a known-bad stamp after the candidate
			// actually delivered media bytes. The HLS/transcode serve path
			// reports a full segment as delivery evidence (first successful
			// segment), clearing exactly the failure state observed before the
			// delivering request, with the same identity/failed_at fence as the
			// direct-play path. A candidate rejected on hardware decode and then
			// delivered on a retry therefore re-enters the auto-pick without
			// waiting for a provider re-list.
			playbackHandler.VirtualCandidateRecoveredMarker = scanner.NewFileRepository(deps.DB).MarkVirtualCandidateRecovered
			// A successful same-identity re-resolve of a failed row clears the
			// stale verdict through the same fenced clear the versions check
			// uses, so the next replay is fast again instead of re-resolving on
			// every start.
			playbackHandler.VirtualCandidateClearFailedMarker = scanner.NewFileRepository(deps.DB).ClearVirtualCandidateFailed
			// The start path indicts a confirmed-dead candidate through the same
			// fenced failed_at stamp the serve layer and versions check use, so
			// a retry rotates to a sibling or fails fast instead of re-resolving
			// the release the provider just dropped. An empty provider listing
			// is not a dead verdict and is never stamped.
			playbackHandler.VirtualCandidateFailMarker = scanner.NewFileRepository(deps.DB).MarkVirtualCandidateFailed
			// Durable, cluster-wide consecutive-refusal state behind the bounded
			// evidence-adoption decision. One store instance serves both the
			// handler's rotate-target trigger and the shared SQL savers' refusal
			// recording, so a restart or a different replica continues the same
			// count.
			adoptionRefusalStore := scanner.NewFileRepository(deps.DB)
			playbackHandler.VirtualAdoptionRefusalStore = adoptionRefusalStore
			playbackHandler.VirtualFileSaver = func(ctx context.Context, args models.VirtualFilePersistArgs) (int64, error) {
				if deps.DB == nil {
					return 0, nil
				}
				return handlers.ExecVirtualFileMetadataUpdateWithRefusals(ctx, deps.DB, args, adoptionRefusalStore)
			}
			playbackHandler.VirtualFileMetadataSaver = func(ctx context.Context, args models.VirtualFilePersistArgs) (handlers.VirtualFileMetadataUpdateResult, error) {
				if deps.DB == nil {
					return handlers.VirtualFileMetadataUpdateResult{}, nil
				}
				return handlers.ExecVirtualFileMetadataUpdateResultWithRefusals(ctx, deps.DB, args, adoptionRefusalStore)
			}
			if streamHandler != nil {
				// The serve layer refreshes an expired stored URL through the
				// same Phase-1 saver as the transport resolve.
				streamHandler.VirtualFileSaver = playbackHandler.VirtualFileSaver
				streamHandler.VirtualFileMetadataSaver = playbackHandler.VirtualFileMetadataSaver
			}
			// Bounded background refresh of signed candidate URLs approaching
			// expiry, so a resume serves a live URL instead of paying a
			// provider round-trip. Inert when the candidate store window is
			// disabled (virtual_library.candidate_store_hours = 0).
			if playbackHandler.VirtualMediaDetailedResolver != nil {
				refresher := handlers.NewVirtualCandidateRefresher(
					scanner.NewFileRepository(deps.DB).ListVirtualCandidatesNeedingRefresh,
					playbackHandler.VirtualMediaDetailedResolver,
					playbackHandler.VirtualFileMetadataSaver,
					playbackHandler.VirtualFileSaver,
					playbackHandler.VirtualCandidateTrustWindow,
					deps.DB,
					nil,
				)
				appCtx := deps.AppContext
				if appCtx == nil {
					appCtx = context.Background()
				}
				go refresher.Run(appCtx)
			}
		}
		if deps.Config != nil {
			ffprobePath := scanner.FFprobePathFromFFmpeg(deps.Config.Playback.FFmpegPath)
			// The ffprobe evidence cache TTL is the admin Streaming setting
			// virtual_library.playback_probe_cache_minutes. Unset or
			// unreadable keeps the ten minutes the cache shipped with. It does
			// not share virtual_library.cache_ttl_minutes with the provider
			// listing cache; that key governs listing reuse only. The cache
			// is built once per boot, so changes apply after a restart.
			probeCacheTTL := time.Duration(config.DefaultVirtualLibraryPlaybackProbeCacheMinutes) * time.Minute
			if settingsRepo != nil {
				if raw, err := settingsRepo.Get(context.Background(), config.VirtualLibraryPlaybackProbeCacheMinutesSettingKey); err == nil {
					probeCacheTTL = time.Duration(config.VirtualLibraryPlaybackProbeCacheMinutes(raw)) * time.Minute
				}
			}
			virtualProbeCache := scanner.NewVirtualProbeCache(probeCacheTTL, 256)
			virtualSourceProberWithHeaders := func(ctx context.Context, sourceURL string, file *models.MediaFile, headers map[string]string) (*models.MediaFile, error) {
				return virtualProbeCache.Probe(ctx, sourceURL, file, func(probeCtx context.Context, probeURL string, probeFile *models.MediaFile) (*models.MediaFile, error) {
					// Keep ffprobe behind the same pinned-IP relay as playback. A
					// direct provider URL would let ffprobe resolve DNS independently
					// of the server's SSRF policy. The core
					// virtual_library.allow_private_streams opt-in governs
					// probe destinations now that resolution is core-only.
					var relayURL string
					var release func()
					var relayErr error
					insecureProbe := plugins.CoreVirtualPrivateStreamsAllowed(context.Background())
					if insecureProbe {
						relayURL, release, relayErr = remoteStreamRelay.RegisterInsecureWithHeaders(probeCtx, probeURL, headers)
					} else {
						relayURL, release, relayErr = remoteStreamRelay.RegisterWithHeaders(probeCtx, probeURL, headers)
					}
					if relayErr != nil {
						return probeFile, relayErr
					}
					defer release()
					return scanner.ProbeVirtualSource(
						probeCtx,
						ffprobePath,
						deps.Config.Playback.FFmpegPath,
						relayURL,
						probeFile,
						func(dvCtx context.Context, input string) bool {
							return playback.DVRPUStrippable(dvCtx, deps.Config.Playback.FFmpegPath, input)
						},
					)
				})
			}
			playbackHandler.VirtualPlaybackSourceProberWithHeaders = virtualSourceProberWithHeaders
			playbackHandler.VirtualPlaybackSourceProber = func(ctx context.Context, sourceURL string, file *models.MediaFile) (*models.MediaFile, error) {
				return virtualSourceProberWithHeaders(ctx, sourceURL, file, nil)
			}
			// Cache-only recovery for the probe-failure damper: a probe that
			// outlived its caller's wait may have completed and landed here.
			playbackHandler.VirtualProbeCacheLookup = func(sourceURL string, file *models.MediaFile) *models.MediaFile {
				return virtualProbeCache.Lookup(sourceURL, file)
			}
		}
		wireVirtualMediaResolver(playbackHandler, deps)
		if streamHandler != nil {
			streamHandler.VirtualMediaResolver = playbackHandler.VirtualMediaResolver
			streamHandler.VirtualMediaRefreshResolver = playbackHandler.VirtualMediaRefreshResolver
			streamHandler.VirtualMediaDetailedResolver = playbackHandler.VirtualMediaDetailedResolver
		}
		if streamHandler != nil && deps.VirtualLibraryService != nil {
			sh := streamHandler
			svc := deps.VirtualLibraryService
			// Cache handoff: AltMount's once-per-release completion signal drives
			// an in-session rebind to the cached copy of the same release, and
			// download.progress rides the playback realtime hub. Vio never
			// fetches media: a fill only re-requests the provider's addon stream
			// listing, which is what makes AltMount queue the release for import.
			if deps.PlaybackRealtimeHub != nil {
				sh.DownloadProgress = deps.PlaybackRealtimeHub
			}
			sh.VirtualReleaseCacheStatus = func(ctx context.Context, virtualURI string, ownerInstallationID int) (bool, bool) {
				if deps.FileRepo == nil || svc.Monitor == nil {
					return false, false
				}
				file, err := deps.FileRepo.GetByPath(ctx, virtualURI)
				if err != nil || file == nil || strings.TrimSpace(file.ProviderReleaseName) == "" {
					return false, false
				}
				return svc.Monitor.ReleaseCached(file.ProviderReleaseName)
			}
			sh.VirtualReleaseCacheFiller = handlers.VirtualReleaseCacheFillFunc(func(ctx context.Context, virtualURI string, ownerInstallationID int, report func(playback.DownloadProgressPayload)) error {
				if deps.FileRepo == nil || svc.Monitor == nil {
					return nil
				}
				file, err := deps.FileRepo.GetByPath(ctx, virtualURI)
				if err != nil {
					// Best-effort: a row that cannot be read or is unknown has
					// no fill to start, and the session keeps streaming the
					// provider source. A missing row is not a fill failure.
					return nil //nolint:nilerr // deliberate best-effort on an unreadable/unknown row
				}
				if file == nil {
					return nil
				}
				releaseName := strings.TrimSpace(file.ProviderReleaseName)
				if releaseName == "" {
					return nil
				}
				// The provider only fills a release it does not already hold;
				// an unconfigured AltMount (known=false) has nothing to fill.
				if cached, known := svc.Monitor.ReleaseCached(releaseName); !known || cached {
					return nil
				}
				// Serving the addon URL is the fill: force a fresh provider
				// listing so AltMount receives the request. The resolver only
				// exchanges stream JSON, never media bytes, so Vio never
				// fetches or stores the release. The listing's stream URL is
				// deliberately not handed to svc.Monitor.EnqueueRelease: that
				// path is AltMount's SABnzbd addurl, which expects the indexer's
				// NZB download URL, not the addon's stream URL, so enqueueing it
				// would make the provider reject or mis-parse the fill.
				if _, err := svc.Refresh(ctx, virtualURI); err != nil {
					return err
				}
				return nil
			})
			// Selection warm-up reuses the play-triggered fill above: an
			// explicit version pick starts the same provider listing, which is
			// what makes AltMount queue the release, so the first play does not
			// pay the cold fetch. Nil (no monitor or no virtual library service)
			// leaves selection warm-ups disabled.
			playbackHandler.VirtualReleaseCacheWarmup = sh.VirtualReleaseCacheFiller
			// The same completion signal makes a completed, duration-verified
			// pinned release sticky on the start path: a transient resolve
			// failure retries that release before the cross-release walk.
			playbackHandler.VirtualReleaseCacheStatus = sh.VirtualReleaseCacheStatus
			// Install the completion observer last so the waiter registry is
			// already wired when the first confirmation can arrive.
			svc.Monitor.SetReleaseConfirmedObserver(sh.HandleVirtualReleaseConfirmed)
		}
		playbackHandler.StreamTelemetry = deps.StreamTelemetry
		playbackHandler.ItemAccess = itemRepo
		playbackHandler.EpisodeLookup = episodeRepo
		playbackHandler.ExtraLookup = extraRepo
		playbackHandler.OriginalLangLookup = itemRepo
		playbackHandler.FFmpegLogSink = deps.FFmpegLogSink
		if deps.DB != nil {
			playbackHandler.PlanStoreV3 = planstore.NewPostgres(deps.DB)
			playbackHandler.Connections = &connectionstore.Store{Pool: deps.DB}
			playbackHandler.SourceBitrates = &catalog.SourceBitrates{Pool: deps.DB}
			// The v2 playback contract binds every mutation to this server's
			// installation identity; a client that read capabilities from a
			// different installation is refused. Nothing else depends on it, so a
			// failure to read it only leaves the v2 playback surface unconfigured.
			installationCtx := deps.AppContext
			if installationCtx == nil {
				installationCtx = context.Background()
			}
			if installationID, err := diagnostics.ServerInstanceID(installationCtx, catalog.NewServerSettingsRepo(deps.DB)); err != nil {
				slog.Warn("playback installation identity unavailable; v2 playback stays unconfigured", "component", "api", "error", err)
			} else {
				playbackHandler.InstallationID = installationID
			}
		}
		// The stream deny marker revokes a stopped session's tokens on every
		// replica. Nil-safe: without Redis a stopped session serves from a valid
		// token until the token expires, as before.
		playbackHandler.StreamDeny = playback.NewStreamDeny(deps.RedisClient)
		// Maintenance also bounds the in-memory fallback store: without it a
		// DB-less deployment accumulates attempts and replans forever.
		playbackHandler.StartV3Maintenance(deps.AppContext)

		// Wire UserStoreProvider for progress/history persistence.
		if deps.UserStoreProvider != nil {
			playbackHandler.StoreProvider = deps.UserStoreProvider
			playbackHandler.DeviceSightings = deviceSightings
		}
		playbackHandler.StableIdentityResolver = watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo)
		playbackHandler.CompletionObserver = deps.WatchCompletionObserver
		if scrobbler, ok := deps.WatchProviderService.(handlers.PlaybackWatchScrobbler); ok {
			playbackHandler.WatchScrobbler = scrobbler
		}
		playbackHandler.AdminStore = playbackAdminStore
		playbackHandler.EventsHub = deps.EventsHub
		playbackHandler.LifecycleEncoder = apiv2.PlaybackLifecycleEncoder{}
		playbackHandler.LiveRowJSON = deps.LiveSessionRowJSON
		if deps.FileRepo != nil {
			playbackHandler.MissingMarker = deps.FileRepo
		}
		if deps.SessionSyncer != nil {
			playbackHandler.SessionSyncer = deps.SessionSyncer
		}
		if streamHandler != nil {
			// Share the playback handler's transcode/reconstruct manager so a
			// direct/remux stream can rebuild its session from the token recipe
			// after a restart (same manager, same SessionManager).
			streamHandler.TM = playbackHandler.TranscodeManager()
			streamHandler.StreamDeny = playbackHandler.StreamDeny
			streamHandler.PlanStoreV3 = playbackHandler.PlanStoreV3
			streamHandler.FinishOtherEnd = playbackHandler.FinishOtherEnd
			if deps.Config != nil {
				streamHandler.JWTSecret = deps.Config.Auth.JWTSecret
			}
			streamHandler.AdminStore = playbackAdminStore
			streamHandler.EventsHub = deps.EventsHub
			streamHandler.SessionSyncer = deps.SessionSyncer
			if deps.FileRepo != nil {
				streamHandler.MissingMarker = deps.FileRepo
			}
		}
		if deps.FileRepo != nil && (playbackHandler.VirtualMediaDetailedResolver != nil || playbackHandler.VirtualMediaResolver != nil) {
			playbackHandler.TranscodeManager().ResolveInput = func(ctx context.Context, mediaFileID int, ownerInstallationID int, userID int, profileID string, canonicalPath string) (string, func(), error) {
				if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(canonicalPath)), "virtual://") {
					return canonicalPath, nil, nil
				}
				file, err := deps.FileRepo.GetByID(ctx, mediaFileID)
				if err != nil || file == nil {
					return "", nil, errors.New("virtual media file is unavailable")
				}
				if ownerInstallationID <= 0 {
					ownerInstallationID = file.VirtualOwnerInstallationID
				}
				if playbackHandler.VirtualMediaDetailedResolver != nil {
					// A reconstructed session re-resolves the release it was
					// already serving. Route it through the same stored-first,
					// trust-window, outage-retrying, same-release-rematching
					// seam the transport startup uses: a persisted provider URL
					// serves the rebuild with zero provider calls, a transient
					// provider listing is retried before failing, and a
					// genuinely different release is still refused rather than
					// silently swapped. The seam returns the relay-registered
					// input (the same shared relay), so it is returned directly.
					// The row's durable identity is threaded so a renumbered
					// same-release candidate is re-identified even when the
					// exact-path stored lookup misses because the session
					// already rebound.
					resolveCtx := virtuallibrary.WithPersistedCandidateIdentity(ctx, virtuallibrary.PersistedCandidateIdentity{
						VideoHash:   file.ProviderVideoHash,
						GUID:        file.ProviderGUID,
						ReleaseName: file.ProviderReleaseName,
						ReleaseSize: file.ProviderReleaseSize,
					})
					res, resCleanup, dErr := playbackHandler.ResolveVirtualTransportInput(resolveCtx, canonicalPath, ownerInstallationID, userID, profileID)
					if dErr != nil {
						return "", nil, dErr
					}
					return res.URL, resCleanup, nil
				}
				var resolved string
				var headers map[string]string
				if playbackHandler.VirtualMediaRefreshResolver != nil {
					resolved, err = playbackHandler.VirtualMediaRefreshResolver.RefreshVirtualMedia(
						ctx, canonicalPath, ownerInstallationID, userID, profileID,
					)
				} else {
					resolved, err = playbackHandler.VirtualMediaResolver.ResolveVirtualMedia(
						ctx, canonicalPath, ownerInstallationID, userID, profileID,
					)
				}
				if err != nil {
					return "", nil, err
				}
				// FFmpeg must never resolve provider DNS directly. Register the
				// refreshed URL with the pinned-IP relay and keep the registration
				// alive until the transcode session closes.
				if remoteStreamRelay == nil {
					return "", nil, errors.New("virtual media relay is unavailable")
				}
				var relayURL string
				var cleanup func()
				// Private stream destination posture for virtual traffic comes
				// solely from the core virtual_library.allow_private_streams
				// opt-in now that resolution is core-only.
				insecure := plugins.CoreVirtualPrivateStreamsAllowed(context.Background())
				if insecure {
					relayURL, cleanup, err = remoteStreamRelay.RegisterInsecureWithHeaders(ctx, resolved, headers)
				} else {
					relayURL, cleanup, err = remoteStreamRelay.RegisterWithHeaders(ctx, resolved, headers)
				}
				if err != nil {
					return "", nil, err
				}
				return relayURL, cleanup, nil
			}
			// Downloads prepare virtual titles through the same resolver rather
			// than a second rotation/trust implementation.
			if deps.ArtifactManager != nil {
				deps.ArtifactManager.SetVirtualInputResolver(playbackHandler)
			}
		}

		// Wire the optional node planner and JWT secret for node-aware stream URLs.
		if deps.NodePlanner != nil {
			playbackHandler.NodePlanner = deps.NodePlanner
		}
		if deps.Config != nil && deps.Config.Auth.JWTSecret != "" {
			playbackHandler.JWTSecret = deps.Config.Auth.JWTSecret
		}
		// Hand proxy nodes the recipes they serve header-authenticated sessions
		// from, so an attempt that negotiated authorized media origins egresses
		// from the pool instead of this server. Nil-safe: without Redis the
		// store reports itself disabled and every such attempt stays API-local.
		playbackHandler.ProxyGrantStore = noderecipe.NewProxyGrantStore(deps.RedisClient, 0)
		// Hand transcode nodes the recipes they rebuild header-authenticated remote
		// transcodes from after a restart. Same nil-safety: without Redis such a
		// session replans instead of recovering, as it did before.
		playbackHandler.NodeRecipeStore = noderecipe.NewStore(deps.RedisClient, 0)
		// Keep the recipe of a header-authenticated session server-side, the only
		// place it exists, so a restarted or different API process can rebuild the
		// playback session. Same nil-safety: without Redis such a session answers
		// 404 after a restart and the client starts a new attempt.
		playbackHandler.SessionRecipeStore = noderecipe.NewSessionRecipeStore(deps.RedisClient, 0)
		if streamHandler != nil {
			streamHandler.SessionRecipeStore = playbackHandler.SessionRecipeStore
		}
		if deps.Config != nil {
			playbackHandler.PlaybackConfig = func() config.PlaybackConfig {
				return deps.CurrentConfig().Playback
			}
			// In integrated mode this and the jellycompat sweep both scan the same
			// TranscodeDir but each snapshots only its own manager's live set, so a
			// >24h idle dir owned by the other manager can be reaped. Bounded and
			// safe: active dirs stay mtime-fresh (spared) and either side rebuilds
			// from its token/recipe, so the worst case is a wasted rebuild. A shared
			// active-set source across both managers would remove even that.
			playback.StartPeriodicOrphanCleanup(deps.AppContext, "api", deps.Config.Playback.TranscodeDir, playbackHandler.CleanupOrphanedTranscodes, playback.OrphanCleanupInterval)
			cleanupDone := playbackHandler.TranscodeManager().StartShutdownCleanup(deps.AppContext)
			if deps.RegisterShutdownWork != nil {
				deps.RegisterShutdownWork(cleanupDone)
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
		if detailSvc != nil {
			// A start plan's effective row must be a row the item's version list
			// carries, judged by that list's own projection.
			playbackHandler.VersionList = detailSvc
		}
		if subtitleRepo != nil {
			playbackHandler.SubtitleRepo = subtitleRepo
		}
		if deps.KeptSubtitles != nil {
			playbackHandler.SubtitleTrackFacts = deps.KeptSubtitles
			playbackHandler.SubtitlePrefetch = deps.SubtitlePrefetch
		}
		if subtitleManager != nil && itemRepo != nil {
			playbackHandler.VirtualSubtitleSearcher = func(
				ctx context.Context,
				contentID, imdbID, title string,
				year, season, episode, fileID int,
				languages []string,
			) {
				// Resolve missing metadata from the catalog.
				if imdbID == "" || title == "" {
					item, err := itemRepo.GetByID(ctx, contentID)
					if err != nil {
						slog.WarnContext(ctx, "auto-subtitle-search: resolve item", "component", "api", "content_id", contentID, "error", err)
						return
					}
					if imdbID == "" {
						imdbID = item.ImdbID
					}
					if title == "" {
						title = item.Title
					}
					if year == 0 {
						year = item.Year
					}
				}
				if title == "" {
					return
				}
				// Download the best match per language, then tell any open
				// playback session about each new track so its subtitle menu
				// updates mid-session. The notifier is assigned later in this
				// function; the closure only runs after wiring completes.
				var readyNotifier subtitles.SubtitleReadyNotifier
				if subtitleAINotifier != nil {
					readyNotifier = subtitleAINotifier
				}
				subtitles.DownloadBestMatches(ctx, subtitleManager, readyNotifier, fileID, subtitles.SearchRequest{
					IMDbID:    imdbID,
					Title:     title,
					Year:      year,
					Season:    season,
					Episode:   episode,
					Languages: languages,
				})
			}
		}
		if deps.RecWorker != nil {
			playbackHandler.SetSignalsChangedNotifier(deps.RecWorker)
		}
		playbackHandler.StartCapabilityWarmupV3(deps.AppContext)
		// The health sweep sees a node's capability hash change long before this
		// cache would expire, so let it invalidate directly. Wired here rather
		// than at checker construction because the handler does not exist yet.
		deps.NodeHealthChecker.SetCapabilitiesChangedCallback(deps.invalidateNodeCapabilities(playbackHandler))

		realtimeHub := deps.PlaybackRealtimeHub
		if realtimeHub == nil {
			realtimeHub = playback.NewRealtimeHub()
		}
		commandTracker := playback.NewCommandTracker()
		playbackHandler.RealtimeHub = realtimeHub
		playbackHandler.InstallAudioReconcileRearm(context.Background())
		playbackHandler.CommandTracker = commandTracker
		playbackHandler.CommandDispatcher = playback.NewCommandDispatcher(deps.SessionMgr, realtimeHub, commandTracker)
		playbackCommandDispatcher = playbackHandler.CommandDispatcher
		if deps.IntroAnalyzer != nil {
			playbackHandler.IntroAnalyzer = deps.IntroAnalyzer
		}
		if deps.IntroRepository != nil {
			playbackHandler.IntroRepository = deps.IntroRepository
		}
		playbackHandler.MarkerRegistry = deps.MarkerRegistry
		if deps.MarkerPopulation != nil {
			playbackHandler.MarkerPopulation = deps.MarkerPopulation
		}
		if deps.MarkerUpdateNotifier != nil {
			playbackHandler.MarkerUpdateNotifier = deps.MarkerUpdateNotifier
		} else {
			playbackHandler.MarkerUpdateNotifier = playback.NewMarkerUpdateNotifier(deps.SessionMgr, realtimeHub)
		}
		// NOTE (fork): upstream's CopySafetyRace construction was removed with
		// the fork's playback redesign, which replaced it with the
		// PlaybackCopySafetyRacer interface (see
		// internal/api/handlers/playback_copy_safety.go). Nothing wires the
		// concrete upstream racer here.
		// A resolver lets subtitle realtime events carry the combined ordinal
		// the new track will hold in the next plan. Without a file repository
		// the notifier still fires; its events just omit the track block.
		var subtitleInventoryResolver playback.SubtitleInventoryResolver
		if deps.FileRepo != nil {
			// subtitleRepo is a concrete pointer: pass it only when non-nil so
			// the resolver holds a nil interface rather than a typed nil.
			var subtitleReader subtitles.Repository
			if subtitleRepo != nil {
				subtitleReader = subtitleRepo
			}
			// The attempt store is an interface field: pass it only when set so
			// the resolver never holds a typed nil either.
			var attempts playback.PlanStoreV3
			if playbackHandler.PlanStoreV3 != nil {
				attempts = playbackHandler.PlanStoreV3
			}
			if resolver := handlers.NewSubtitleInventoryResolver(deps.FileRepo, subtitleReader, attempts); resolver != nil {
				subtitleInventoryResolver = resolver
			}
		}
		subtitleAINotifier = playback.NewSubtitleReadyNotifier(deps.SessionMgr, realtimeHub, subtitleInventoryResolver)
		if subtitleAINotifier != nil && deps.EventBus != nil {
			// The bus carries each message under the realtime event it becomes.
			publish := func(ctx context.Context, event playback.RealtimeEventName, payload string) error {
				return deps.EventBus.Publish(ctx, cache.ChannelPlayback, cache.Event{Type: string(event), Payload: payload})
			}
			subscribe := func(ctx context.Context, handler func(playback.RealtimeEventName, string)) error {
				return deps.EventBus.Subscribe(ctx, cache.ChannelPlayback, func(event cache.Event) {
					switch event.Type {
					case cache.EventSubtitleReady, cache.EventSubtitleTimingChanged, cache.EventSubtitleSyncUpdated:
						handler(playback.RealtimeEventName(event.Type), event.Payload)
					}
				})
			}
			busCtx := deps.AppContext
			if busCtx == nil {
				busCtx = context.Background()
			}
			if err := subtitleAINotifier.UseEventBus(busCtx, publish, subscribe); err != nil {
				slog.Warn("subscribe subtitle events failed", "component", "api", "error", err)
			}
		}
		adminPlaybackControlHandler = handlers.NewAdminPlaybackControlHandler(playbackHandler)
		// Administrator commands run on the replica holding the session's
		// control lane; the relay forwards them there over Redis.
		if relay := playback.NewControlRelay(realtimeHub, deps.PlaybackControlRedisClient); relay != nil && deps.EventBus != nil {
			publish := func(ctx context.Context, payload string) error {
				// The relay's own client, so a stalled Redis cannot hold the
				// publish past the reply timeout.
				return cache.PublishEvent(ctx, deps.PlaybackControlRedisClient, cache.ChannelPlaybackControl, cache.Event{Type: cache.EventPlaybackControlRelay, Payload: payload})
			}
			subscribe := func(ctx context.Context, handler func(string)) error {
				return deps.EventBus.Subscribe(ctx, cache.ChannelPlaybackControl, func(event cache.Event) {
					if event.Type == cache.EventPlaybackControlRelay {
						handler(event.Payload)
					}
				})
			}
			relayCtx := deps.AppContext
			if relayCtx == nil {
				relayCtx = context.Background()
			}
			adminPlaybackControlHandler.UseControlRelay(relay)
			if err := relay.Start(relayCtx, publish, subscribe); err != nil {
				slog.Warn("playback control relay unavailable; retrying subscription; administrator commands reach only sessions on this replica", "component", "api", "error", err)
			}
		}

		if deps.DB != nil && deps.FileRepo != nil && viewerResolver != nil && deps.Config != nil && detailSvc != nil {
			roomTokenService := watchtogether.NewRoomTokenService(deps.Config.Auth.JWTSecret, 24*time.Hour)
			watchTogetherService := watchtogether.NewService(
				watchtogether.NewRepository(deps.DB),
				deps.SessionMgr,
				deps.FileRepo,
				watchtogether.NewCatalogSelectionResolver(detailSvc),
				watchtogether.NewSuggestionRepository(deps.DB),
				watchtogether.NewProfileNameResolver(deps.UserStoreProvider),
			)
			watchTogetherService.SetPlaybackAttemptStore(playbackHandler.PlanStoreV3)
			if settingValuesHandler != nil {
				watchTogetherService.SetGuestControlPreferences(settingValuesHandler.WatchPartyGuestControlPreferences())
			}
			if err := watchTogetherService.SetClusterEventBus(deps.EventBus); err != nil {
				slog.Warn("watch together cluster synchronization unavailable", "error", err)
			}
			watchTogetherHandler = handlers.NewWatchTogetherHandler(
				watchTogetherService,
				viewerResolver,
				roomTokenService,
			)
			if deps.UserStoreProvider != nil {
				watchTogetherHandler.MemberState = watchtogether.NewMemberStateReader(
					deps.UserStoreProvider,
					episodeRepo,
					catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider),
				)
				watchTogetherHandler.Details = detailSvc
				watchTogetherHandler.MemberStateCatalog = itemRepo
			}
		}
	}

	// Wire the batched version liveness check onto the catalog resource
	// handler. The file repository provides row lookup and the failed_at
	// stamping; the playback handler's detailed resolver (when playback is
	// wired) resolves pinned virtual candidates through the provider. The
	// item/episode/extra repositories authorize each file for the requesting
	// profile before anything is resolved or stamped — the same access
	// checker instance the playback handler uses.
	if catalogResourceHandler != nil {
		if deps.FileRepo != nil {
			catalogResourceHandler.FileResolver = deps.FileRepo
			catalogResourceHandler.MarkVirtualFailed = deps.FileRepo.MarkVirtualCandidateFailed
			catalogResourceHandler.ClearVirtualFailed = deps.FileRepo.ClearVirtualCandidateFailed
		}
		// A fresh failed_at verdict from the liveness check changes the
		// persisted version list, so the check publishes the same
		// versions_updated catalog event as the Refresh List executor.
		catalogResourceHandler.Events = deps.RealtimeHub
		if playbackHandler != nil {
			// The liveness check adopts a same-release rematch through the same
			// saver the playback path uses, so a renumbered provider result id
			// is persisted instead of re-matched on every listing.
			catalogResourceHandler.VirtualFileMetadataSaver = playbackHandler.VirtualFileMetadataSaver
			catalogResourceHandler.VirtualFileSaver = playbackHandler.VirtualFileSaver
		}
		if itemRepo != nil {
			catalogResourceHandler.ItemAccess = itemRepo
		}
		if episodeRepo != nil {
			catalogResourceHandler.EpisodeLookup = episodeRepo
		}
		if extraRepo != nil {
			catalogResourceHandler.ExtraLookup = extraRepo
		}
		if playbackHandler != nil {
			catalogResourceHandler.VirtualResolver = playbackHandler.VirtualMediaDetailedResolver
		}
	}

	// Wire subtitle repo and S3 client onto streamHandler for S3-stored subtitle serving.
	if streamHandler != nil && subtitleRepo != nil && deps.S3Public != nil {
		streamHandler.SubtitleRepo = subtitleRepo
		streamHandler.S3Client = deps.S3Public
		streamHandler.S3Bucket = deps.S3Public.Bucket()
	}
	// Wire the subtitle repo and blob store onto streamHandler so downloaded
	// subtitles serve from whichever backend stores them.
	subtitleBlobs := blobstore.NewByteStore(deps.Blobs.Assets)
	if streamHandler != nil && subtitleRepo != nil && subtitleBlobs != nil {
		streamHandler.SubtitleRepo = subtitleRepo
		streamHandler.SubtitleBlobs = subtitleBlobs
	}
	if streamHandler != nil && subtitleRepo != nil {
		streamHandler.ExternalTimings = subtitleRepo
	}
	if streamHandler != nil && deps.SubtitlePlaySync != nil {
		streamHandler.PlaySync = deps.SubtitlePlaySync
	}
	if streamHandler != nil && deps.Config != nil {
		streamHandler.PlaybackConfig = func() config.PlaybackConfig {
			return deps.CurrentConfig().Playback
		}
		subtitleCache := playback.NewSubtitleCache(func() string {
			return deps.CurrentConfig().Playback.TranscodeDir
		})
		streamHandler.SubtitleCache = subtitleCache
		// Share the serve-path cache with the playback handler so a plan
		// can pre-warm virtual subtitle extracts before the first fetch.
		if playbackHandler != nil {
			playbackHandler.SubtitleCache = subtitleCache
		}
	}

	restartStatus := deps.ServerRestartStatus
	if restartStatus == nil {
		restartStatus = handlers.NewServerRestartStatusTracker()
	}
	serverControlHandler := handlers.NewServerControlHandler(deps.RequestServerRestart, playbackCommandDispatcher, restartStatus)

	// Build admin handler if we have a user repo.
	var adminHandler *handlers.AdminHandler
	var accessGroupHandler *handlers.AccessGroupHandler
	var catalogSeedHandler *handlers.CatalogSeedHandler
	var adminJobsHandler *handlers.AdminJobsHandler
	if userRepo != nil {
		adminHandler = handlers.NewAdminHandler(userRepo, deps.DB, deps.UserStoreProvider)
		adminHandler.SessionsLoader = playbackSessionsLoader
		adminHandler.DetailSvc = detailSvc
		adminHandler.EventBus = deps.EventBus
		adminHandler.EventsHub = deps.EventsHub
		adminHandler.ImpersonationService = authService
		adminHandler.StatsSource = deps.AdminStatsProvider
		adminHandler.WatchProviders = deps.WatchProviderRegistry
		adminHandler.PlaybackActivitySource = deps.AdminPlaybackActivityProvider
		adminHandler.TopActivitySource = deps.AdminTopActivityProvider
		adminHandler.TimeseriesSource = deps.AdminTimeseriesProvider
		adminHandler.DownloadsStatsSource = deps.AdminDownloadsStatsProvider
		adminHandler.RedisClient = deps.RedisClient
		adminHandler.RealtimeHub = deps.RealtimeHub
		adminHandler.AccessGroups = accessGroupStore
		adminHandler.BootstrapSensitiveConfigured = deps.BootstrapSensitiveConfigured
		adminHandler.BootstrapSensitiveValues = deps.BootstrapSensitiveValues
		adminHandler.RedisBootstrapAvailable = deps.RedisBootstrapAvailable
		adminHandler.RestartStatus = restartStatus
		adminHandler.CatalogSearchStatus = catalogSearchService
		adminHandler.DiagnosticsStore = diagnosticsStore
		if profileHandler != nil {
			adminHandler.ProfileAvatars = profileHandler
		}
		if watchlistTitles != nil && deps.DB != nil {
			adminHandler.WatchlistTitlesSweeper = watchlistTitles
		}
		if settingsRepo != nil {
			adminHandler.SettingsRepo = settingsRepo
		}
		adminHandler.Config = deps.Config
		adminHandler.ArtworkBackend = deps.ArtworkBackend
		if deps.OnUserSessionsRevoked != nil {
			adminHandler.OnUserSessionsRevoked = deps.OnUserSessionsRevoked
		}
		// Refresh the administrator defaults before the handler tells clients
		// to re-read, so their next request already sees the new default.
		adminHandler.OnServerSettingUpdated = func(ctx context.Context, key, value string) {
			adminDefaults.Changed(ctx, key)
			if deps.OnServerSettingUpdated != nil {
				deps.OnServerSettingUpdated(ctx, key, value)
			}
		}
		adminHandler.BeforeServerSettingCommit = deps.BeforeServerSettingCommit
		if deps.Backups != nil {
			adminHandler.BackupKeyCheck = deps.Backups.KeyCheck()
		}
		if deps.RecWorker != nil {
			adminHandler.RecWorker = deps.RecWorker
		}
	}
	if accessGroupStore != nil {
		accessGroupHandler = handlers.NewAccessGroupHandler(accessGroupStore)
		if deps.RecWorker != nil {
			accessGroupHandler.RecWorker = deps.RecWorker
		}
	}
	if deps.DB != nil {
		jobRepo := adminjob.NewRepository(deps.DB)
		// Avoid wrapping a nil store in a non-nil interface; handlers rely on
		// interface-nil checks to gate artifact features.
		var privateStore handlers.CatalogSeedArtifactStore
		if deps.S3Private != nil {
			privateStore = deps.S3Private
		} else if api := blobstore.NewBucketAPI(deps.Blobs.Operational); api != nil {
			privateStore = api
		}
		catalogSeedService := catalogseed.NewService(deps.DB, deps.PersonRepo, recommendations.NewRepo(deps.DB))
		if deps.Config != nil {
			catalogSeedService.WithEmbeddingModel(deps.Config.Recommendations.EmbeddingModel)
		}
		catalogSeedHandler = handlers.NewCatalogSeedHandler(catalogSeedService, jobRepo, privateStore)
		catalogSeedHandler.RealtimeHub = deps.RealtimeHub
		adminJobsHandler = handlers.NewAdminJobsHandler(jobRepo, privateStore)
		adminJobsHandler.CancelRegistry = deps.AdminJobCancelRegistry
		adminJobsHandler.RealtimeHub = deps.RealtimeHub
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
		adminMatchHandler.EventBus = deps.EventBus
		adminMatchHandler.Groups = scanner.NewScannedGroupRepository(deps.DB)
	}

	// Build admin split/merge handler for repairing wrong version groupings.
	var adminSplitHandler *handlers.AdminSplitHandler
	if itemRepo != nil && deps.DB != nil {
		var merger handlers.ItemMerger
		if m, ok := deps.MetadataService.(handlers.ItemMerger); ok {
			merger = m
		}
		adminSplitHandler = handlers.NewAdminSplitHandler(
			deps.DB,
			itemRepo,
			deps.MetadataService,
			merger,
			deps.Refresher,
			deps.Scanner,
			deps.FolderRepo,
		)
		if deps.RecWorker != nil {
			adminSplitHandler.SetRecommendationStaler(recommendations.NewRepo(deps.DB))
		}
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
		adminImageHandler.EventsHub = deps.EventsHub
		adminImageHandler.EventBus = deps.EventBus
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
		if deps.MarkerPopulation != nil {
			adminIntroHandler.OnlineMarkers = deps.MarkerPopulation
		}
		if playbackHandler != nil {
			adminIntroHandler.MarkerUpdateNotifier = playbackHandler.MarkerUpdateNotifier
		}
	}

	var markersHandler *handlers.MarkersHandler
	if deps.FileRepo != nil {
		var notifier handlers.PlaybackMarkerUpdateNotifier
		if playbackHandler != nil {
			notifier = playbackHandler.MarkerUpdateNotifier
		}
		var contributor handlers.MarkerContributor
		if deps.MarkerContributionService != nil {
			contributor = deps.MarkerContributionService
		}
		var contributions handlers.MarkerContributionLister
		if deps.MarkerContributionStore != nil {
			contributions = deps.MarkerContributionStore
		}
		markersHandler = handlers.NewMarkersHandler(
			deps.FileRepo, deps.FileRepo, contributor, contributions, notifier, slog.Default(),
		)
		markersHandler.BaseContext = deps.AppContext
		if deps.DB != nil {
			markersHandler.Libraries = catalog.NewFolderRepository(deps.DB)
		}
		markersHandler.MarkerImageURLs = deps.ArtworkResolver
		markersHandler.ThumbnailQueuer = deps.ChapterThumbnailQueuer
		if deps.MarkerPopulation != nil {
			markersHandler.MarkerPopulation = deps.MarkerPopulation
		}
		markersHandler.AuditHistory = deps.FileRepo
		if itemRepo != nil {
			markersHandler.Authorizer = &handlers.MediaFileAuthorizer{
				FileResolver:  deps.FileRepo,
				ItemAccess:    itemRepo,
				EpisodeLookup: episodeRepo,
				ExtraLookup:   extraRepo,
			}
		}
	}

	var adminMarkerProvidersHandler *handlers.AdminMarkerProvidersHandler
	if deps.MarkerRegistry != nil && deps.MarkerProviderConfig != nil {
		adminMarkerProvidersHandler = handlers.NewAdminMarkerProvidersHandler(
			deps.MarkerRegistry, deps.MarkerProviderConfig, deps.EventBus, slog.Default(),
		)
	}

	// Admin subtitle config handler only needs the DB repo — no S3 required.
	var adminSubtitleHandler *handlers.AdminSubtitleHandler
	var subtitleSearchHandler *handlers.SubtitleSearchHandler
	if subtitleRepo != nil {
		adminSubtitleHandler = handlers.NewAdminSubtitleHandler(subtitleRepo)
	}
	if deps.DB != nil && subtitleBlobs != nil && subtitleRepo != nil {
		mediaResolver := &pgSubtitleMediaResolver{pool: deps.DB}
		subtitleSearchHandler = handlers.NewSubtitleSearchHandler(subtitleManager, subtitleRepo, mediaResolver)
		if subtitleAINotifier != nil {
			subtitleSearchHandler.SetSubtitleReadyAnnouncer(subtitleAINotifier)
		}
		if deps.FileRepo != nil && settingsRepo != nil {
			syncService := newSubtitleSyncService(&deps, subtitleManager, subtitleRepo, settingsRepo, subtitleAINotifier)
			subtitleSearchHandler.SetSyncService(syncService, subtitleRepo)
			if deps.SubtitlePlaySync != nil {
				deps.SubtitlePlaySync.Set(syncService)
			}
		}
	}

	if adminSubtitleHandler != nil && deps.DB != nil && subtitleManager != nil {
		adminSubtitleHandler.SetDownloadedSubtitleDeps(deps.DB, subtitleManager)
	}

	// Build the AI subtitle handler (on-demand translation). Generated tracks are
	// stored as ordinary downloaded subtitles, so they reach every client through
	// the existing subtitle pipeline with no client changes.
	// Shared AI endpoint client + dispatch semaphore: subtitle translation/ASR
	// and metadata translation draw from one client and one concurrency bound.
	// Connection settings, models, toggles, and quotas hot-reload through
	// OnConfigChange; only the semaphore size (ai.max_concurrent_jobs) is
	// fixed at construction.
	var aiClient *llm.Client
	var chatGPT *chatgpt.Service
	if store, ok := settingsRepo.(chatgpt.Store); ok && deps.SecretCipher != nil {
		chatGPT = chatgpt.NewService(store)
		chatGPT.OnSettingUpdated = func(ctx context.Context, key, value string) {
			if deps.OnServerSettingUpdated != nil {
				deps.OnServerSettingUpdated(ctx, key, value)
			}
			if deps.EventBus != nil {
				_ = deps.EventBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventSettingsChanged, Payload: key})
			}
		}
	}
	aiConfig := func(cfg *config.Config) llm.Config {
		c := llmConfigFromServer(cfg)
		if chatGPT != nil {
			c.ChatGPTTokenSource = chatGPT.TokenSource
			c.ChatGPTTokenRejected = chatGPT.RejectToken
			c.ChatGPTPlanRejected = chatGPT.RejectPlanToken
			c.TextConfigSource = chatGPT.TextConfig
		}
		return c
	}
	var aiSem chan struct{}
	if deps.Config != nil {
		aiClient = llm.NewClient(aiConfig(deps.Config))
		aiSem = jobrunner.NewSemaphore(deps.Config.AI.MaxConcurrentJobs)
		if deps.OnConfigChange != nil {
			clientForReload := aiClient
			deps.OnConfigChange(func(_, updated *config.Config) {
				clientForReload.UpdateConfig(aiConfig(updated))
			})
		}
	}

	var subtitleAIHandler *handlers.SubtitleAIHandler
	if subtitleManager != nil && subtitleRepo != nil && deps.FileRepo != nil && deps.DB != nil && deps.Config != nil {
		aiCfg := effectiveSubtitleAIConfig(deps.Config)
		aiCfg.TextConfigSource = aiClient.TextConfig
		var aiNotifier subtitleai.Notifier
		if subtitleAINotifier != nil {
			aiNotifier = subtitleAINotifier
		}
		aiTranslator := subtitleai.NewLLMTranslator(aiClient, aiCfg.BatchSize, aiCfg.ContextNeighbors)
		aiTranscriber := subtitleai.NewWhisperTranscriber(aiClient, deps.Config.Playback.FFmpegPath, deps.Config.SubtitleAI.ASRChunkSeconds)
		aiService := subtitleai.NewService(
			deps.AppContext,
			aiCfg,
			subtitleai.NewPgJobRepository(deps.DB),
			aiTranslator,
			aiTranscriber,
			subtitleManager,
			subtitleRepo,
			deps.FileRepo,
			aiNotifier,
			deps.Config.Playback.FFmpegPath,
			slog.Default(),
			aiSem,
		)
		aiService.SetExternalTimings(subtitleRepo)
		if streamHandler != nil && streamHandler.SubtitleCache != nil {
			aiService.SetSubtitleCache(streamHandler.SubtitleCache)
		}
		aiService.Recover()
		if deps.OnConfigChange != nil {
			deps.OnConfigChange(func(_, updated *config.Config) {
				newCfg := effectiveSubtitleAIConfig(updated)
				newCfg.TextConfigSource = aiClient.TextConfig
				aiService.UpdateConfig(newCfg)
				aiTranslator.SetBatching(updated.SubtitleAI.BatchSize, updated.SubtitleAI.ContextNeighbors)
				aiTranscriber.SetExtraction(updated.Playback.FFmpegPath, updated.SubtitleAI.ASRChunkSeconds)
			})
		}
		subtitleAIHandler = handlers.NewSubtitleAIHandler(aiService)
		subtitleAIHandler.StoreProvider = deps.UserStoreProvider
		subtitleAIHandler.LiveNotifier = subtitleAINotifier
	}
	if subtitleSearchHandler != nil && deps.FileRepo != nil && itemRepo != nil {
		fileAuthorizer := &handlers.MediaFileAuthorizer{
			FileResolver:  deps.FileRepo,
			ItemAccess:    itemRepo,
			EpisodeLookup: episodeRepo,
			ExtraLookup:   extraRepo,
		}
		subtitleSearchHandler.FileAuthorizer = fileAuthorizer
		if subtitleAIHandler != nil {
			subtitleAIHandler.FileAuthorizer = fileAuthorizer
		}
	}

	// Metadata AI translation (descriptions into the localization tables).
	var metadataAIHandler *handlers.MetadataAIHandler
	if deps.DB != nil && deps.Config != nil && aiClient != nil {
		mtRepo := metadatatranslation.NewPgRepository(deps.DB)
		mtConfig := func(cfg *config.Config) metadatatranslation.Config {
			c := metadataAIConfigFromServer(cfg)
			c.TextConfigSource = aiClient.TextConfig
			return c
		}
		mtService := metadatatranslation.NewService(
			deps.AppContext,
			mtConfig(deps.Config),
			mtRepo,
			mtRepo,
			&metadatatranslation.CatalogLocalizationStore{
				Items:    catalog.NewMediaItemLocalizationRepository(deps.DB),
				Seasons:  catalog.NewSeasonLocalizationRepository(deps.DB),
				Episodes: catalog.NewEpisodeLocalizationRepository(deps.DB),
			},
			aiClient.SystemUserChat,
			aiSem,
			slog.Default(),
		)
		mtService.Recover()
		if deps.OnConfigChange != nil {
			deps.OnConfigChange(func(_, updated *config.Config) {
				mtService.UpdateConfig(mtConfig(updated))
			})
		}
		metadataAIHandler = handlers.NewMetadataAIHandler(mtService)
		if itemRepo != nil {
			metadataAIHandler.ItemAccess = itemRepo
			metadataAIHandler.SeasonLookup = seasonRepo
			metadataAIHandler.EpisodeLookup = episodeRepo
			if deps.FolderRepo != nil {
				metadataAIHandler.Libraries = deps.FolderRepo
			} else {
				metadataAIHandler.Libraries = catalog.NewFolderRepository(deps.DB)
			}
			if detailSvc != nil {
				metadataAIHandler.Pending = detailSvc
			}
			metadataAIHandler.ItemLibraries = itemRepo
			// The budget caps provider spend, so like the profile PIN
			// limiter it counts in Redis whenever Redis is configured,
			// whatever ratelimit.backend says, and per node only without
			// Redis.
			if deps.RedisClient != nil {
				metadataAIHandler.Limiter = ratelimit.NewRedisLimiter(deps.RedisClient)
			} else {
				metadataAIHandler.Limiter = ratelimit.NewMemoryLimiter()
			}
		}
		// Wire the refresh fallback: libraries with auto_translate_metadata get
		// missing localizations filled after each metadata refresh.
		if mt, ok := deps.MetadataService.(interface {
			SetAutoTranslator(metadata.AutoTranslator)
		}); ok {
			mt.SetAutoTranslator(mtService)
		}
	}

	// Build section handler if DB is available.
	var sectionHandler *handlers.SectionHandler
	var libraryCollectionHandler *handlers.LibraryCollectionHandler
	var libraryCollectionGroupHandler *handlers.LibraryCollectionGroupHandler
	libraryCollectionService := deps.CollectionService
	if deps.DB != nil {
		sectionRepo := sections.NewRepository(deps.DB)
		sectionFetcher := sections.NewFetcher(deps.DB)
		sectionFetcher.StoreProvider = deps.UserStoreProvider
		sectionFetcher.AdminDefaults = adminDefaults
		sectionFetcher.CollectionOwners = collectionOwners
		if watchlistTitles != nil {
			sectionFetcher.WatchlistPromoter = watchlistTitles
		}
		sectionFetcher.CollectionRepo = catalog.NewLibraryCollectionRepository(deps.DB)
		sectionFetcher.NextUpRepo = catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
		sectionFetcher.AudiobookNextRepo = catalog.NewAudiobookNextRepository(deps.DB)
		if deps.DB != nil {
			sectionFetcher.RecommendationRepo = recommendations.NewRepo(deps.DB)
			if ratingsRepo != nil {
				sectionFetcher.RecommendationReader = deps.recommendationReader(sectionFetcher.RecommendationRepo, ratingsRepo)
			}
		}
		sections.InstallRecipeDelegate(sectionFetcher)
		sectionHandler = handlers.NewSectionHandler(sectionRepo, sectionFetcher)
		catalogResolver.WithSectionResolver(sectionHandler)
		if deps.TrendingRefresher != nil {
			sectionHandler.TrendingRefresher = deps.TrendingRefresher
		}
		if deps.SeasonalListRefresher != nil {
			sectionHandler.SeasonalLists = deps.SeasonalListRefresher
		}
		sectionHandler.CollectionRepo = sectionFetcher.CollectionRepo
		sectionHandler.FolderRepo = deps.FolderRepo
		if deps.UserStoreProvider != nil {
			sectionHandler.StoreProvider = deps.UserStoreProvider
		}
		sectionHandler.EpisodeRepo = episodeRepo
		sectionHandler.DetailSvc = detailSvc
		if ebookProgressStore != nil {
			sectionHandler.EbookProgress = ebookProgressStore
		}
		if userRepo != nil {
			sectionHandler.UserRepo = userRepo
		}
		if accessGroupStore != nil {
			sectionHandler.AccessGroups = accessGroupStore
		}

		libraryCollectionRepo := catalog.NewLibraryCollectionRepository(deps.DB)
		var collectionSortCleaner *userstore.CollectionSortPreferenceCleaner
		var collectionRowRenamer *userstore.CollectionRowRenamer
		if userRepo != nil && deps.UserStoreProvider != nil {
			collectionSortCleaner = userstore.NewCollectionSortPreferenceCleaner(userRepo, deps.UserStoreProvider)
			collectionRowRenamer = userstore.NewCollectionRowRenamer(userRepo, deps.UserStoreProvider)
		}
		if collectionHandler != nil {
			collectionHandler.LibraryCollections = libraryCollectionRepo
		}
		sectionHandler.SortPreferenceCleaner = collectionSortCleaner
		if libraryCollectionService == nil {
			libraryCollectionService = catalog.NewLibraryCollectionService(
				libraryCollectionRepo,
				itemRepo,
				catalog.NewLibraryItemRepository(deps.DB),
				nil,
			)
		}
		if libraryCollectionService.TMDBCollections == nil {
			libraryCollectionService.TMDBCollections = &tmdbCollectionAdapter{client: deps.TMDB}
		}
		if libraryCollectionService.TMDBFranchises == nil {
			libraryCollectionService.TMDBFranchises = &tmdbFranchiseAdapter{client: deps.TMDB}
		}
		if libraryCollectionService.TMDBDiscovers == nil {
			libraryCollectionService.TMDBDiscovers = &tmdbDiscoverAdapter{client: deps.TMDB}
		}
		if libraryCollectionService.TMDBLists == nil {
			libraryCollectionService.TMDBLists = &tmdbListAdapter{client: deps.TMDB}
		}
		if libraryCollectionService.MDBListAPI == nil && deps.MDBListClient != nil {
			libraryCollectionService.MDBListAPI = deps.MDBListClient
		}
		// Virtual variants come only from the core virtual library: the
		// virtual-library plugin is retired, so without an active core
		// service there is no variants provider and virtual syncs fail
		// explicit (provider unavailable), never a silent downgrade.
		if deps.VirtualLibraryVariants != nil {
			libraryCollectionService.VirtualVariants = deps.VirtualLibraryVariants
		}
		if refresher, ok := deps.MetadataService.(interface {
			RefreshScheduledItem(context.Context, string) error
		}); ok {
			libraryCollectionService.RefreshVirtualItem = refresher.RefreshScheduledItem
		}

		// Propagate the now-wired TMDB fetchers to the user-side sync service
		// (constructed earlier in main.go before settingsRepo existed, so its
		// fetcher fields started nil).
		if deps.UserCollectionSync != nil {
			if deps.UserCollectionSync.TMDBCollections == nil {
				deps.UserCollectionSync.TMDBCollections = libraryCollectionService.TMDBCollections
			}
			if deps.UserCollectionSync.TMDBLists == nil {
				deps.UserCollectionSync.TMDBLists = libraryCollectionService.TMDBLists
			}
		}

		// Wire the trending snapshot reader into the section fetcher. The
		// trending_discover home section reads its list from the persisted
		// snapshot table; the upstream fetch happens out-of-band in the refresh
		// task, so the read path never calls the provider.
		sectionFetcher.TrendingSnapshots = sections.NewTrendingSnapshotRepository(deps.DB)
		sectionFetcher.SeasonalLists = sections.NewSeasonalListRepository(deps.DB)
		if sectionFetcher.RecommendationRepo != nil {
			sectionFetcher.SeasonalNeighbors = sectionFetcher.RecommendationRepo
		}

		libraryCollectionHandler = handlers.NewLibraryCollectionHandler(
			libraryCollectionRepo,
			libraryCollectionService,
			itemRepo,
			nil,
		)
		libraryCollectionHandler.ArtworkStore = deps.Blobs.Assets
		libraryCollectionHandler.ArtworkResolver = deps.ArtworkResolver
		libraryCollectionHandler.ArtworkSigner = deps.ArtworkSigner
		libraryCollectionHandler.FrontendFS = deps.FrontendFS
		libraryCollectionHandler.Executor = &catalog.QueryExecutor{Pool: deps.DB}
		libraryCollectionHandler.SectionRepo = sectionRepo
		libraryCollectionHandler.UserCollectionPool = deps.DB
		libraryCollectionHandler.CollectionOwners = collectionOwners
		libraryCollectionHandler.PersonalCollages = personalCollages
		libraryCollectionHandler.PersonalMemberCounts = personalMemberCounts
		libraryCollectionHandler.EventsHub = deps.EventsHub
		libraryCollectionHandler.SortPreferenceCleaner = collectionSortCleaner
		libraryCollectionHandler.CollectionRowRenamer = collectionRowRenamer
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
		libraryCollectionHandler.SubscribePurgeInvalidations(appCtx)
		if deps.DB != nil {
			sections.StartPurgeRevisionPoller(appCtx, deps.DB, 5*time.Second)
		}
		// The audiobook Authors, Narrators and Series tabs read precomputed
		// groups; the periodic refresh catches enrichment and edits.
		groupStats := catalog.NewAudiobookGroupStatsRefresher(deps.DB, 10*time.Minute, time.Minute, time.Minute)
		go groupStats.Run(appCtx)
		// A scan that adds or removes titles publishes scan_complete with the
		// library ID. Observing the publish, rather than subscribing, counts
		// each scan once on the node that ran it, with or without Redis.
		if observed, ok := deps.EventBus.(interface {
			ObservePublishes(cache.PublishObserver)
		}); ok {
			scheduler := catalog.NewSmartCountScheduler(refresher, 10*time.Second)
			observed.ObservePublishes(func(channel string, event cache.Event) {
				if channel != cache.ChannelCatalog || event.Type != cache.EventScanComplete {
					return
				}
				if libraryID, err := strconv.Atoi(event.Payload); err == nil {
					scheduler.LibraryChanged(libraryID)
				}
			})
			go scheduler.Run(appCtx)
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
			recsReader = deps.recommendationReader(recsRepo, ratingsRepo)
		}
		recsHandler = handlers.NewRecommendationsHandler(deps.Recommender, recsReader, deps.UserStoreProvider, ratingsRepo, recsRepo, deps.Recommender != nil).
			WithUserStoreOutsidePostgres(deps.Config != nil && deps.Config.UserDB.Backend == "sqlite")
		if deps.DB != nil {
			recsFetcher := sections.NewFetcher(deps.DB)
			recsFetcher.StoreProvider = deps.UserStoreProvider
			recsFetcher.AdminDefaults = adminDefaults
			recsFetcher.CollectionOwners = collectionOwners
			if watchlistTitles != nil {
				recsFetcher.WatchlistPromoter = watchlistTitles
			}
			recsFetcher.NextUpRepo = catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider)
			recsFetcher.AudiobookNextRepo = catalog.NewAudiobookNextRepository(deps.DB)
			recsHandler.Fetcher = recsFetcher
			recsHandler.WatchTonightFetcher = recsFetcher
		}
		if detailSvc != nil {
			recsHandler.DetailSvc = detailSvc
		}
		recsHandler.CalendarRepo = calendarRepo
		recsHandler.EpisodeRepo = episodeRepo
		if ebookProgressStore != nil {
			recsHandler.EbookProgress = ebookProgressStore
		}
		if deps.PersonRepo != nil {
			recsHandler.CastFetcher = deps.PersonRepo
		}
		if deps.RecWorker != nil {
			recsHandler.RecWorker = deps.RecWorker
		}
	}

	// Build download handler.
	var downloadHandler *handlers.DownloadHandler
	var downloadSvc *downloads.Service
	if deps.DB != nil && deps.FileRepo != nil && deps.Config != nil {
		downloadRepo := downloads.NewRepository(deps.DB)
		downloadBandwidth := downloads.NewBandwidthManager(
			deps.Config.Download.ServerBandwidthBPS,
			deps.Config.Download.UserBandwidthBPS,
		)
		downloadBandwidth.SetSharedBudget(deps.DownloadBandwidthRedis, downloads.BandwidthRates(deps.DB))
		downloadLimiter := downloads.NewQuantityLimiter(
			downloadRepo,
			deps.Config.Download.MaxConcurrentPerUser,
			deps.Config.Download.MaxPerPeriod,
			deps.Config.Download.PeriodDuration,
		)
		downloadSvc = downloads.NewService(
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
		downloadSvc.SetGroupPolicyProvider(accessGroupStore)
		if playbackHandler != nil {
			downloadSvc.SetVirtualInputResolver(playbackHandler)
		}
		if deps.PolicySystem != nil {
			downloadSvc.SetActionDecider(deps.PolicySystem.PDP())
		}
		if detailSvc != nil {
			// Offline manifest + artwork/subtitle proxies (Phase 2). subtitleManager
			// may be nil when subtitles are unconfigured; pass a nil interface so the
			// downloaded-subtitle path reports unavailable instead of panicking.
			var subtitleSource downloads.SubtitleSource
			if subtitleManager != nil {
				subtitleSource = subtitleManager
			}
			downloadSvc.SetOfflineDeps(detailSvc, subtitleSource, nil)
			if subtitleRepo != nil {
				downloadSvc.SetExternalTimings(subtitleRepo)
			}
			if deps.Blobs.Assets != nil {
				downloadSvc.SetArtworkStore(deps.Blobs.Assets, deps.ArtworkSigner, deps.ArtworkRepair)
			}
		}
		if streamHandler != nil {
			downloadSvc.SetSubtitleCache(streamHandler.SubtitleCache)
		}
		if deps.MarkerPopulation != nil {
			downloadSvc.SetMarkerPopulation(deps.MarkerPopulation)
		}
		if deps.ArtifactManager != nil {
			// Prepare-to-file pipeline (Phase 3): remux/transcode-to-single-file.
			downloadSvc.SetArtifactManager(deps.ArtifactManager)
		}
		// Series monitoring (auto-download subscriptions). Client-pull only:
		// devices sync on app open / background refresh; there is no server
		// background worker.
		downloadSvc.SetSubscriptions(downloads.NewSubscriptionRepository(deps.DB))
		if deps.UserStoreProvider != nil {
			// delete_watched monitors skip episodes the profile has finished.
			downloadSvc.SetProgressStores(deps.UserStoreProvider)
		}
		if deps.OnDownloadService != nil {
			deps.OnDownloadService(downloadSvc)
		}
		downloadHandler = handlers.NewDownloadHandler(downloadSvc)
		if jwtService != nil {
			downloadHandler.SetDirectDownloadLinks(jwtService)
		}
		if deps.NodePlanner != nil {
			downloadHandler.SetProxyDelivery(deps.NodePlanner, func() string {
				cfg := deps.CurrentConfig()
				if cfg == nil {
					return ""
				}
				return cfg.Auth.JWTSecret
			})
		}
		if profileHandler != nil {
			// Profiles may live outside Postgres (sqlite userdb backend), so
			// deleting one cannot FK-cascade the shared user_devices table;
			// purge the device library (and its downloads) in-app instead.
			profileHandler.DeviceLibraryPurger = downloadRepo
		}
	} else {
		downloadHandler = handlers.NewDownloadHandler(nil)
	}

	var policyHandler *handlers.PolicyHandler
	if deps.PolicySystem != nil && deps.DB != nil {
		policyHandler = handlers.NewPolicyHandler(
			deps.PolicySystem,
			policy.NewPolicyStore(deps.DB),
			policy.NewDecisionRepository(deps.DB),
			func() bool {
				cfg := deps.CurrentConfig()
				return cfg != nil && cfg.Policy.EditorEnabled
			},
		)
	}

	var historyImportHandler *handlers.HistoryImportHandler
	var historyImportSvc *historyimport.Service
	if deps.DB != nil {
		historyRepo := historyimport.NewRepository(deps.DB, deps.SecretCipher)
		historyImportSvc = historyimport.NewService(deps.AppContext, historyRepo, deps.UserStoreProvider)
		// One policy for every media server address a user supplies.
		localNetworkAccess := historyimport.NewLocalNetworkAccess(settingsRepo, historyRepo)
		historyImportSvc.SetLocalNetworkAccess(localNetworkAccess)
		historyIdentity := watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo)
		historyImportSvc.SetStableIdentityResolver(historyIdentity)
		// Shows hidden from the source's Continue Watching are dropped through
		// the same tracker as Home dismissals, so interest recomputes.
		historyImportSvc.SetContinueWatchingStores(
			notifications.TrackDroppedSeries(catalog.NewDroppedSeriesRepo(deps.DB), deps.Notifications),
			catalog.NewNextUpRepository(deps.DB, deps.UserStoreProvider),
		)
		if deps.EventsHub != nil {
			historyImportSvc.AddObserver(evt.NewHistoryImportObserver(deps.EventsHub))
		}
		if deps.RecWorker != nil {
			historyImportSvc.SetSignalsChangedNotifier(deps.RecWorker)
		}
		historyImportSvc.StartBackgroundWork()
		historyImportHandler = handlers.NewHistoryImportHandler(historyImportSvc)
		if deps.UserStoreProvider != nil {
			webhookSyncSvc := webhooksync.NewService(webhooksync.NewRepository(deps.DB, deps.SecretCipher), historyRepo, deps.UserStoreProvider)
			webhookSyncSvc.SetLocalNetworkAccess(localNetworkAccess)
			webhookSyncSvc.SetStableIdentityResolver(historyIdentity)
			if deps.RecWorker != nil {
				webhookSyncSvc.SetSignalsChangedNotifier(deps.RecWorker)
			}
			webhookSyncHandler = handlers.NewWebhookSyncHandler(webhookSyncSvc, deps.UserStoreProvider)
		}
	}

	// ABS-compat routes are NOT mounted here — they live on a dedicated
	// http.Server (see absCompatSrv in cmd/silo/main.go) so the discovery
	// probes (/ping, /healthcheck, /status, etc.) don't collide with the
	// SPA fallback. Same pattern as the Jellyfin compat listener on 8096.

	// The native v2 API. The subtree is handed to the sealed apiv2 listener
	// with a single wildcard registration the route inventory records as a
	// delegation; every operation behind it is described by
	// contracts/api/v2/openapi.json, not by an inventory row. All v2
	// operations register at build regardless of the wiring here: a gate the
	// wiring lacks makes its operations fail closed, never disappear.
	// The user-scoped plugin settings handler is built here, before the v2
	// dependencies are sealed, so v2 shares the same instance the v1
	// /settings/plugins routes register below. Constructing it inside that
	// route group left v2 with a nil service and every plugin-settings
	// operation answering 503.
	if deps.PluginUserConfig != nil && deps.PluginService != nil {
		userPluginSettingsHandler = handlers.NewPluginHandler(
			plugins.NewRepositoryStore(deps.DB),
			plugins.NewInstallationStore(deps.DB),
			plugins.NewRuntimeConfigStore(deps.DB, deps.SecretCipher),
			deps.PluginService,
			deps.PluginUserConfig,
			deps.PluginHTTPProxy,
			metadata.NewChainRepository(deps.DB),
			deps.PluginImageResolver,
			restartStatus,
		)
	}
	// The OAuth handler is built whenever the database (oauth_sessions
	// storage), the auth service and the JWT service are available. Until
	// server.public_url is set (the stable redirect_uri origin for IdPs), a
	// v2 start sends the browser or app back with provider_unavailable and
	// the frozen v1 init answers 409; SetHostBaseURL follows config changes. It is built before the v2 listener so
	// completeOAuthLogin shares it with the v1 routes.
	var oauthHandler *auth.OAuthHandler
	if authHandler != nil {
		if deps.DB != nil && authService != nil && jwtService != nil {
			stateSecret := auth.DeriveOAuthStateSecret([]byte(deps.Config.Auth.JWTSecret))
			oauthStore := auth.NewPGOAuthStore(deps.DB, stateSecret)
			resolveClient := func(ctx context.Context, installationID int) (auth.OAuthClient, string, error) {
				pp := authService.FindOAuthInstallation(installationID)
				if pp == nil {
					return nil, "", auth.ErrUnknownAuthInstallation
				}
				c, err := pp.OAuthClient(ctx)
				if err != nil {
					return nil, "", err
				}
				return c, pp.CapabilityID(), nil
			}
			identity := serveridentity.New(catalog.NewServerSettingsRepo(deps.DB))
			oauthHandler = auth.NewOAuthHandler(auth.OAuthHandlerDeps{
				Store:           oauthStore,
				CompletionStore: oauthStore,
				LinkTickets:     oauthStore,
				StateSecret:     stateSecret,
				ResolveClient:   resolveClient,
				LoginCompleter:  authService,
				HostBaseURL:     deps.PublicURL,
				StateTTL:        10 * time.Minute,
				ServerID:        identity.ServerID,
				RevokeSession:   authService.Logout,
				Users:           userRepo,
				ProviderLogout:  authService.ProviderLogoutURL,
				KnownOrigins:    deps.overlayOrigins(),
			})
			if deps.OnConfigChange != nil {
				deps.OnConfigChange(func(_, updated *config.Config) {
					oauthHandler.SetHostBaseURL(updated.Server.PublicURL)
				})
			}
		}
	}

	// userRepo is a concrete pointer, so it has to stay out of the
	// interface parameter when unset — a typed nil would satisfy the
	// handler's nil check and panic on first use.
	var compatUsers handlers.CompatConnectUsers
	if userRepo != nil {
		compatUsers = userRepo
	}
	compatConnectInfoHandler := handlers.NewCompatConnectInfoHandler(
		deps.Config,
		settingsRepo,
		compatUsers,
	)
	if authHandler != nil {
		authHandler.SetOAuthRoutesAvailable(oauthHandler != nil)
	}
	if deps.Notifications != nil {
		// Tokenized email links are built against the public URL, and
		// notification artwork resolves through the detail service.
		deps.Notifications.SetPublicURL(deps.PublicURL)
		if detailSvc != nil {
			deps.Notifications.SetImageResolver(detailSvc)
		}
	}
	v2deps := v2Dependencies(deps, authMiddleware, viewerAccessMiddleware, actingAdmin, metadataCurationAccess, markerEditAccess, settingsRepo)
	v2deps.CompatConnectInfo = compatConnectInfoHandler
	// Server identity is public discovery data, so it reads through the raw
	// settings repo: a SECRET_KEY rotation must not change who the server is.
	if deps.DB != nil {
		v2deps.ServerIdentity = serveridentity.New(catalog.NewServerSettingsRepo(deps.DB))
	}
	v2deps.ServerConnections = apiv2.ServerConnections{
		PublicURL: func() string {
			if cfg := deps.CurrentConfig(); cfg != nil {
				return cfg.Server.PublicURL
			}
			return ""
		},
		LocalURL: func() string {
			if cfg := deps.CurrentConfig(); cfg != nil {
				return cfg.Server.LocalURL
			}
			return ""
		},
		LocalTLSPin: deps.LocalTLSPin,
	}
	if deps.NetworkAccess != nil {
		v2deps.ServerConnections.Providers = deps.NetworkAccess.Status
	}
	if deps.OpsLogRepo != nil {
		v2deps.AdminOperationalLogs = deps.OpsLogRepo
	}
	if deps.ActivityLogRepo != nil {
		v2deps.AdminAuditLogs = deps.ActivityLogRepo
	}
	if libraryHandler != nil {
		v2deps.ScanControls = libraryHandler
	}
	if deps.OpsLogRepo != nil && deps.ActivityLogRepo != nil && deps.LogStreamHub != nil && sessionRepo != nil && userRepo != nil {
		socket := handlers.NewAdminLogsSocketV2(handlers.NewAdminLogsHandler(deps.OpsLogRepo, deps.ActivityLogRepo, deps.LogStreamHub), evt.NewSocketTicketStore(deps.RedisClient, evt.SocketAudienceAdminLogs), sessionRepo, userRepo, viewerResolver, actingAdmin, deps.PublicURL)
		v2deps.AdminLogsSocket = socket
		if deps.OnConfigChange != nil {
			deps.OnConfigChange(func(_, updated *config.Config) { socket.SetPublicOrigin(updated.Server.PublicURL) })
		}
		if overlay := deps.overlayOrigins(); overlay != nil {
			socket.SetOverlayOrigins(overlay)
		}
	}
	if autoscanHandler != nil {
		v2deps.AutoscanDelivery = autoscanHandler
		v2deps.AdminAutoscanSources = autoscanHandler
		v2deps.AdminAutoscanConnections = autoscanHandler
		v2deps.AdminAutoscanConnectionTests = autoscanHandler
		v2deps.AdminAutoscanConnectionCreation = autoscanHandler
		v2deps.AdminAutoscanConnectionUpdate = autoscanHandler
		v2deps.AdminAutoscanConnectionDeletes = autoscanHandler
		v2deps.AdminAutoscanSourceDeletes = autoscanHandler
		v2deps.AdminAutoscanSourceWrites = autoscanHandler
		v2deps.AdminSourceWebhookLifecycle = autoscanHandler
		v2deps.AdminAutoscanScans = autoscanHandler
		v2deps.AdminAutoscanEvents = autoscanHandler
		v2deps.AdminAutoscanSettingsUpdates = autoscanHandler
		v2deps.AdminAutoscanAvailableSources = autoscanHandler
		v2deps.AdminAutoscanRewrites = autoscanHandler
		v2deps.AdminAutoscanInspection = autoscanHandler
	}
	if downloadSvc != nil {
		v2deps.Downloads = downloadSvc
		v2deps.DownloadProxyDelivery = downloadHandler.ProxyDeliveryAvailable
		v2deps.DownloadDelivery = downloadHandler
		v2deps.DownloadManifests = downloadSvc
		v2deps.DownloadSubscriptions = downloadSvc
		v2deps.DownloadSubscriptionMutations = downloadSvc
		v2deps.DownloadSubscriptionSync = downloadSvc
		v2deps.DownloadCreation = downloadSvc
		v2deps.AdminAccountDownloads = downloadSvc
		v2deps.AdminDownloadDevices = downloadSvc
		v2deps.DownloadPrepareAgain = downloadSvc
	}
	if ebookReaderHandler != nil {
		v2deps.EbookProgress = ebookReaderHandler
		v2deps.EbookConfig = ebookReaderHandler
		v2deps.EbookFiles = ebookReaderHandler
		v2deps.EbookAnnotations = ebookReaderHandler
	}
	if diagnosticsHandler != nil {
		v2deps.DiagnosticsIngress = diagnosticsHandler
		v2deps.DiagnosticsChunks = diagnosticsHandler
	}
	if passwordResetService != nil {
		passwordResetHandler := handlers.NewPasswordResetHandler(passwordResetService, userRepo)
		if accessGroupStore != nil {
			passwordResetHandler.SetAccessGroupProvider(accessGroupStore)
		}
		v2deps.PasswordResets = passwordResetHandler
	}
	var invitationHandler *handlers.InvitationHandler
	if invitationService != nil {
		invitationHandler = handlers.NewInvitationHandler(invitationService)
		if accessGroupStore != nil {
			invitationHandler.SetAccessGroupProvider(accessGroupStore)
		}
		v2deps.Invitations = invitationHandler
	}
	if deps.DB != nil && deps.Config != nil && viewerResolver != nil {
		v2deps.ThemeSongs = &handlers.ThemeSongsHandler{
			Service: themesongs.NewService(themesongs.NewRepository(deps.DB), deps.Config.Auth.JWTSecret), Sessions: sessionRepo, Users: userRepo, Resolver: viewerResolver,
			Router:     deps.themeRouter(playbackHandler),
			FFmpegPath: func() string { return deps.CurrentConfig().Playback.FFmpegPath },
		}
	}
	v2deps.ObserveThemeAudio = func(method string, handler http.Handler) http.Handler {
		return observeNative(deps.StreamTelemetry, method, "/api/v2/catalog/items/{id}/themes/{theme_id}/audio", handler.ServeHTTP)
	}
	v2deps.ObserveDownloadDelivery = func(method, pattern string, handler http.Handler) http.Handler {
		return observeNative(deps.StreamTelemetry, method, pattern, handler.ServeHTTP)
	}
	v2deps.ObserveBandwidthProbe = func(handler http.Handler) http.Handler {
		return observeNative(deps.StreamTelemetry, http.MethodGet, "/api/v2/playback/bandwidth-probe", handler.ServeHTTP)
	}

	if deps.BrandingService != nil {
		v2deps.Branding = deps.BrandingService
	}
	if deps.OverlayImages != nil {
		v2deps.OverlayImages = deps.OverlayImages
	}

	if inviteCodeRepo != nil {
		v2deps.AdminInviteCodes = inviteCodeRepo
	}

	if apiKeyRepo != nil {
		adminAPIKeys := handlers.NewAPIKeyHandler(apiKeyRepo)
		if userRepo != nil {
			adminAPIKeys.Owners = userRepo
		}
		v2deps.AdminAPIKeys = adminAPIKeys
		v2deps.PersonalAPIKeys = newPersonalAPIKeyHandler(apiKeyRepo, deps.UserStoreProvider)
	}
	if markersHandler != nil {
		v2deps.Markers = markersHandler
		v2deps.AdminMarkerHistory = markersHandler
		v2deps.AdminMarkerContributions = markersHandler
	}
	if adminMarkerProvidersHandler != nil {
		v2deps.AdminMarkerProviders = adminMarkerProvidersHandler
	}

	// The pilot operations call the v1 handlers' extracted business logic;
	// a typed nil must not become a non-nil interface, so each is set only
	// when the v1 handler exists.
	if authHandler != nil {
		v2deps.Accounts = authHandler
		v2deps.Devices = authHandler
		v2deps.Sessions = authHandler
		v2deps.PluginLaunch = authHandler
	}
	if oauthHandler != nil {
		v2deps.OAuth = oauthHandler
	}
	if adminSubtitleHandler != nil {
		v2deps.AdminSubtitleInspection = adminSubtitleHandler
		v2deps.AdminSubtitleProviderConfiguration = adminSubtitleHandler
		v2deps.AdminSubtitleList = adminSubtitleHandler
		v2deps.AdminSubtitleMetadata = adminSubtitleHandler
		v2deps.AdminSubtitleBytes = adminSubtitleHandler
		v2deps.AdminSubtitleDelete = adminSubtitleHandler
	}
	if playbackHandler != nil {
		v2deps.Playback = playbackHandler
		if sessionRepo != nil && userRepo != nil {
			socket := handlers.NewPlaybackControlSocketV2(playbackHandler, deps.RedisClient, sessionRepo, userRepo, viewerResolver, actingAdmin, deps.PublicURL)
			v2deps.PlaybackControlSocket = socket
			if deps.OnConfigChange != nil {
				deps.OnConfigChange(func(_, updated *config.Config) { socket.SetPublicOrigin(updated.Server.PublicURL) })
			}
			if overlay := deps.overlayOrigins(); overlay != nil {
				socket.SetOverlayOrigins(overlay)
			}
		}
		// Raw v2 delivery shares the byte-protocol handlers; fonts use the typed
		// service. Both retain token-carried reconstruction and deny markers.
		v2deps.PlaybackMedia = &apiv2.PlaybackMediaHandlers{
			Manifest: observeNative(deps.StreamTelemetry, http.MethodGet, "/api/v2/playback/transcode/{session_id}/master.m3u8", handlers.RefuseRelayRedirects(playbackHandler.HandleGetTranscodeManifest)),
			Segment:  observeNative(deps.StreamTelemetry, http.MethodGet, "/api/v2/playback/transcode/{session_id}/segment/{name}", handlers.RefuseRelayRedirects(playbackHandler.HandleGetTranscodeSegment)),
		}
		// Catalog detail reads warm the virtual versions they surface. The
		// handler refuses when virtual playback is not wired, so the seam is
		// safe on every deployment.
		v2deps.VirtualSelectionWarm = playbackHandler
		if streamHandler != nil {
			v2deps.PlaybackMedia.Original = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observeNative(deps.StreamTelemetry, r.Method, "/api/v2/stream/{session_id}", streamHandler.HandleStream)(w, r.WithContext(handlers.WithNativeAPIV2(r.Context())))
			})
			v2deps.PlaybackMedia.Subtitle = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observeNative(deps.StreamTelemetry, r.Method, "/api/v2/stream/{session_id}/subtitles/{track}", streamHandler.HandleSubtitle)(w, r)
			})
			v2deps.PlaybackMedia.SubtitleFonts = streamHandler
		}
	}
	if progressHandler != nil {
		v2deps.Progress = progressHandler
	}
	if deps.DB != nil && deps.UserStoreProvider != nil {
		snapshotResolver, _ := viewerResolver.(*policy.ViewerResolver)
		bootstrap := progresssync.NewService(deps.DB, deps.UserStoreProvider, settingsRepo, snapshotResolver)
		v2deps.ProgressBootstrap = bootstrap
		if deps.AppContext != nil {
			go bootstrap.RunCleanup(deps.AppContext, func(error) { slog.Warn("progress bootstrap cleanup unavailable", "component", "progresssync") })
		}
	}

	if personalDataHandler != nil {
		v2deps.History = personalDataHandler
	}
	if itemsHandler != nil {
		v2deps.Watch = itemsHandler
		if deps.TrickplayReader != nil {
			v2deps.Trickplay = deps.TrickplayReader
		}
	}
	if deps.TrickplayAdmin != nil {
		v2deps.AdminTrickplay = deps.TrickplayAdmin
	}
	if virtualRefreshJobs != nil {
		v2deps.VirtualCandidatesRefresh = virtualRefreshJobs
	}
	if virtualCandidatesRefresh != nil && settingsRepo != nil {
		v2deps.VirtualCandidatesDiscovery = virtualCandidatesRefresh
		v2deps.PreplayDiscoveryEnabled = func(ctx context.Context) bool {
			enabled, err := settingsRepo.Get(ctx, "virtual_library.discover_before_playback")
			if err != nil || enabled != "true" {
				return false
			}
			streaming, err := settingsRepo.Get(ctx, "virtual_library.enabled")
			return err == nil && streaming != "false"
		}
		// The catalog leaf detail read prices its wait on this setting; unset
		// or unreadable keeps apiv2's built-in default.
		v2deps.PreplayDiscoveryBudgetMs = func(ctx context.Context) int {
			raw, err := settingsRepo.Get(ctx, config.VirtualLibraryCatalogDiscoveryBudgetMsSettingKey)
			if err != nil {
				return 0
			}
			return config.VirtualLibraryCatalogDiscoveryBudgetMs(raw)
		}
	}
	if virtualIndexerReleases != nil {
		v2deps.VirtualReleaseRequest = virtualIndexerReleases
		v2deps.IndexerReleases = virtualIndexerReleases
	}
	if deps.VirtualLibraryService != nil {
		v2deps.VirtualLibraryStatus = deps.VirtualLibraryService
	}
	if virtualRefreshExecutor != nil && deps.OnVirtualRefreshExecutor != nil {
		deps.OnVirtualRefreshExecutor(virtualRefreshExecutor)
	}
	if profileHandler != nil {
		v2deps.Profiles = profileHandler
	}
	if deps.FolderRepo != nil {
		v2deps.Libraries = deps.FolderRepo
	} else if deps.DB != nil {
		v2deps.Libraries = catalog.NewFolderRepository(deps.DB)
	}
	if adminHandler != nil {
		v2deps.AdminUsers = adminHandler
		v2deps.AdminPlaybackHistory = adminHandler
		v2deps.AdminAccounts = adminHandler
		v2deps.AdminLoginSessions = adminHandler
		v2deps.AdminDevices = adminHandler
		if adminHandler.AdminDevicesAvailable() {
			v2deps.AdminAccountDevices = adminHandler
		}
		if deps.DB != nil {
			v2deps.AdminWatchSummary = adminHandler
		}
		v2deps.AdminPlaybackSessions = adminHandler
		if playbackHandler != nil && deps.DB != nil {
			playbackHandler.LifecycleViews = adminHandler
			v2deps.PlaybackLifecycleEvents = deps.EventsHub != nil
		}
		if deps.DB != nil && deps.ArtifactManager != nil {
			v2deps.AdminDownloadPreparations = downloads.NewPreparationReader(deps.DB, profileNamesByUser(deps.UserStoreProvider))
			v2deps.AdminDownloadPreparationControls = deps.ArtifactManager
			v2deps.AdminDownloadStorage = deps.ArtifactManager
		}
		if adminPlaybackControlHandler != nil {
			v2deps.AdminPlaybackCommands = adminPlaybackControlHandler
			v2deps.AdminPlaybackTerminate = adminPlaybackControlHandler
		}
		if deps.NodeRepo != nil {
			v2deps.AdminNodeSessions = &handlers.AdminNodeSessionsService{Redis: deps.RedisClient, Nodes: deps.NodeRepo}
		}
		v2deps.AdminSettingRead = adminHandler

		v2deps.AdminJellyfinCompatStatus = adminHandler
		v2deps.AdminJellyfinCompatSettings = adminHandler
		v2deps.AdminJellyfinCompatWeb = adminHandler
		v2deps.AdminSettingsInspection = adminHandler
		v2deps.AdminSettingsWrite = adminHandler
		v2deps.AdminSettingsChecks = adminHandler
		v2deps.AdminAIProviders = adminHandler
		adminHandler.ChatGPT = chatGPT
	}
	if chatGPT != nil {
		v2deps.ChatGPT = chatGPT
	}
	if watchTogetherHandler != nil {
		v2deps.WatchTogetherSuggestions = watchTogetherHandler
		v2deps.WatchTogetherSuggestionDelete = watchTogetherHandler
		v2deps.WatchTogetherSuggestionCreate = watchTogetherHandler
		v2deps.WatchTogetherSuggestionPromote = watchTogetherHandler
		v2deps.WatchTogetherClose = watchTogetherHandler
		v2deps.WatchTogetherRoomRead = watchTogetherHandler
		v2deps.WatchTogetherPolicy = watchTogetherHandler
		v2deps.WatchTogetherJoin = watchTogetherHandler
		v2deps.WatchTogetherSelection = watchTogetherHandler
		v2deps.WatchTogetherSourceFallback = watchTogetherHandler
		v2deps.WatchTogetherStage = watchTogetherHandler
		v2deps.WatchTogetherStart = watchTogetherHandler
		v2deps.WatchTogetherStop = watchTogetherHandler
		v2deps.WatchTogetherSelectionMode = watchTogetherHandler
		if watchTogetherHandler.MemberState != nil && watchTogetherHandler.MemberStateCatalog != nil {
			v2deps.WatchTogetherMemberState = watchTogetherHandler
		}
		if watchTogetherHandler.MemberState != nil && watchTogetherHandler.Details != nil {
			v2deps.WatchTogetherPicker = watchTogetherHandler
		}
		v2deps.WatchTogetherCapability = watchTogetherHandler
		v2deps.WatchTogetherCreate = watchTogetherHandler
		if deps.RedisClient != nil && sessionRepo != nil && userRepo != nil {
			socket := handlers.NewWatchTogetherSocketV2(watchTogetherHandler, watchtogether.NewRoomSocketCredentialStore(deps.RedisClient), sessionRepo, userRepo, viewerResolver, actingAdmin, deps.PublicURL)
			v2deps.WatchTogetherSocket = socket
			playbackHandler.WatchTogetherAvailable = true
			if deps.OnConfigChange != nil {
				deps.OnConfigChange(func(_, updated *config.Config) { socket.SetPublicOrigin(updated.Server.PublicURL) })
			}
			if overlay := deps.overlayOrigins(); overlay != nil {
				socket.SetOverlayOrigins(overlay)
			}
		}
	}
	if deps.EventsHub != nil {
		events := handlers.NewEventsHandler(deps.EventsHub, adminJobsHandler, adminHandler, deps.TaskManager, deps.ScanRegistry, deps.LibraryScanQueue, historyImportSvc)
		events.SetNotificationsSystem(deps.Notifications)
		v2deps.EventsCapability = events
		if sessionRepo != nil && userRepo != nil {
			var socketKeys handlers.SocketAPIKeys
			if apiKeyRepo != nil {
				socketKeys = apiKeyRepo
			}
			socket := handlers.NewEventsSocketV2(events, evt.NewSocketTicketStore(deps.RedisClient, evt.SocketAudienceEvents), sessionRepo, userRepo, viewerResolver, actingAdmin, socketKeys, deps.PublicURL)
			v2deps.EventsSocket = socket
			if deps.OnConfigChange != nil {
				deps.OnConfigChange(func(_, updated *config.Config) { socket.SetPublicOrigin(updated.Server.PublicURL) })
			}
			if overlay := deps.overlayOrigins(); overlay != nil {
				socket.SetOverlayOrigins(overlay)
			}
		}
	}
	if deps.Notifications != nil {
		inbox := handlers.NewNotificationsHandler(deps.Notifications, deps.EventsHub)
		inbox.SetApplePushDisplayTokenIssuer(jwtService)
		v2deps.NotificationInbox = inbox
		v2deps.NotificationDestinations = inbox
		v2deps.NotificationDestinationTests = inbox
		v2deps.NotificationDestinationCreate = inbox
		v2deps.OrderedAndroidPush = inbox
		v2deps.AndroidPushRetirement = deps.Notifications.PushDevices
		v2deps.OrderedApplePush = handlers.NewOrderedApplePushV2(deps.Notifications.PushDevices, jwtService, sessionRepo, userRepo, viewerResolver, actingAdmin)
		v2deps.ApplePushRetirement = deps.Notifications.PushDevices
		v2deps.AdminNotificationPush = deps.Notifications
		v2deps.AdminNotificationDiscord = deps.Notifications
		v2deps.NotificationChannels = deps.Notifications
		v2deps.NotificationDiscordManagement = newDiscordNotificationsHandler(deps)
		v2deps.NotificationEmailVerification = deps.Notifications.EmailVerification
		v2deps.NotificationEmailLinks = handlers.NewEmailLinkHandler(deps.Notifications)
		linkHandler := handlers.NewDiscordLinkHandler(deps.Notifications, deps.Notifications.Settings, deps.PublicURL)
		v2deps.NotificationDiscordLinks = linkHandler
		if deps.OnConfigChange != nil {
			deps.OnConfigChange(func(_, updated *config.Config) { linkHandler.SetPublicURL(updated.Server.PublicURL) })
		}
		v2deps.NotificationRelay = handlers.NewAdminApplePushHandler(deps.Notifications, settingsRepo)
	}
	v2deps.AdminAccessGroups = accessGroupHandler
	if deps.ActivityLogRepo != nil {
		v2deps.AdminAccountActivity = deps.ActivityLogRepo
	}
	if settingValuesHandler != nil {
		v2deps.SettingsContract = settingValuesHandler
		v2deps.SettingValues = settingValuesHandler
		v2deps.AdminAccountSettings = settingValuesHandler
	}
	if settingsHandler != nil {
		v2deps.Settings = settingsHandler
	}
	if userPluginSettingsHandler != nil {
		v2deps.PluginSettings = userPluginSettingsHandler
	}
	if deviceHandler != nil {
		v2deps.DeviceSettings = deviceHandler
	}
	if audioPrefHandler != nil {
		v2deps.AudioPreferences = audioPrefHandler
	}
	if libraryPlaybackPrefHandler != nil {
		v2deps.LibraryPlaybackPreferences = libraryPlaybackPrefHandler
	}
	if subtitlePrefHandler != nil {
		v2deps.SubtitlePreferences = subtitlePrefHandler
	}
	v2deps.LibraryMonitoring = deps.LibraryMonitoring
	if libraryHandler != nil {
		v2deps.LibraryAdmin = libraryHandler
		v2deps.UserLibraries = libraryHandler
		if deps.DB != nil {
			v2deps.LibraryJobs = adminjob.NewRepository(deps.DB)
		}
	}
	if adminJobsHandler != nil {
		v2deps.AdminTaskJobs = adminJobsHandler
		// Both sides share one signer so they cannot disagree.
		if signer := newAdminJobArtifactSigner(&deps); signer != nil {
			adminJobsHandler.ArtifactSigner = signer
			v2deps.AdminJobArtifacts = adminJobsHandler
			v2deps.AdminJobArtifactSigner = signer
		}
	}
	if deps.StorageTransition != nil {
		v2deps.AdminStorageTransition = deps.StorageTransition
	}
	if deps.ProfileTransfer != nil {
		deps.ProfileTransfer.SetItemsChanged(importedChanges(deps.Notifications, deps.RecWorker))
		v2deps.ProfileTransfer = deps.ProfileTransfer
	}
	if deps.Backups != nil {
		v2deps.AdminBackups = deps.Backups
		// Presigning destinations hand out storage URLs; the signer covers the
		// ones that cannot, and expires links on the same schedule.
		if deps.Config != nil {
			v2deps.AdminBackupSigner = artworkurl.NewBackupObjectSigner(deps.CurrentConfig().Auth.JWTSecret, adminJobArtifactURLTTL)
		}
	}
	if catalogSeedHandler != nil {
		v2deps.AdminCatalogSources = catalogSeedHandler
		v2deps.AdminCatalogTransfer = catalogSeedHandler
	}
	systemJWTSecret := ""
	if deps.Config != nil {
		systemJWTSecret = deps.Config.Auth.JWTSecret
	}
	v2deps.AdminHardwareAcceleration = handlers.NewSystemHandler(deps.TranscodePool, systemJWTSecret, func() (string, string, string) {
		cfg := deps.CurrentConfig()
		if cfg == nil {
			return "", "", ""
		}
		return cfg.Playback.FFmpegPath, cfg.Playback.HWAccel, cfg.Playback.HWDevice
	})
	v2deps.AdminTelemetryParity = &handlers.StreamTelemetryParityHandler{Registry: deps.StreamTelemetry, ViewCache: deps.StreamTelemetryViewCache, Pool: deps.DB, Redis: deps.RedisClient}
	v2deps.AdminFilesystem = handlers.NewFilesystemHandler()
	if deps.APIUsage != nil {
		v2deps.AdminAPIClients = deps.APIUsage
	}
	if adminHandler != nil {
		v2deps.AdminDashboardInsights = adminHandler
		v2deps.AdminDashboardStats = adminHandler
		v2deps.AdminDashboardLayout = adminHandler
		v2deps.AdminDashboardLayoutResets = adminHandler
		v2deps.AdminDashboardLayoutSaves = adminHandler
		v2deps.AdminServerStatus = adminHandler
	}
	v2deps.AdminServerRestart = serverControlHandler
	var nodeHandler *handlers.NodeHandler
	if deps.NodeRepo != nil {
		jwtSecret := ""
		if deps.Config != nil {
			jwtSecret = deps.Config.Auth.JWTSecret
		}
		nodeHandler = handlers.NewNodeHandler(deps.NodeRepo, deps.ProxyPool, deps.TranscodePool, deps.NodeRepo, deps.EventBus, deps.RedisClient, jwtSecret)
		// A re-probe stores the node's new inventory through the sweep's own
		// refresh, so the drift and persist rules have one implementation.
		// Without a health checker the node still re-probes and the row
		// catches up on a later sweep.
		if deps.NodeHealthChecker != nil {
			nodeHandler.SetCapabilityRefresher(deps.NodeHealthChecker)
		}
		// An acceleration override change makes this server's cached view of
		// the node wrong the moment it lands; the same invalidation the health
		// sweep uses drops it.
		nodeHandler.SetCapabilityInvalidator(deps.invalidateNodeCapabilities(playbackHandler))
		// A node with no override of its own runs the cluster's acceleration
		// policy, and how many devices that names is what decides how long
		// its re-probe may take.
		nodeHandler.SetClusterPlaybackPolicy(playbackHandler.PlaybackConfig)
		v2deps.AdminNodesRead = nodeHandler
		v2deps.AdminNodeCommands = nodeHandler
		v2deps.AdminNodeDrain = nodeHandler
		v2deps.AdminNodeReload = nodeHandler
		// Network access admin operations fan out to every enabled proxy node
		// over its bearer routes, the same way force-reload does.
		if deps.PluginService != nil {
			deps.PluginService.SetNetworkAccessNodes(nodeHandler)
		}
		if deps.DB != nil {
			nodeHandler.SetConfigurationStore(nodepool.NewAdminConfigurationStore(deps.DB))
			v2deps.AdminNodeConfiguration = nodeHandler
		}
	}
	var rateLimitHandler *handlers.RateLimitHandler
	if settingsRepo != nil {
		rateLimitHandler = handlers.NewRateLimitHandler(settingsRepo, deps.RateLimitMW, deps.EventBus, restartStatus, deps.RedisBootstrapAvailable)
		v2deps.AdminRateLimits = rateLimitHandler
		v2deps.AdminRateLimitsWrite = rateLimitHandler
	}
	var emailHandler *handlers.EmailHandler
	if settingsRepo != nil {
		emailHandler = handlers.NewEmailHandler(mail.NewSMTPSender(settingsRepo), deps.EmailBrand)
		v2deps.AdminEmailTests = emailHandler
	}
	v2deps.AdminResourceSampler = deps.ResourceSampler
	v2deps.AdminCatalogSearch = adminHandler
	v2deps.AdminItemMetadata = adminHandler
	if adminHandler != nil {
		v2deps.AdminUnmatchedFiles = adminHandler
	}
	if itemRepo != nil {
		v2deps.AdminCatalogItems = itemRepo
	}
	if adminImageHandler != nil {
		v2deps.AdminCatalogImages = adminImageHandler
	}
	v2deps.AdminCatalogSplit = adminSplitHandler
	if adminMatchHandler != nil {
		v2deps.AdminCatalogMatch = adminMatchHandler
	}
	if adminIntroHandler != nil {
		v2deps.AdminEpisodeMarkers = adminIntroHandler
	}
	if deps.RecWorker != nil {
		v2deps.AdminRecommendations = deps.RecWorker
	}
	if diagnosticsHandler != nil {
		v2deps.AdminDiagnosticDownloads = diagnosticsHandler
		v2deps.AdminDiagnosticReads = diagnosticsHandler
		v2deps.AdminDiagnosticDeletes = diagnosticsHandler
	}
	if deps.DB != nil {
		v2deps.AdminPluginCatalogSettings = plugins.NewRepositoryStore(deps.DB)
		v2deps.AdminPluginRepositories = plugins.NewRepositoryStore(deps.DB)
		v2deps.AdminPluginRepositoryCreation = plugins.NewRepositoryStore(deps.DB)
		v2deps.AdminPluginRepositoryUpdates = plugins.NewRepositoryStore(deps.DB)
		v2deps.AdminPluginRepositoryDeletes = plugins.NewRepositoryStore(deps.DB)
	}
	var externalSignInPlugins *handlers.PluginHandler
	if deps.DB != nil && deps.PluginService != nil && deps.PluginUserConfig != nil {
		v2PluginHandler := handlers.NewPluginHandler(
			plugins.NewRepositoryStore(deps.DB),
			plugins.NewInstallationStore(deps.DB),
			plugins.NewRuntimeConfigStore(deps.DB, deps.SecretCipher),
			deps.PluginService,
			deps.PluginUserConfig,
			deps.PluginHTTPProxy,
			metadata.NewChainRepository(deps.DB),
			deps.PluginImageResolver,
			restartStatus,
		)
		v2PluginHandler.SetAuthProvidersChanged(deps.OnAuthProvidersChanged)
		v2deps.AdminPluginInventory = v2PluginHandler
		v2deps.AdminPluginConfiguration = v2PluginHandler
		v2deps.AdminPluginLifecycle = v2PluginHandler
		v2deps.AdminPluginUploads = v2PluginHandler
		externalSignInPlugins = v2PluginHandler
	}
	if deps.Backups != nil && deps.DB != nil {
		wireBackupRestore(deps, adminHandler, libraryHandler, adminImageHandler, externalSignInPlugins, libraryCollectionHandler, sectionHandler, rateLimitHandler)
	}
	if deps.DB != nil && authService != nil && userRepo != nil {
		externalSignIn := handlers.NewExternalSignInHandler(auth.NewIdentityService(deps.DB), authService, userRepo, externalSignInPlugins)
		externalSignIn.Stores = deps.UserStoreProvider
		v2deps.ExternalSignIn = externalSignIn
	}
	if deps.PluginService != nil {
		v2deps.NetworkAccess = deps.PluginService
	}
	if deps.TaskManager != nil && deps.DB != nil {
		v2deps.AdminTasks = deps.TaskManager
		v2deps.AdminTaskMetrics = metadata.NewRefreshDebtRepository(deps.DB)
		v2deps.AdminTaskHistory = repository.NewPgExecutionRepository(deps.DB)
	}
	if policyHandler != nil {
		v2deps.AdminPolicy = policyHandler
		v2deps.PolicyCapability = policyHandler
	}
	if sectionHandler != nil {
		v2deps.LibrarySections = sectionHandler
		v2deps.AdminSections = sectionHandler
	}
	if libraryCollectionHandler != nil {
		v2deps.LibraryCollections = libraryCollectionHandler
		v2deps.AdminCollections = libraryCollectionHandler
		v2deps.AdminVirtualItems = libraryCollectionHandler
	}
	if libraryCollectionGroupHandler != nil {
		v2deps.AdminCollectionGroups = libraryCollectionGroupHandler
	}
	if itemsHandler != nil {
		v2deps.CatalogAccess = itemsHandler
	}
	if catalogHandler != nil {
		v2deps.CatalogBrowse = catalogHandler
		v2deps.BoxSets = catalogHandler
	}
	if catalogResourceHandler != nil {
		v2deps.CatalogItems = catalogResourceHandler
		v2deps.CatalogVersionsCheck = catalogResourceHandler
		v2deps.CatalogVersionsWarm = catalogResourceHandler
	}
	if shuffleService != nil {
		v2deps.Shuffles = shuffleService
	}
	if itemsHandler != nil {
		v2deps.CatalogTrailers = itemsHandler
	}
	if metadataAIHandler != nil {
		v2deps.MetadataAI = metadataAIHandler
		v2deps.AdminMetadataTranslation = metadataAIHandler
	}
	if peopleHandler != nil {
		v2deps.People = peopleHandler
		v2deps.AdminPeople = peopleHandler
	}
	if literaryWorkHandler != nil {
		v2deps.LiteraryWorks = literaryWorkHandler
		v2deps.AdminLiteraryWorks = literaryWorkHandler.Service
	}
	if calendarRepo != nil {
		calendarPopular := recommendations.NewRepo(deps.DB)
		calendarTrending := sections.NewTrendingSnapshotRepository(deps.DB)
		calendarHandler = handlers.NewCalendarHandler(calendarRepo, detailSvc, calendarPopular, calendarTrending)
		v2deps.Calendar = calendarHandler
	}
	if homeDismissalHandler != nil {
		v2deps.HomeDismissals = homeDismissalHandler
	}
	if sectionHandler != nil {
		v2deps.HomeSections = sectionHandler
	}
	v2deps.Recipes = &handlers.RecipeHandler{}
	if personalDataHandler != nil && itemsHandler != nil {
		v2deps.PersonalLists = personalDataHandler
		v2deps.NotInterested = personalDataHandler
		// Allowances live in the shared tables, so they are kept only where
		// the user store is PostgreSQL in the same database.
		if viewerResolver != nil && deps.DB != nil {
			if shared, ok := deps.UserStoreProvider.(interface{ StoresAccountsIn(*pgxpool.Pool) bool }); ok && shared.StoresAccountsIn(deps.DB) {
				v2deps.TitleAllowances = handlers.NewTitleAllowanceHandler(personalDataHandler, viewerResolver, pgstore.NewTitleAllowances(deps.DB)).
					WithSmartCollections(&catalog.QueryExecutor{Pool: deps.DB}).
					WithCollectionOwners(collectionOwners)
			}
		}
	}
	if ratingsHandler != nil {
		v2deps.Ratings = ratingsHandler
	}
	if recsHandler != nil {
		v2deps.Recommendations = recsHandler
	}
	if sectionHandler != nil {
		v2deps.ProfileSections = sectionHandler
		v2deps.AdminProfileSections = sectionHandler
	}
	if webhookSyncHandler != nil {
		v2deps.WebhookSync = webhookSyncHandler
		v2deps.WebhookSyncManagement = webhookSyncHandler
		v2deps.WebhookReceiver = webhookSyncHandler
	}
	if onboardingHandler != nil {
		v2deps.Onboarding = onboardingHandler
	}
	if historyImportHandler != nil {
		v2deps.HistoryImports = historyImportHandler
		v2deps.AdminHistoryImports = historyImportHandler.Service()
	}
	v2deps.WatchProviders = deps.WatchProviderService
	if requestHandler != nil {
		v2deps.Requests = requestHandler.Service()
		v2deps.RequestLifecycle = requestHandler.Service()
		v2deps.AdminRequests = requestHandler.Service()
		if watchlistRequests, ok := requestHandler.Service().(apiv2.WatchlistRequestService); ok && personalDataHandler != nil && watchlistTitles != nil {
			v2deps.WatchlistTitles = personalDataHandler
			v2deps.WatchlistRequests = watchlistRequests
		}
		// *requests.Service implements the usage read (pinned at the top of
		// this file), so the assertion only fails for a test double.
		if usage, ok := requestHandler.Service().(apiv2.AdminRequestUsageService); ok {
			v2deps.AdminRequestUsage = usage
		}
	}
	if collectionHandler != nil {
		v2deps.PersonalCollections = collectionHandler
	}
	if userImportHandler != nil {
		v2deps.CollectionImports = userImportHandler
	}
	if downloadHandler != nil {
		direct := func(path string, handler http.HandlerFunc) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observeNative(deps.StreamTelemetry, r.Method, path, handler)(w, r)
			})
		}
		v2deps.DirectDownloads = &apiv2.DirectDownloadHandlers{
			Original: direct("/api/v2/direct-download", downloadHandler.HandleDirectDownloadV2),
			Proxy:    direct("/api/v2/direct-download-proxy", downloadHandler.HandleDirectDownloadViaProxyV2),
		}
		if downloadSvc != nil && jwtService != nil {
			v2deps.DirectDownloadLinks = downloadHandler
		}
	}

	if subtitleSearchHandler != nil {
		v2deps.SubtitleProviders = subtitleSearchHandler
		v2deps.SubtitleReads = subtitleSearchHandler
		v2deps.ViewerSubtitleDelete = subtitleSearchHandler
		v2deps.SubtitleDownloads = subtitleSearchHandler
		v2deps.SubtitleUploads = subtitleSearchHandler
		v2deps.SubtitleSync = subtitleSearchHandler
	}
	if subtitleAIHandler != nil {
		v2deps.SubtitleAIReads = subtitleAIHandler
		v2deps.SubtitleAI = subtitleAIHandler

		v2deps.SubtitleAICancel = subtitleAIHandler
		v2deps.SubtitleAICreate = subtitleAIHandler
	}
	if deps.PluginHTTPProxy != nil {
		v2deps.AuthProviderIconPublic = deps.PluginHTTPProxy.PublicGETRoute
	}
	pluginRouteAccess := newPluginAccessResolver(jwtService, sessionRepo, authService, apiKeyRepo, userRepo, viewerResolver, actingAdmin)
	// Assets keep their JWT-only access: API keys never reached them. Their
	// callers keep only authenticated and admin, so they never renew the
	// launch cookie.
	pluginAssetAccess := newPluginAccessResolver(jwtService, sessionRepo, nil, nil, nil, viewerResolver, actingAdmin)
	pluginAssetAccess.noRenewal = true
	v2deps.PluginContent = plugins.NewContentHandler(deps.PluginHTTPProxy, func(r *http.Request) plugins.ContentAccess {
		resolved := pluginRouteAccess.resolve(r)
		return plugins.ContentAccess{
			Authenticated: resolved.authenticated, Admin: resolved.admin, UserID: resolved.userID, ProfileID: resolved.profileID,
			RenewedCookie: renewedPluginCookie(r, resolved, plugins.ContentPrefix),
		}
	}, func(r *http.Request) plugins.ContentAccess {
		resolved := pluginAssetAccess.resolve(r)
		return plugins.ContentAccess{Authenticated: resolved.authenticated, Admin: resolved.admin}
	})
	if deps.BrandingService != nil {
		v2deps.AdminBrandingAssets = handlers.NewBrandingHandler(deps.BrandingService)
	}
	if deps.v2Wiring != nil {
		deps.v2Wiring(v2deps)
	}
	v2deps.ObserveRoutes = deps.v2RouteSnapshot
	r.Handle("/api/v2/*", apiv2.NewHandler(v2deps))

	// The retired /api/v1 namespace: the operational probes stay at their
	// paths, and everything else answers the retirement tombstone.
	r.Get("/api/v1/health", healthHandler.ServeHTTP)
	r.Get("/api/v1/ready", readyHandler.ServeHTTP)
	// Vio keeps three fork-only operations on their old paths until they have
	// /api/v2 operations of their own: deleting a catalog item, purging the
	// zero-storage items of a collection, and listing a plugin's dynamic config
	// options. They are registered before the tombstone so the specific routes
	// win. See docs/architecture/fork-divergence.md (v1 carve-out).
	if itemsHandler != nil {
		r.Delete("/api/v1/admin/items/{id}", itemsHandler.HandleDelete)
	}
	if libraryCollectionHandler != nil {
		r.Post("/api/v1/admin/collections/purge-virtual", libraryCollectionHandler.PurgeVirtualPlaybackItems)
	}
	if externalSignInPlugins != nil {
		r.Post("/api/v1/admin/plugins/installations/{id}/config-options", externalSignInPlugins.HandleListInstallationConfigOptions)
	}
	r.Handle("/api/v1", v1Tombstone)
	r.Handle("/api/v1/*", v1Tombstone)
	// Integrations keep calling the retired URLs that carry a credential in
	// the path. Naming the segment lets the request and activity logs redact
	// it (activitylog.RedactSecretPathParams), as they did for the live routes.
	r.Handle("/api/v1/autoscan/webhooks/{token}", v1Tombstone)
	r.Handle("/api/v1/plex-sync/webhooks/{secret}", v1Tombstone)
	r.Handle("/api/v1/webhook-sync/webhooks/{secret}", v1Tombstone)
	r.Handle("/api/v1/invitations/{token}", v1Tombstone)
	r.Handle("/api/v1/invitations/{token}/*", v1Tombstone)

	if nodeHandler != nil {
		nodeHandler.StartConfigurationReconciliation(deps.AppContext)
	}
	// The sweep ends sessions through the handler's fields, so it starts
	// once every route above has finished wiring them.
	playbackHandler.StartLifecycleSweep(deps.AppContext, deps.DB)
	return r
}

// overlayOrigins returns the source of connected overlay origins the
// WebSocket handshakes accept, or nil when network access is not wired.
func (d Dependencies) overlayOrigins() handlers.OverlayOriginSource {
	if d.NetworkAccess == nil || d.NetworkAccess.Status == nil {
		return nil
	}
	return d.NetworkAccess.Status.ConnectedOrigins
}

// useBaseMiddleware mounts the middleware chain every native request passes
// through, in order. It is factored out of NewRouter so a test can drive the
// real chain over a real socket: re-declaring the stack in a test would let the
// two drift, and a drifted copy is exactly how a broken writer chain passes its
// own tests (see the §4.4 conformance requirement in the stream-telemetry design).
func useBaseMiddleware(r chi.Router, deps Dependencies) {
	// No response is used as a type it does not declare (see httpstream.NoSniff).
	r.Use(httpstream.NoSniff)

	// Server-generated request ID; never adopts a client-supplied one.
	r.Use(apimw.RequestID)

	// Client IP resolution must run before request logging.
	if deps.ClientIPResolver != nil {
		r.Use(clientip.Middleware(deps.ClientIPResolver))
	}

	// Ingress token from network access provider plugins: validated and
	// stripped before anything can log or forward it; an unknown token is
	// refused outright. Requests without it stay on the default access path.
	if deps.NetworkAccess != nil {
		r.Use(netaccess.Middleware(deps.NetworkAccess.Registry))
	}

	r.Use(apimw.RequestLogger(deps.NodeID))
	r.Use(middleware.Recoverer)
	r.Use(apimw.Metrics)

	// Compress text-like responses (JSON, SVG, …), while leaving exact bulk
	// media routes unwrapped so their io.ReaderFrom/sendfile path survives,
	// and serving validator-bearing v2 responses identity-encoded so a strong
	// ETag names exactly one representation (apiv2.IdentityEncoded).
	r.Use(httpstream.CompressWithExclusions(skipNativeMediaCompression, apiv2.IdentityEncoded))

	// Activity logging (before auth — captures all requests including failed auth).
	if deps.ActivityLogWriter != nil {
		r.Use(activitylog.NewMiddleware(deps.ActivityLogWriter, deps.NodeID, deps.LogStreamHub))
	}
}

func skipNativeMediaCompression(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(p) < 3 || p[0] != "api" || p[1] != "v2" {
		return false
	}
	switch {
	case len(p) == 8 && p[2] == "catalog" && p[3] == "items" && p[4] != "" && p[5] == "themes" && p[6] != "" && p[7] == "audio":
		return true
	case len(p) == 4 && p[2] == "stream" && p[3] != "":
		return true
	case len(p) == 7 && p[2] == "playback" && p[3] == "transcode" && p[4] != "" && p[5] == "segment" && p[6] != "":
		return true
	case len(p) == 5 && p[2] == "downloads" && p[3] != "" && (p[4] == "file" || p[4] == "file-proxy"):
		return true
	case len(p) == 3 && (p[2] == "direct-download" || p[2] == "direct-download-proxy"):
		return true
	case len(p) == 7 && p[2] == "ebooks" && p[3] != "" && p[4] == "files" && p[5] != "" && p[6] == "read":
		return true
	default:
		return false
	}
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
			COALESCE(e.episode_number, 0),
			COALESCE(mf.multi_episode_end, 0)
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
		&meta.EpisodeEnd,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &meta, nil
}

// NewTMDBCollectionFetcher adapts client to a TMDBCollectionFetcher.
// Exported so main.go can construct it for the collection sync scheduler.
func NewTMDBCollectionFetcher(client *tmdb.Client) catalog.TMDBCollectionFetcher {
	return &tmdbCollectionAdapter{client: client}
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
	return tmdbCollectionEntries(results), nil
}

func (a *tmdbCollectionAdapter) EnrichExternalIDs(ctx context.Context, entries []catalog.TMDBCollectionEntry) error {
	return enrichTMDBExternalIDs(ctx, a.client, entries)
}

func (a *tmdbCollectionAdapter) FindTMDBIDs(ctx context.Context, itemType string, titles []catalog.TitleWithoutTMDBID) ([]int, error) {
	return findTMDBIDs(ctx, a.client, itemType, titles)
}

// tmdbFranchiseAdapter adapts tmdb.Client to catalog.TMDBCollectionByIDFetcher
// for the `tmdb_collection` sync mode.
type tmdbFranchiseAdapter struct {
	client *tmdb.Client
}

func (a *tmdbFranchiseAdapter) GetCollection(ctx context.Context, id int) ([]catalog.TMDBCollectionEntry, error) {
	collection, err := a.client.GetCollection(ctx, id, "")
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
		entries[i] = catalog.TMDBCollectionEntry{
			ID:          p.ID,
			MediaType:   mediaType,
			Title:       p.Title,
			ReleaseDate: p.ReleaseDate,
		}
	}
	return entries, nil
}

func (a *tmdbFranchiseAdapter) EnrichExternalIDs(ctx context.Context, entries []catalog.TMDBCollectionEntry) error {
	return enrichTMDBExternalIDs(ctx, a.client, entries)
}

func (a *tmdbFranchiseAdapter) FindTMDBIDs(ctx context.Context, itemType string, titles []catalog.TitleWithoutTMDBID) ([]int, error) {
	return findTMDBIDs(ctx, a.client, itemType, titles)
}

// TMDBDiscoverAdapter adapts tmdb.Client to catalog.TMDBDiscoverFetcher for
// the `tmdb_discover` sync mode and catalog.TMDBDigitalReleaseChecker for
// theatrical release gating. Like the preset adapter, it enriches each
// result with external IDs so the catalog matcher can fall back to IMDb/TVDB
// when a local item lacks a TMDB ID.
type TMDBDiscoverAdapter struct {
	client *tmdb.Client
}

type tmdbDiscoverAdapter = TMDBDiscoverAdapter

// NewTMDBDiscoverAdapter adapts client to catalog.TMDBDiscoverFetcher and
// catalog.TMDBDigitalReleaseChecker. Callers pass the process-wide TMDB client
// so discover syncs and release gating share its caches and rate budget.
// Exported so main.go can wire it before background tasks start.
func NewTMDBDiscoverAdapter(client *tmdb.Client) *TMDBDiscoverAdapter {
	return &TMDBDiscoverAdapter{
		client: client,
	}
}

// HasDigitalRelease implements catalog.TMDBDigitalReleaseChecker: a movie is
// digitally released once TMDB records any Digital, Physical, or TV release
// date in the past. Titles with no release-date data fail open (released).
func (a *TMDBDiscoverAdapter) EpisodeReleaseDates(ctx context.Context, seriesID, season int) (map[int]time.Time, error) {
	return a.client.EpisodeReleaseDates(ctx, seriesID, season)
}

func (a *TMDBDiscoverAdapter) HasDigitalRelease(ctx context.Context, tmdbID int) (bool, error) {
	return a.client.HasDigitalRelease(ctx, tmdbID)
}

func (a *TMDBDiscoverAdapter) Discover(ctx context.Context, mediaType string, params catalog.TMDBDiscoverParams, limit int) ([]catalog.TMDBCollectionEntry, error) {
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
	return tmdbCollectionEntries(results), nil
}

func (a *TMDBDiscoverAdapter) EnrichExternalIDs(ctx context.Context, entries []catalog.TMDBCollectionEntry) error {
	return enrichTMDBExternalIDs(ctx, a.client, entries)
}

func (a *TMDBDiscoverAdapter) FindTMDBIDs(ctx context.Context, itemType string, titles []catalog.TitleWithoutTMDBID) ([]int, error) {
	return findTMDBIDs(ctx, a.client, itemType, titles)
}

// tmdbListAdapter adapts tmdb.Client to catalog.TMDBListFetcher for the
// `tmdb_list` sync mode.
type tmdbListAdapter struct {
	client *tmdb.Client
}

func (a *tmdbListAdapter) GetList(ctx context.Context, id, limit int) ([]catalog.TMDBCollectionEntry, error) {
	results, err := a.client.GetList(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	return tmdbCollectionEntries(results), nil
}

func (a *tmdbListAdapter) EnrichExternalIDs(ctx context.Context, entries []catalog.TMDBCollectionEntry) error {
	return enrichTMDBExternalIDs(ctx, a.client, entries)
}

func (a *tmdbListAdapter) FindTMDBIDs(ctx context.Context, itemType string, titles []catalog.TitleWithoutTMDBID) ([]int, error) {
	return findTMDBIDs(ctx, a.client, itemType, titles)
}

func tmdbCollectionEntries(results []tmdb.CollectionResult) []catalog.TMDBCollectionEntry {
	entries := make([]catalog.TMDBCollectionEntry, len(results))
	for i, r := range results {
		entries[i] = catalog.TMDBCollectionEntry{ID: r.ID, MediaType: r.MediaType, Title: r.Title, ReleaseDate: r.ReleaseDate}
	}
	return entries
}

// tmdbExternalIDLookups bounds the concurrent external-ID lookups of one sync.
// The client's shared rate limit sets the pace; concurrency only keeps the
// round trips from adding up, since an import runs its first sync inside the
// request.
const tmdbExternalIDLookups = 8

// enrichTMDBExternalIDs fills in the IMDb and TVDB IDs of entries, which lets
// the matcher find a local title that lacks a TMDB ID. The matcher asks only
// for the entries their TMDB ID left unmatched.
func enrichTMDBExternalIDs(ctx context.Context, client *tmdb.Client, entries []catalog.TMDBCollectionEntry) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(tmdbExternalIDLookups)
	for i := range entries {
		g.Go(func() error {
			// A failed lookup leaves the entry matchable by TMDB ID alone; only
			// cancellation ends the sync.
			if externalIDs, err := client.GetExternalIDs(gctx, entries[i].MediaType, entries[i].ID); err == nil && externalIDs != nil {
				entries[i].IMDbID = externalIDs.IMDbID
				entries[i].TVDBID = externalIDs.TVDBID
			}
			return gctx.Err()
		})
	}
	return g.Wait()
}

// findTMDBIDs looks up the TMDB ID of each catalog title known only by IMDb or
// TVDB ID, trying the provider that identifies itemType best first. A title
// TMDB does not know, or whose lookup fails, gets 0; only cancellation ends
// the sync.
func findTMDBIDs(ctx context.Context, client *tmdb.Client, itemType string, titles []catalog.TitleWithoutTMDBID) ([]int, error) {
	ids := make([]int, len(titles))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(tmdbExternalIDLookups)
	for i, title := range titles {
		g.Go(func() error {
			lookups := [][2]string{{tmdb.ExternalSourceIMDb, title.IMDbID}, {tmdb.ExternalSourceTVDB, title.TVDBID}}
			if itemType == "series" {
				lookups[0], lookups[1] = lookups[1], lookups[0]
			}
			for _, lookup := range lookups {
				if lookup[1] == "" {
					continue
				}
				results, err := client.FindByExternalIDCached(gctx, lookup[0], lookup[1])
				if err != nil {
					continue
				}
				for _, result := range results {
					if result.MediaType == itemType {
						ids[i] = result.ID
						return nil
					}
				}
			}
			return gctx.Err()
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return ids, nil
}

// adminJobArtifactURLTTL matches the presigned lifetime an S3 deployment hands
// out, so the two backends expire a download link on the same schedule.
const adminJobArtifactURLTTL = 15 * time.Minute

// newAdminJobArtifactSigner returns the signer for the streaming artifact
// route, or nil when the deployment has no use for it. Only a store that cannot
// presign needs the route, so an S3 deployment keeps handing out presigned URLs
// and never mints a capability. A deployment with no operational store has no
// artifacts to serve, and wiring the route there would advertise downloads
// through the job capabilities that can never complete.
func newAdminJobArtifactSigner(deps *Dependencies) *artworkurl.Signer {
	if deps.Config == nil || deps.S3Private != nil || deps.Blobs.Operational == nil {
		return nil
	}
	return artworkurl.NewJobArtifactSigner(deps.CurrentConfig().Auth.JWTSecret, adminJobArtifactURLTTL)
}

// llmConfigFromServer derives the shared AI client config from the server
// config. Used at construction and again on every config reload.
func llmConfigFromServer(cfg *config.Config) llm.Config {
	return llm.Config{
		AuthMode:        cfg.AI.AuthMode,
		ReasoningEffort: cfg.AI.ReasoningEffort,
		ServiceTier:     cfg.AI.ServiceTier,
		BaseURL:         cfg.AI.BaseURL,
		APIKey:          cfg.AI.APIKey,
		ChatModel:       cfg.AI.ChatModel,
		ASRBaseURL:      cfg.AI.ASRBaseURL,
		ASRAPIKey:       cfg.AI.ASRAPIKey,
		ASRModel:        cfg.AI.ASRModel,
	}
}

// effectiveSubtitleAIConfig derives the subtitle AI service config from the server config.
func effectiveSubtitleAIConfig(cfg *config.Config) subtitleai.Config {
	return subtitleai.Config{
		Configured:            cfg.AI.BaseURL != "" || cfg.AI.AuthMode == llm.AuthModeChatGPT,
		SpeechConfigured:      cfg.AI.ASRBaseURL != "",
		TranslateEnabled:      cfg.SubtitleAI.Enabled,
		TranscribeEnabled:     cfg.SubtitleAI.TranscribeEnabled,
		ChatModel:             cfg.AI.ChatModel,
		ASRModel:              cfg.AI.ASRModel,
		BatchSize:             cfg.SubtitleAI.BatchSize,
		ContextNeighbors:      cfg.SubtitleAI.ContextNeighbors,
		LiveASRChunkSeconds:   cfg.SubtitleAI.LiveASRChunkSeconds,
		TranscribeQuotaJobs:   cfg.SubtitleAI.TranscribeQuotaJobs,
		TranscribeQuotaPeriod: cfg.SubtitleAI.TranscribeQuotaPeriod,
	}
}

type scopeEntitlementResolver struct {
	resolver apimw.ViewerResolver
}

func (r scopeEntitlementResolver) MaxPlaybackQuality(ctx context.Context, userID int, profileID string) (string, error) {
	scope, err := r.resolveScope(ctx, userID, profileID)
	if err != nil {
		return "", err
	}
	return scope.MaxPlaybackQuality, nil
}

// MaxContentRating implements mediarequests.ContentRatingResolver so request
// discovery honors the profile's parental rating ceiling.
func (r scopeEntitlementResolver) MaxContentRating(ctx context.Context, userID int, profileID string) (string, error) {
	scope, err := r.resolveScope(ctx, userID, profileID)
	if err != nil {
		return "", err
	}
	return scope.MaxContentRating, nil
}

func (r scopeEntitlementResolver) resolveScope(ctx context.Context, userID int, profileID string) (access.Scope, error) {
	return r.resolver.Resolve(ctx, access.ResolveInput{
		UserID:              userID,
		ProfileID:           profileID,
		SkipPINVerification: true,
	})
}

// metadataAIConfigFromServer derives the metadata translation service config
// from the server config. Used at construction and on every config reload.
func metadataAIConfigFromServer(cfg *config.Config) metadatatranslation.Config {
	return metadatatranslation.Config{
		Enabled:    cfg.MetadataAI.Enabled,
		Configured: cfg.AI.BaseURL != "" || cfg.AI.AuthMode == llm.AuthModeChatGPT,
		ChatModel:  cfg.AI.ChatModel,
		OnView:     cfg.MetadataAI.OnView,
	}
}

// v2Dependencies assembles the gates the v2 listener composes onto its
// operations from the same middleware values the v1 groups use, so the two
// surfaces cannot drift in authorization strength.
func v2Dependencies(
	deps Dependencies,
	auth *apimw.AuthMiddleware,
	viewer *apimw.ViewerAccessMiddleware,
	actingAdmin apimw.ActingAdminDecider,
	metadataCuration func(http.Handler) http.Handler,
	markerEdit func(http.Handler) http.Handler,
	settings catalog.SettingsStore,
) apiv2.Dependencies {
	out := apiv2.Dependencies{
		Auth:            auth,
		ViewerAccess:    viewer,
		ActingAdmin:     actingAdmin,
		PermissionGates: map[string]func(http.Handler) http.Handler{},
		ArtworkStore:    deps.Blobs.Assets,
		ArtworkBackend:  deps.ArtworkBackend,
		ArtworkSigner:   deps.ArtworkSigner,
		ArtworkRepair:   deps.ArtworkRepair,
	}
	if viewer != nil && deps.UserStoreProvider != nil {
		// The same household rule v1 mounts on its profile-optional viewer
		// reads; v2 operations opt in with Operation.HouseholdProfileGate.
		out.HouseholdProfile = apimw.NewHouseholdProfileGate(deps.UserStoreProvider).Require
	}
	if metadataCuration != nil {
		out.PermissionGates[policy.PermissionMetadataCuration] = metadataCuration
	}
	if markerEdit != nil {
		out.PermissionGates[policy.PermissionMarkerEdit] = markerEdit
	}
	if settings != nil {
		out.DemoSettings = settings
		out.CatalogSettings = settings
		var declared ratingsources.DeclaredFunc
		if deps.DB != nil {
			declared = func(ctx context.Context) ([]ratingsources.DeclaredSource, error) {
				return metadata.DeclaredRatingSources(ctx, deps.DB)
			}
		}
		out.RatingSources = ratingsources.NewPolicy(settings, declared)
	}
	if deps.RateLimitMW != nil {
		out.RateLimit = deps.RateLimitMW.Handler
		out.BucketRateLimit = deps.RateLimitMW.AuthEndpointHandler
	}
	if deps.Config != nil {
		out.CursorSecret = []byte(deps.Config.Auth.JWTSecret)
	}
	return out
}

// virtualProviderStaleResolver decorates a virtual resolve port with the
// optional provider-staleness capability the playback resolve probes before it
// lists a provider. The embedded resolve path is unchanged; Stale and
// RefreshIfStale delegate to the core virtual library monitor, whose AltMount
// and Prowlarr clients know when their cached state has aged out and can
// refresh it under the resolve's bounded context. A port that does not carry
// the capability is unaffected.
type virtualProviderStaleResolver struct {
	handlers.VirtualMediaDetailedResolver
	stale   func() bool
	refresh func(context.Context) error
}

// Stale reports whether any configured virtual provider's cached state is due
// for a refresh.
func (r virtualProviderStaleResolver) Stale() bool {
	return r.stale != nil && r.stale()
}

// RefreshIfStale refreshes stale virtual provider state under ctx. A nil refresh
// is a no-op so a partially wired resolver never panics.
func (r virtualProviderStaleResolver) RefreshIfStale(ctx context.Context) error {
	if r.refresh == nil {
		return nil
	}
	return r.refresh(ctx)
}

// profileNamesByUser resolves an account's profile names through its user
// store; nil when there is no store provider.
func profileNamesByUser(stores userstore.UserStoreProvider) downloads.ProfileNamesFunc {
	if stores == nil {
		return nil
	}
	return func(ctx context.Context, userID int) (map[string]string, error) {
		store, err := stores.ForUser(ctx, userID)
		if err != nil || store == nil {
			return nil, err
		}
		profiles, err := store.ListProfiles(ctx)
		if err != nil {
			return nil, err
		}
		names := make(map[string]string, len(profiles))
		for _, profile := range profiles {
			if name := strings.TrimSpace(profile.Name); name != "" {
				names[profile.ID] = name
			}
		}
		return names, nil
	}
}

// newPersonalAPIKeyHandler builds the account-scoped API key handler with the
// household check its creation path runs.
func newPersonalAPIKeyHandler(repo *auth.APIKeyRepository, stores userstore.UserStoreProvider) *handlers.APIKeyHandler {
	h := handlers.NewAPIKeyHandler(repo)
	h.Stores = stores
	return h
}

func newDiscordNotificationsHandler(deps Dependencies) *handlers.DiscordNotificationsHandler {
	h := handlers.NewDiscordNotificationsHandler(deps.Notifications, deps.PublicURL)
	h.Stores = deps.UserStoreProvider
	return h
}

// boxSetSyncStarter starts the box set sync task when a library turns box
// sets on. A run already in progress looks the library up before it ends.
type boxSetSyncStarter struct {
	tasks *taskmanager.TaskManager
}

func (s boxSetSyncStarter) SyncBoxSetsSoon() {
	if _, err := s.tasks.StartTask(tasks.SyncMovieBoxSetsTaskKey); err != nil {
		slog.Debug("box set sync not started", "component", "api", "error", err)
	}
}
