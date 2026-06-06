package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/hashicorp/go-hclog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/activitylog"
	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/api"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/audiobooks"
	"github.com/Silo-Server/silo-server/internal/audiobooks/podcastfeed"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/autoscan"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalogseed"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/database"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/imagecache"
	"github.com/Silo-Server/silo-server/internal/intromarkers"
	"github.com/Silo-Server/silo-server/internal/jellycompat"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/logfilter"
	"github.com/Silo-Server/silo-server/internal/logstream"
	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/markers/introdb"
	"github.com/Silo-Server/silo-server/internal/mdblist"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/partman"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
	"github.com/Silo-Server/silo-server/internal/proxy"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/radarr"
	"github.com/Silo-Server/silo-server/internal/requests/sonarr"
	"github.com/Silo-Server/silo-server/internal/s3client"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/server"
	"github.com/Silo-Server/silo-server/internal/subtitles"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	taskrepository "github.com/Silo-Server/silo-server/internal/taskmanager/repository"
	"github.com/Silo-Server/silo-server/internal/taskmanager/tasks"
	"github.com/Silo-Server/silo-server/internal/taskmanager/triggers"
	"github.com/Silo-Server/silo-server/internal/transcodenode"
	"github.com/Silo-Server/silo-server/internal/usercollections"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
	"github.com/Silo-Server/silo-server/internal/watchsync"
	watchmdblist "github.com/Silo-Server/silo-server/internal/watchsync/providers/mdblist"
	"github.com/Silo-Server/silo-server/internal/watchsync/providers/simkl"
	"github.com/Silo-Server/silo-server/internal/watchsync/providers/trakt"
	"github.com/Silo-Server/silo-server/internal/worker"
	"github.com/Silo-Server/silo-server/migrations"
	siloweb "github.com/Silo-Server/silo-server/web"
)

// resolveNodeIdentity returns a stable node identifier used by the
// heartbeat writer, reconciler, and shutdown cleanup. Resolution order:
// SILO_NODE_NAME > NODE_NAME > os.Hostname().
func resolveNodeIdentity() string {
	if v := os.Getenv("SILO_NODE_NAME"); v != "" {
		return v
	}
	if v := os.Getenv("NODE_NAME"); v != "" {
		return v
	}
	h, _ := os.Hostname()
	return h
}

func resolvePluginCacheDir() string {
	if v := strings.TrimSpace(os.Getenv("SILO_PLUGIN_CACHE_DIR")); v != "" {
		return v
	}
	return filepath.Join(os.TempDir(), "silo-plugins")
}

func buildBaseHandler(format string, level slog.Level) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(os.Stderr, opts)
	}
	return slog.NewTextHandler(os.Stderr, opts)
}

func mustGetSetting(store interface {
	Get(context.Context, string) (string, error)
}, ctx context.Context, key, fallback string) string {
	value, err := store.Get(ctx, key)
	if err != nil || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func configureOperationalLogging(
	ctx context.Context,
	pool *pgxpool.Pool,
	settingsRepo *catalog.ServerSettingsRepo,
	redisCfg config.RedisConfig,
	logStreamHub *logstream.Hub,
	baseHandler slog.Handler,
	logQuiet string,
	nodeID string,
) (opslog.Writer, *opslog.Repo, *partman.Manager) {
	if err := opslog.SeedDefaults(ctx, settingsRepo); err != nil {
		log.Fatalf("seed opslog defaults: %v", err)
	}
	opsPM := partman.NewManager(pool, "operational_logs", partman.Daily, 3)
	if err := opsPM.EnsureFuturePartitions(ctx); err != nil {
		log.Fatalf("ensure operational log partitions: %v", err)
	}

	var operationalWriter opslog.Writer
	operationalConsumer := opslog.NewConsumer(pool, nil, logStreamHub)
	if redisCfg.URL != "" {
		redisClient, redisErr := cache.NewRedisClient(redisCfg)
		if redisErr == nil && redisClient != nil {
			operationalWriter = opslog.NewRedisWriter(redisClient)
			operationalConsumer = opslog.NewConsumer(pool, redisClient, logStreamHub)
			go operationalConsumer.RunRedis(ctx)
		}
	}
	if operationalWriter == nil {
		memWriter := opslog.NewMemoryWriter(10000)
		operationalWriter = memWriter
		go operationalConsumer.RunMemory(ctx, memWriter.Chan())
	}

	opsCaptureLevel := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(mustGetSetting(settingsRepo, ctx, "opslog.capture_level", "info"))) {
	case "debug":
		opsCaptureLevel = slog.LevelDebug
	case "warn", "warning":
		opsCaptureLevel = slog.LevelWarn
	case "error":
		opsCaptureLevel = slog.LevelError
	}

	filteredHandler := logfilter.New(baseHandler, logQuiet)
	slog.SetDefault(slog.New(opslog.NewHandler(filteredHandler, operationalWriter, opsCaptureLevel, nodeID)))

	return operationalWriter, opslog.NewRepo(pool), opsPM
}

func maybeApplyPostgresTuning(ctx context.Context, pool *pgxpool.Pool, appMaxConnections int, mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "integrated", "api":
	default:
		return
	}

	opts, err := database.LoadPostgresTuneOptionsFromEnv(appMaxConnections)
	if err != nil {
		slog.Warn("postgres auto-tuning disabled", "error", err)
		return
	}
	if !opts.Enabled {
		return
	}

	tuneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	result, err := database.ApplyPostgresTuning(tuneCtx, pool, opts)
	for _, failure := range result.Failures {
		slog.Warn("postgres auto-tuning setting failed",
			"name", failure.Name,
			"value", failure.Value,
			"error", failure.Err,
		)
	}
	if err != nil {
		slog.Warn("postgres auto-tuning failed",
			"error", err,
			"applied", result.Applied,
			"failures", len(result.Failures),
		)
		return
	}

	slog.Info("postgres auto-tuning applied",
		"profile", opts.Profile,
		"postgres_major", result.PostgresMajorVersion,
		"settings", result.Applied,
		"resets", len(result.Reset),
		"failures", len(result.Failures),
		"memory_budget_bytes", opts.MemoryBudgetBytes,
		"detected_memory_bytes", opts.DetectedMemoryBytes,
		"memory_source", opts.MemorySource,
		"memory_budget_percent", opts.MemoryBudgetPercent,
		"cpus", opts.CPUs,
		"connections", opts.Connections,
		"storage", opts.Storage,
		"db_size", result.DBSize,
		"database_size_bytes", result.DatabaseSizeBytes,
	)
	if len(result.RestartRequired) > 0 {
		slog.Warn("postgres restart required to finish applying auto-tuned settings",
			"settings", strings.Join(result.RestartRequired, ","),
		)
	}
	if len(result.Reset) > 0 {
		slog.Info("postgres auto-tuning reset stale settings",
			"settings", strings.Join(result.Reset, ","),
		)
	}
}

func main() {
	envFile := flag.String("env", ".env", "path to .env bootstrap file")
	flag.Parse()

	ctx := context.Background()

	// Step 1: Bootstrap from .env
	bc, err := config.LoadBootstrap(*envFile)
	if err != nil {
		log.Fatalf("bootstrap: %v", err)
	}

	// Step 2: Connect to PostgreSQL (bootstrap pool with default max connections)
	bootstrapDBCfg := config.DatabaseConfig{URL: bc.DatabaseURL, MaxConnections: 20}
	pool, err := database.NewPool(ctx, bootstrapDBCfg)
	if err != nil {
		log.Fatalf("database pool: %v", err)
	}
	defer pool.Close()
	slog.Info("connected to PostgreSQL")

	// Run migrations only for integrated/api modes. Proxy and transcode nodes
	// should never alter the schema — they may scale independently and would
	// race or apply migrations before the primary node is deliberately upgraded.
	if bc.Mode == "integrated" || bc.Mode == "api" || bc.Mode == "" {
		migCtx, migCancel := context.WithTimeout(ctx, 5*time.Minute)
		if migErr := database.RunMigrations(migCtx, pool, migrations.FS, "."); migErr != nil {
			migCancel()
			log.Fatalf("failed to run migrations: %v", migErr)
		}
		migCancel()
		slog.Info("database migrations applied")
	}

	// Step 3: Load settings from DB
	settingsRepo := catalog.NewServerSettingsRepo(pool)
	settings, err := settingsRepo.GetAll(ctx)
	if err != nil {
		log.Fatalf("loading settings: %v", err)
	}

	// Step 4: YAML import (one-time)
	yamlPath := "silo.yaml"
	if _, yamlErr := os.Stat(yamlPath); yamlErr == nil {
		if settings["_yaml_imported"] == "" {
			yamlSettings, importErr := config.YAMLToSettingsMap(yamlPath)
			if importErr != nil {
				log.Printf("WARN: could not import YAML config: %v", importErr)
			} else {
				for k, v := range yamlSettings {
					if err := settingsRepo.Set(ctx, k, v); err != nil {
						log.Printf("WARN: failed to import setting %s: %v", k, err)
					}
				}
				if err := settingsRepo.Set(ctx, "_yaml_imported", "true"); err != nil {
					slog.Warn("failed to set yaml import flag", "error", err)
				}
				log.Println("Imported config from silo.yaml — this file is no longer used")
				settings, _ = settingsRepo.GetAll(ctx)
			}
		}
	}

	// Step 5: Auto-generate secrets
	if settings["auth.jwt_secret"] == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatalf("generating jwt secret: %v", err)
		}
		encoded := base64.StdEncoding.EncodeToString(secret)
		if err := settingsRepo.Set(ctx, "auth.jwt_secret", encoded); err != nil {
			slog.Warn("failed to persist generated JWT secret", "error", err)
		}
		settings["auth.jwt_secret"] = encoded
	}
	if settings["jellyfin_compat.server_id"] == "" {
		serverID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://silo.local/jellycompat")).String()
		if err := settingsRepo.Set(ctx, "jellyfin_compat.server_id", serverID); err != nil {
			slog.Warn("failed to persist generated server ID", "error", err)
		}
		settings["jellyfin_compat.server_id"] = serverID
	}

	// Step 6: Build config from DB
	cfg, err := config.LoadFromDB(settings)
	if err != nil {
		log.Fatalf("building config: %v", err)
	}

	// Step 7: Apply bootstrap overrides
	cfg.Server.Listen = bc.Listen
	cfg.Server.Mode = bc.Mode
	cfg.Database.URL = bc.DatabaseURL
	cfg.JellyfinCompat.Listen = bc.JFListen
	if bc.RedisURL != "" {
		cfg.Redis.URL = bc.RedisURL
	}

	// Step 8: Recreate pool if max_connections differs from bootstrap default
	if cfg.Database.MaxConnections != bootstrapDBCfg.MaxConnections {
		pool.Close()
		pool, err = database.NewPool(ctx, cfg.Database)
		if err != nil {
			log.Fatalf("recreating pool with configured max_connections: %v", err)
		}
	}
	settingsRepo = catalog.NewServerSettingsRepo(pool)
	nodeID := resolveNodeIdentity()

	// Step 9: Validate
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	// Step 10: Configure log level
	var slogLevel slog.Level
	switch strings.ToLower(cfg.Server.LogLevel) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	baseHandler := buildBaseHandler(cfg.Server.LogFormat, slogLevel)
	slog.SetDefault(slog.New(logfilter.New(baseHandler, cfg.Server.LogQuiet)))

	mode := cfg.Server.Mode
	maybeApplyPostgresTuning(ctx, pool, cfg.Database.MaxConnections, mode)
	slog.Info("silo starting", "mode", mode, "listen", cfg.Server.Listen, "log_level", cfg.Server.LogLevel, "node_id", nodeID)

	appCtx, appCancel := context.WithCancel(ctx)
	defer appCancel()

	eventBus := cache.NewEventBus(cfg.Redis.URL)
	logStreamHub := logstream.NewHub(nodeID, eventBus)
	if err := logStreamHub.Start(appCtx); err != nil {
		log.Fatalf("log stream hub start: %v", err)
	}
	realtimeHub := notifications.NewHub(nodeID, eventBus)
	if err := realtimeHub.Start(appCtx); err != nil {
		log.Fatalf("realtime hub start: %v", err)
	}
	eventsHub := realtimeHub.EventsHub()
	scanRegistry := evt.NewScanRegistry()
	operationalWriter, opsRepo, opsPM := configureOperationalLogging(appCtx, pool, settingsRepo, cfg.Redis, logStreamHub, baseHandler, cfg.Server.LogQuiet, nodeID)
	defer func() {
		if err := eventBus.Close(); err != nil {
			slog.Warn("event bus close error", "error", err)
		}
	}()

	// Proxy and transcode modes run with DB + Redis for hot-reload.
	if mode == "proxy" || mode == "transcode" {
		redisClient, err := cache.NewRedisClient(cfg.Redis)
		if err != nil || redisClient == nil {
			slog.Error("redis is required for "+mode+" mode", "error", err)
			os.Exit(1)
		}

		bootstrap := nodeconfig.BootstrapOverrides{
			Listen:      cfg.Server.Listen,
			Mode:        cfg.Server.Mode,
			DatabaseURL: cfg.Database.URL,
			JFListen:    cfg.JellyfinCompat.Listen,
		}
		watcher := nodeconfig.NewWatcher(pool, eventBus, bootstrap)
		if err := watcher.Start(appCtx); err != nil {
			slog.Error("config watcher start failed", "error", err)
			os.Exit(1)
		}

		nodeURL := os.Getenv("NODE_URL")
		nodeName := os.Getenv("NODE_NAME")
		if nodeURL == "" {
			nodeURL = "http://localhost" + cfg.Server.Listen
			slog.Warn("NODE_URL not set, using listen address — session keys may collide across nodes")
		}
		if nodeName == "" {
			nodeName = mode
		}

		tracker := nodesessions.NewTracker(redisClient, nodeURL, nodeName, mode)
		tracker.StartRefresh(appCtx)
		defer func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			tracker.Cleanup(cleanupCtx)
		}()

		var handler http.Handler
		if mode == "proxy" {
			srv := proxy.NewServer(watcher, tracker)
			handler = srv.Handler()
		} else {
			srv := transcodenode.NewServer(watcher, tracker)
			srv.SetFFmpegLogSink(playback.NewSlogFFmpegLogSink(slog.Default(), nodeID))
			handler = srv.Handler()
		}

		_ = operationalWriter
		_ = opsRepo
		startStandaloneServer(cfg.Server.Listen, handler)
		return
	}

	// Determine which components to initialize based on mode.
	needsS3 := mode == "integrated" || mode == "api"
	needsScanner := mode == "integrated" || mode == "api"
	needsUserDB := mode == "integrated" || mode == "api"
	needsWorkers := mode == "integrated" || mode == "api"

	bootstrapSensitiveConfigured := map[string]bool{}
	bootstrapSensitiveValues := map[string]string{}
	if bc.RedisURL != "" {
		bootstrapSensitiveConfigured["redis.url"] = true
		bootstrapSensitiveValues["redis.url"] = bc.RedisURL
	}

	deps := api.Dependencies{
		Config:                       cfg,
		BootstrapSensitiveConfigured: bootstrapSensitiveConfigured,
		BootstrapSensitiveValues:     bootstrapSensitiveValues,
		AppContext:                   appCtx,
		DB:                           pool,
		EventBus:                     eventBus,
		LogStreamHub:                 logStreamHub,
		RealtimeHub:                  realtimeHub,
		EventsHub:                    eventsHub,
		ScanRegistry:                 scanRegistry,
		OpsLogRepo:                   opsRepo,
		FFmpegLogSink:                playback.NewSlogFFmpegLogSink(slog.Default(), nodeID),
		PublicURL:                    os.Getenv("SILO_PUBLIC_URL"),
	}
	audiobooksService := audiobooks.New(&audiobooksSettingsAdapter{repo: settingsRepo})
	audiobooksEnabled, err := audiobooksService.Enabled(appCtx)
	if err != nil {
		slog.Warn("audiobooks feature disabled; failed to read setting", "err", err)
		audiobooksEnabled = false
	}
	deps.AudiobooksEnabled = audiobooksEnabled
	adminJobCancelRegistry := adminjob.NewCancelRegistry()
	deps.AdminJobCancelRegistry = adminJobCancelRegistry
	if needsWorkers && deps.DB != nil {
		deps.IntroRepository = intromarkers.NewRepository(deps.DB)
		deps.IntroAnalyzer = intromarkers.NewAnalyzer(
			deps.IntroRepository,
			intromarkers.DefaultConfig(cfg.Playback.FFmpegPath),
			slog.Default(),
		)
	}
	if deps.DB != nil {
		markerRegistry := markers.NewRegistry(slog.Default())
		introdbAPIKey, err := settingsRepo.Get(appCtx, "introdb.api_key")
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("load introdb.api_key from settings failed; provider will run with no key",
				"error", err)
			introdbAPIKey = ""
		}
		introdbClient := introdb.NewClient(introdbAPIKey)
		if err := markerRegistry.Register(introdb.NewProvider(introdbClient)); err != nil {
			log.Fatalf("register introdb marker provider: %v", err)
		}
		deps.OnServerSettingUpdated = func(_ context.Context, key, value string) {
			if key == "introdb.api_key" {
				introdbClient.SetAPIKey(value)
			}
		}
		if eventBus != nil {
			_ = eventBus.Subscribe(appCtx, cache.ChannelAdmin, func(event cache.Event) {
				if event.Type != cache.EventSettingsChanged || event.Payload != "introdb.api_key" {
					return
				}
				value, loadErr := settingsRepo.Get(context.Background(), "introdb.api_key")
				if loadErr != nil {
					slog.Warn("introdb api key reload failed", "error", loadErr)
					return
				}
				introdbClient.SetAPIKey(value)
			})
		}
		deps.MarkerRegistry = markerRegistry
		deps.MarkerResolver = markers.NewDBExternalIDResolver(deps.DB)
	}
	var watchProviderService *watchsync.Service
	if deps.DB != nil {
		watchProviderRegistry := watchsync.NewRegistry()
		if err := watchProviderRegistry.Register(trakt.NewProvider(nil, "")); err != nil {
			log.Fatalf("register watch provider: %v", err)
		}
		if err := watchProviderRegistry.Register(simkl.NewProvider(nil, "")); err != nil {
			log.Fatalf("register watch provider: %v", err)
		}
		if err := watchProviderRegistry.Register(watchmdblist.NewProvider(nil, "")); err != nil {
			log.Fatalf("register watch provider: %v", err)
		}
		watchProviderService = watchsync.NewService(
			watchsync.NewPostgresRepository(deps.DB),
			watchProviderRegistry,
		)
		deps.WatchProviderService = watchProviderService
	}

	// Initialize node pools for integrated/api modes.
	if mode == "integrated" || mode == "api" {
		nodeRepo := nodepool.NewRepository(pool)
		deps.NodeRepo = nodeRepo

		proxyPool := nodepool.NewProxyPool()
		transcodePool := nodepool.NewTranscodePool()

		proxyNodes, _ := nodeRepo.ListEnabled(context.Background(), nodepool.NodeTypeProxy)
		transcodeNodes, _ := nodeRepo.ListEnabled(context.Background(), nodepool.NodeTypeTranscode)
		proxyPool.SetNodes(proxyNodes)
		transcodePool.SetNodes(transcodeNodes)

		deps.ProxyPool = proxyPool
		deps.TranscodePool = transcodePool

		healthChecker := nodepool.NewHealthChecker(proxyPool, transcodePool, nodeRepo)
		healthChecker.Start(appCtx)
		slog.Info("node pools initialized", "proxy_nodes", len(proxyNodes), "transcode_nodes", len(transcodeNodes))

		// Subscribe to node pool change events for multi-instance reload.
		_ = eventBus.Subscribe(appCtx, cache.ChannelAdmin, func(event cache.Event) {
			if event.Type == cache.EventNodePoolChanged {
				pNodes, pErr := nodeRepo.ListEnabled(context.Background(), nodepool.NodeTypeProxy)
				tNodes, tErr := nodeRepo.ListEnabled(context.Background(), nodepool.NodeTypeTranscode)
				if pErr != nil || tErr != nil {
					slog.Warn("node pool reload from event failed, keeping current pools",
						"proxy_err", pErr, "transcode_err", tErr)
					return
				}
				proxyPool.SetNodes(pNodes)
				transcodePool.SetNodes(tNodes)
				slog.Info("node pools reloaded from event", "proxy", len(pNodes), "transcode", len(tNodes))
			}
		})
	}

	// Step 3: Create S3 clients (if needed).
	if needsS3 {
		configureS3Clients(cfg, &deps)
	}

	// Step 4: Create scanner (if needed).
	if needsScanner && deps.DB != nil {
		folderRepo := catalog.NewFolderRepository(deps.DB)
		fileRepo := scanner.NewFileRepository(deps.DB)
		deps.FolderRepo = folderRepo
		deps.FileRepo = fileRepo

		ffprobePath := scanner.FFprobePathFromFFmpeg(cfg.Playback.FFmpegPath)
		s := scanner.NewScanner(fileRepo, ffprobePath, deps.S3Public, cfg.Scanner.Workers, cfg.Scanner.EmptyTrashAfterScan)
		deps.Scanner = s
		deps.ProbeEnsurer = scanner.NewPlaybackProbeEnsurer(fileRepo, ffprobePath, 10*time.Second)
		slog.Info("scanner initialized")
	}

	var chapterThumbService *chapterthumbs.Service
	if deps.FileRepo != nil && deps.FolderRepo != nil && deps.S3Public != nil {
		chapterThumbService = chapterthumbs.NewService(
			deps.FileRepo,
			deps.FolderRepo,
			deps.ProbeEnsurer,
			settingsRepo,
			deps.S3Public,
			nil,
			deps.TranscodePool,
			cfg.Playback.FFmpegPath,
			cfg.Playback.HWAccel,
			cfg.Playback.HWDevice,
			cfg.Playback.ChapterThumbnailWorkers,
		)
		if chapterThumbService != nil {
			chapterThumbService.Start(appCtx)
			deps.ChapterThumbnailQueuer = chapterThumbService
		}
	}

	var pluginHost *pluginhost.Host
	var pluginService *plugins.Service
	var pluginInstallationStore *plugins.InstallationStore
	var pluginRuntimeConfigStore *plugins.RuntimeConfigStore
	var pluginHTTPProxy *plugins.HTTPProxy
	pluginAutoUpdateDone := make(chan struct{})
	var pluginAutoUpdater *plugins.AutoUpdateService
	if deps.DB != nil {
		pluginCacheDir := resolvePluginCacheDir()
		repositoryStore := plugins.NewRepositoryStore(deps.DB)
		installationStore := plugins.NewInstallationStore(deps.DB)
		runtimeConfigStore := plugins.NewRuntimeConfigStore(deps.DB)
		catalogService := plugins.NewCatalogService(repositoryStore, plugins.CatalogServiceOptions{
			SiloAPIVersion: plugins.DefaultSiloAPIVersion,
		})
		installer := plugins.NewInstaller(installationStore, plugins.InstallerOptions{
			BaseDir: pluginCacheDir,
		})

		libDataSource := pluginhost.LibraryDataSourceFunc(
			func(ctx context.Context, _ string) ([]pluginhost.LibraryRecord, error) {
				// TODO: scope by userID when the requests plugin needs it (Plan B).
				// For now, all callers see admin-scope.
				if deps.FolderRepo == nil {
					return nil, nil
				}
				folders, err := deps.FolderRepo.List(ctx)
				if err != nil {
					return nil, err
				}
				out := make([]pluginhost.LibraryRecord, 0, len(folders))
				for _, f := range folders {
					out = append(out, pluginhost.LibraryRecord{
						ID:        strconv.Itoa(f.ID),
						Name:      f.Name,
						MediaType: mapFolderTypeToMediaType(f.Type),
					})
				}
				return out, nil
			},
		)
		presenceItemRepo := catalog.NewItemRepository(deps.DB)
		catalogPresence := pluginhost.NewCatalogPresence(
			func(ctx context.Context, mediaType string, tmdbIDs []string) ([]pluginhost.LibraryPresenceRecord, error) {
				rows, err := presenceItemRepo.LookupTMDBIDs(ctx, mediaType, tmdbIDs)
				if err != nil {
					return nil, err
				}
				out := make([]pluginhost.LibraryPresenceRecord, 0, len(rows))
				for _, r := range rows {
					out = append(out, pluginhost.LibraryPresenceRecord{
						ExternalID: r.TMDBID,
						MediaID:    r.MediaID,
						LibraryID:  r.LibraryID,
						Title:      r.Title,
					})
				}
				return out, nil
			},
		)
		pluginHost = pluginhost.NewHost(pluginhost.Config{
			EventPublisher:  eventsHub,
			LibraryLister:   pluginhost.NewLibraryLister(libDataSource),
			CatalogPresence: catalogPresence,
			InstalledPlugins: pluginhost.InstalledPluginListerFunc(
				func(ctx context.Context) ([]pluginhost.InstalledPluginRecord, error) {
					installations, err := installationStore.List(ctx)
					if err != nil {
						return nil, err
					}
					out := make([]pluginhost.InstalledPluginRecord, 0, len(installations))
					for _, installation := range installations {
						capabilities, err := installationStore.ListCapabilities(ctx, installation.ID)
						if err != nil {
							return nil, err
						}
						descriptors := make([]*pluginv1.CapabilityDescriptor, 0, len(capabilities))
						for _, capability := range capabilities {
							descriptor, err := plugins.DecodeCapability(capability)
							if err != nil {
								return nil, err
							}
							descriptors = append(descriptors, descriptor)
						}
						out = append(out, pluginhost.InstalledPluginRecord{
							InstallationID: installation.ID,
							PluginID:       installation.PluginID,
							Version:        installation.Version,
							Enabled:        installation.Enabled,
							Capabilities:   descriptors,
						})
					}
					return out, nil
				},
			),
			GlobalConfigSetter: pluginhost.GlobalConfigSetterFunc(
				func(ctx context.Context, installationID int, key string, value map[string]any) error {
					return runtimeConfigStore.PutGlobalConfig(ctx, installationID, key, value)
				},
			),
			Logger: hclog.New(&hclog.LoggerOptions{
				Name:   "plugin-host",
				Level:  hclog.Info,
				Output: os.Stderr,
			}),
		})
		pluginService = plugins.NewService(
			repositoryStore,
			installationStore,
			runtimeConfigStore,
			catalogService,
			installer,
			plugins.NewHostAdapter(pluginHost),
		)
		if err := pluginService.PreloadEnabled(appCtx); err != nil {
			log.Fatalf("preload enabled plugins: %v", err)
		}
		slog.Info("plugin cache initialized", "base_dir", pluginCacheDir)

		pluginAutoUpdater = plugins.NewAutoUpdateService(
			repositoryStore,
			installationStore,
			catalogService,
			installer,
			pluginHost,
			slog.Default(),
		)
		go func() {
			defer close(pluginAutoUpdateDone)
			if err := pluginAutoUpdater.Run(appCtx); err != nil {
				slog.Error("plugin auto-update failed", "error", err)
			}
		}()
		pluginInstallationStore = installationStore
		pluginRuntimeConfigStore = runtimeConfigStore
		pluginHTTPProxy = plugins.NewHTTPProxyWithTypedResolver(pluginService, pluginInstallationStore)
		if deps.DB != nil {
			pluginHTTPProxy = pluginHTTPProxy.WithUserThemeLookup(plugins.NewPgUserThemeLookup(deps.DB))
			pluginHTTPProxy = pluginHTTPProxy.WithUserIdentityLookup(plugins.NewPgUserIdentityLookup(deps.DB))
		}
		deps.PluginService = pluginService
		deps.PluginHTTPProxy = pluginHTTPProxy
		defer func() {
			if pluginHost == nil {
				return
			}
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := pluginHost.Shutdown(shutdownCtx); err != nil {
				slog.Warn("failed to shut down plugin host", "error", err)
			}
		}()
	} else {
		close(pluginAutoUpdateDone)
	}
	if pluginService != nil && pluginInstallationStore != nil {
		dispatcher := plugins.NewEventDispatcherWithTypedResolver(deps.EventBus, deps.EventsHub, pluginInstallationStore, pluginService, 4)
		pluginService.SetEventDispatcher(dispatcher)
		if err := dispatcher.Start(appCtx); err != nil {
			log.Fatalf("plugin event dispatcher: %v", err)
		}
		defer dispatcher.Stop()
		// Backfill the capability-subscriber index from the already-preloaded
		// installations. PreloadEnabled ran earlier (before the dispatcher
		// existed), so its rebuildDispatcherIndex was a no-op. Without this
		// call, capability-scoped subscriptions never fire until the next
		// lifecycle mutation.
		pluginService.OnLifecycleChange(appCtx)
	}

	// backgroundInit collects non-critical startup work (catalog-size-dependent
	// seeding, network-bound reconciliation) that must not block the HTTP
	// listener. The steps run sequentially in a background goroutine once the
	// server is ready to serve. Failures are logged, never fatal.
	var backgroundInit []func(context.Context)

	// Step 4b: Create metadata service and match worker (if needed).
	var metadataService *metadata.MetadataService
	var personRefreshService *metadata.PersonRefreshService
	var matchWorker *metadata.MatchWorker
	var libraryIngestExecutor *libraryingest.Executor
	var libraryScanQueue *scanqueue.Service
	var itemRefreshExecutor *adminjob.ItemRefreshExecutor
	var libraryRefreshExecutor *adminjob.LibraryRefreshExecutor
	var itemRepo *catalog.ItemRepository
	var skippedRootRepo *metadata.SkippedRootRepository
	var movieQueueRepo *metadata.MovieMatchQueueRepository
	var seriesQueueRepo *metadata.SeriesRootMatchQueueRepository
	var matchQueueCoordinator *metadata.MatchQueueCoordinator
	var rootClaimRepo *catalog.RootClaimRepository
	var groupClaimRepo *catalog.GroupClaimRepository
	var seasonRepo *catalog.SeasonRepository
	var episodeRepo *catalog.EpisodeRepository
	var audiobookEnricher *audiobooks.Enricher
	if needsWorkers && deps.DB != nil && deps.FileRepo != nil {
		chainRepo := metadata.NewChainRepository(deps.DB)
		skippedRootRepo = metadata.NewSkippedRootRepository(deps.DB)
		itemRepo = catalog.NewItemRepository(deps.DB)
		episodeRepo = catalog.NewEpisodeRepository(deps.DB)
		seasonRepo = catalog.NewSeasonRepository(deps.DB)
		personRepo := catalog.NewPersonRepository(deps.DB)
		libraryRepo := catalog.NewLibraryItemRepository(deps.DB)

		// Wait for plugin auto-update to finish before registering image resolvers.
		<-pluginAutoUpdateDone

		imageResolver := metadata.NewPluginImageResolver()
		if pluginService != nil {
			// Register image resolver sources for all enabled plugin metadata providers.
			rows, err := deps.DB.Query(appCtx,
				`SELECT pc.plugin_installation_id, pc.capability_id
				 FROM plugin_capabilities pc
				 JOIN plugin_installations pi ON pi.id = pc.plugin_installation_id
				 WHERE pc.capability_type = 'metadata_provider.v1' AND pi.enabled = true`)
			if err != nil {
				slog.Warn("failed to list capabilities for image resolver registration", "error", err)
			} else {
				defer rows.Close()
				for rows.Next() {
					var instID int
					var capID string
					if err := rows.Scan(&instID, &capID); err != nil {
						slog.Warn("failed to scan capability for image resolver", "error", err)
						continue
					}
					source := metadata.NewPluginClientSource(instID, capID, func(
						ctx context.Context, installationID int, capabilityID string,
					) (metadata.PluginMetadataClient, error) {
						return pluginService.MetadataProviderClient(ctx, installationID, capabilityID)
					})
					imageResolver.RegisterSource(capID, source)
					slog.Info("registered plugin image resolver", "capability_id", capID, "installation_id", instID)
				}
			}
		}
		if deps.S3Public != nil {
			presignTTL := cfg.S3.MetadataPresignExpiry
			if presignTTL <= 0 {
				presignTTL = 4 * time.Hour
			}
			imageResolver.SetS3Presigner(deps.S3Public, deps.S3Public.EffectivePresignTTL(presignTTL))
		}
		deps.ImageResolver = imageResolver
		deps.PluginImageResolver = imageResolver

		staleIDRepo := metadata.NewStaleMediaIDRepository(deps.DB)
		providerIDRepo := catalog.NewProviderIDRepository(deps.DB)
		movieQueueRepo = metadata.NewMovieMatchQueueRepository(deps.DB, deps.FileRepo)
		seriesQueueRepo = metadata.NewSeriesRootMatchQueueRepository(deps.DB)
		deps.MovieMatchQueueRepo = movieQueueRepo
		deps.SeriesRootMatchQueueRepo = seriesQueueRepo
		matchQueueCoordinator = metadata.NewMatchQueueCoordinator(movieQueueRepo, seriesQueueRepo)
		rootClaimRepo = catalog.NewRootClaimRepository(deps.DB)
		groupClaimRepo = catalog.NewGroupClaimRepository(deps.DB)
		pluginResolver := metadata.NewPluginResolverAdapter(pluginService)
		metadataService = metadata.NewMetadataService(
			chainRepo, pluginResolver,
			itemRepo, providerIDRepo, episodeRepo, seasonRepo, libraryRepo, deps.FolderRepo,
			personRepo,
			deps.FileRepo, skippedRootRepo, staleIDRepo, rootClaimRepo,
		)
		personRefreshService = metadata.NewPersonRefreshService(deps.DB, pluginResolver, personRepo)
		personRefreshService.SetImageResolver(imageResolver)

		// Wire the audiobook enricher. It uses the same plugin resolver and chain
		// repo as the movie/TV pipeline, but resolves providers at
		// content_level='audiobook' and sweeps items directly rather than via a queue.
		audiobookEnricher = audiobooks.NewEnricher(
			deps.DB,
			chainRepo,
			pluginResolver,
			itemRepo,
			personRepo,
			providerIDRepo,
		)

		// Always wire the image resolver so plugin-prefixed URLs (e.g.
		// metadb://) can be resolved to presigned HTTP URLs in API responses.
		metadataService.SetImageResolver(imageResolver)

		// Wire the image cacher whenever object storage is available so explicit
		// admin image applies can succeed even if automatic metadata caching is off.
		if deps.S3Public != nil {
			imageCacher := imagecache.New(deps.S3Public)
			metadataService.SetImageCacher(imageCacher)
			metadataService.SetAutoCacheImages(cfg.Metadata.CacheImages)
			if deps.Scanner != nil {
				deps.Scanner.SetImageCacher(imageCacher)
			}
			if cfg.Metadata.CacheImages {
				personRefreshService.SetImageCacher(imageCacher)
				slog.Info("metadata image caching enabled")
			}
			if audiobookEnricher != nil {
				audiobookEnricher.SetImageCacher(imageCacher)
				audiobookEnricher.SetFFmpegPath(scanner.FFmpegPathFromFFprobe(scanner.FFprobePathFromFFmpeg(cfg.Playback.FFmpegPath)))
			}
		}

		matchWorker = metadata.NewMatchWorker(metadataService, deps.FileRepo, cfg.Matcher.Workers, cfg.Matcher.BatchSize, 30*time.Second)
		matchWorker.SetRealtimeHub(deps.RealtimeHub)
		if movieQueueRepo != nil {
			matchWorker.SetMovieFileClaimer(movieQueueRepo)
		}
		if seriesQueueRepo != nil {
			matchWorker.SetSeriesRootClaimer(seriesQueueRepo, cfg.Matcher.TVSeriesRootQueueEnabled())
			backgroundInit = append(backgroundInit, func(ctx context.Context) {
				if cleaned, err := seriesQueueRepo.CleanupLegacySeriesGroupQueue(ctx); err != nil {
					slog.Warn("failed to clean legacy series group queue rows", "error", err)
				} else if cleaned > 0 {
					slog.Info("cleaned legacy series group queue rows", "count", cleaned)
				}
			})
		}
		if deps.FolderRepo != nil {
			backgroundInit = append(backgroundInit, func(ctx context.Context) {
				start := time.Now()
				enabledFolders, err := deps.FolderRepo.GetEnabled(ctx)
				if err != nil {
					slog.Warn("failed to seed metadata queues", "error", err)
					return
				}
				seedMovieQueue := func(folderID int) {
					if movieQueueRepo == nil {
						return
					}
					if err := movieQueueRepo.SyncForFolder(ctx, folderID); err != nil {
						slog.Warn("failed to seed movie match queue", "folder_id", folderID, "error", err)
					}
				}
				seedSeriesQueue := func(folderID int) {
					if seriesQueueRepo == nil {
						return
					}
					if err := seriesQueueRepo.SyncForFolder(ctx, folderID); err != nil {
						slog.Warn("failed to seed series root queue", "folder_id", folderID, "error", err)
					}
				}
				for _, folder := range enabledFolders {
					if folder == nil {
						continue
					}
					switch strings.ToLower(strings.TrimSpace(folder.Type)) {
					case "movie", "movies":
						seedMovieQueue(folder.ID)
					case "series", "tv", "show", "tvshows":
						seedSeriesQueue(folder.ID)
					case "mixed":
						seedSeriesQueue(folder.ID)
						seedMovieQueue(folder.ID)
					}
				}
				slog.Info("deferred init: metadata match queues seeded", "folders", len(enabledFolders), "duration", time.Since(start))
			})
		}

		deps.SkippedRootRepo = skippedRootRepo
		deps.StaleIDRepo = staleIDRepo
		deps.PersonRepo = personRepo
		deps.PersonRefreshQueue = worker.NewPersonRefreshWorker(
			personRefreshService,
			worker.DefaultPersonRefreshWorkerConfig(),
		)
		deps.PersonRefresher = personRefreshService
		deps.Refresher = metadataService
		deps.MetadataService = metadataService
		slog.Info("metadata service initialized and running")

	}
	if deps.Scanner != nil {
		if matchQueueCoordinator != nil {
			deps.Scanner.SetMetadataQueueProducer(matchQueueCoordinator)
		}
		if movieQueueRepo != nil {
			deps.Scanner.SetMovieQueueSyncer(movieQueueRepo)
		}
		if seriesQueueRepo != nil {
			deps.Scanner.SetSeriesQueueSyncer(seriesQueueRepo)
		}
	}
	if deps.Scanner != nil && matchWorker != nil && deps.FolderRepo != nil && skippedRootRepo != nil {
		libraryIngestExecutor = libraryingest.NewExecutor(
			deps.Scanner,
			matchWorker,
			deps.FolderRepo,
			skippedRootRepo,
			deps.EventBus,
			deps.RealtimeHub,
		)
		deps.LibraryIngester = libraryIngestExecutor
		if deps.DB != nil {
			libraryScanQueue = scanqueue.NewService(
				scanqueue.NewRepository(deps.DB),
				deps.FolderRepo,
				libraryIngestExecutor,
				deps.EventsHub,
				appCtx,
				cfg.Scanner.MaxConcurrentLibraries,
				cfg.Scanner.MaxConcurrentScoped,
			)
			libraryScanQueue.Start()
			defer libraryScanQueue.Stop()
			deps.LibraryScanQueue = libraryScanQueue
		}
		if deps.DB != nil && deps.FileRepo != nil && metadataService != nil {
			itemRefreshResolver := adminjob.NewItemRefreshResolver(
				itemRepo,
				seasonRepo,
				episodeRepo,
				deps.FolderRepo,
				deps.FileRepo,
			)
			libraryRefreshExecutor = adminjob.NewLibraryRefreshExecutor(
				adminjob.NewPGLibraryRefreshItemLister(deps.DB),
				deps.FolderRepo,
				itemRefreshResolver,
				libraryIngestExecutor,
				metadataService,
				deps.EventBus,
				deps.RealtimeHub,
			)
		}
		if metadataService != nil && deps.FileRepo != nil {
			itemRefreshExecutor = adminjob.NewItemRefreshExecutor(
				deps.FolderRepo,
				deps.FileRepo,
				rootClaimRepo,
				groupClaimRepo,
				skippedRootRepo,
				seasonRepo,
				episodeRepo,
				libraryIngestExecutor,
				metadataService,
				deps.EventBus,
				deps.RealtimeHub,
			)
		}
	}

	// Ensure PersonRepo is available for the router's DetailService.
	if deps.DB != nil && deps.PersonRepo == nil {
		deps.PersonRepo = catalog.NewPersonRepository(deps.DB)
	}

	// Step 5: Create user store provider (if needed).
	var userStoreProvider userstore.UserStoreProvider
	if needsUserDB {
		switch cfg.UserDB.Backend {
		case "sqlite":
			poolConfig := userdb.PoolConfig{
				MaxOpen:     cfg.UserDB.PoolMaxOpen,
				IdleTimeout: cfg.UserDB.IdleTimeout,
				DataDir:     "/var/lib/silo/userdb",
			}
			pool := userdb.NewUserDBPool(poolConfig)
			userStoreProvider = userdb.NewSQLiteProvider(pool)
			slog.Info("user store initialized", "backend", "sqlite", "max_open", poolConfig.MaxOpen)
		default: // "postgres"
			userStoreProvider = pgstore.NewPostgresProvider(deps.DB)
			slog.Info("user store initialized", "backend", "postgres")
		}
		defer userStoreProvider.Close()
	}
	if userStoreProvider != nil && pluginService != nil {
		deps.PluginUserConfig = plugins.NewUserConfigStore(userStoreProvider, pluginService)
	}

	// Step 6: Create playback session manager and wire into dependencies.
	sessionMgr := playback.NewSessionManager(6, 2) // defaults from plan: max_streams=6, max_transcodes=2
	if deps.DB != nil {
		userRepo := auth.NewUserRepository(deps.DB)
		sessionMgr.SetLimitProvider(func(ctx context.Context, userID int) (playback.SessionLimits, error) {
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
	if userStoreProvider != nil {
		deps.UserStoreProvider = userStoreProvider
	}
	if watchProviderService != nil {
		historyRepo := historyimport.NewRepository(deps.DB)
		historyIdentity := watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, catalog.NewProviderIDRepository(deps.DB))
		watchProviderService.
			WithMatcher(historyimport.NewMatcher(historyRepo)).
			WithWatchState(watchstate.NewService(userStoreProvider).WithStableIdentityResolver(historyIdentity)).
			WithUserStoreProvider(userStoreProvider)
		backgroundInit = append(backgroundInit, func(ctx context.Context) {
			if err := watchProviderService.SweepOpenScrobbles(ctx); err != nil {
				slog.Warn("failed to sweep open watch provider scrobbles", "error", err)
			}
		})
	}
	deps.SessionMgr = sessionMgr
	deps.PlaybackRealtimeHub = playback.NewRealtimeHub()
	if chapterThumbService != nil && deps.S3Public != nil {
		chapterThumbService.SetNotifier(
			playback.NewChapterThumbnailNotifier(sessionMgr, deps.PlaybackRealtimeHub, deps.S3Public, 0),
		)
	}

	// Build the reconciler early enough that playback handlers can trigger
	// immediate session syncs after start/stop events.
	nodeIdentity := resolveNodeIdentity()

	var reconciler *worker.Reconciler
	var heartbeatWriter *worker.HeartbeatWriter
	if needsWorkers && deps.DB != nil {
		sessionProvider := func() []worker.SessionSync {
			sessions := sessionMgr.AllSessions()
			syncs := make([]worker.SessionSync, len(sessions))
			for i, s := range sessions {
				syncs[i] = buildLiveSessionSync(s, nodeIdentity)
			}
			return syncs
		}
		reconciler = worker.NewReconciler(deps.DB, nodeIdentity, sessionProvider)
		reconciler.EventBus = deps.EventBus
		reconciler.EventsHub = deps.EventsHub
		reconciler.PreSync = func() {
			// Retire sessions that have not shown real playback activity
			// recently enough to count as live. This keeps the in-memory
			// limiter, transcode teardown, and synced admin view aligned.
			if expired := sessionMgr.CleanStale(); len(expired) > 0 {
				slog.Info("expired idle sessions", "count", len(expired))
			}
		}
		deps.SessionSyncer = reconciler

		nodeURL := fmt.Sprintf("http://%s%s", nodeIdentity, cfg.Server.Listen)
		heartbeatWriter = worker.NewHeartbeatWriter(deps.DB, nodeIdentity, mode, nodeURL)
	}

	if deps.DB != nil {
		adminStatsProvider, statsErr := handlers.NewAdminStatsProvider(appCtx, deps.DB, deps.EventBus)
		if statsErr != nil {
			log.Fatalf("failed to create admin stats provider: %v", statsErr)
		}
		defer adminStatsProvider.Close()
		deps.AdminStatsProvider = adminStatsProvider
	}

	// Wire recommendations engine, worker, and ratings repo if enabled.
	var recEngine *recommendations.Engine
	var recWorker *recommendations.Worker
	if cfg.Recommendations.Enabled && deps.DB != nil {
		deps.RatingsRepo = catalog.NewRatingsRepo(deps.DB)
		recEngine = recommendations.NewEngine(
			deps.DB,
			deps.RatingsRepo,
			catalog.NewItemRepository(deps.DB),
			catalog.NewPersonRepository(deps.DB),
			userStoreProvider,
			cfg.Recommendations,
		)
		deps.Recommender = recEngine

		var err error
		recWorker, err = recommendations.NewWorker(
			recEngine,
			cfg.Recommendations.EmbeddingsCron,
			cfg.Recommendations.TasteProfilesCron,
			cfg.Recommendations.CowatchCron,
			cfg.Recommendations.RecommendationsCron,
		)
		if err != nil {
			slog.Error("failed to create recommendation worker", "error", err)
		} else {
			deps.RecWorker = recWorker
		}
	}

	// Client IP resolver with trusted proxy config.
	if err := clientip.SeedDefaults(ctx, settingsRepo); err != nil {
		log.Fatalf("seed clientip defaults: %v", err)
	}
	trustedCIDRs, err := clientip.LoadTrustedCIDRs(ctx, settingsRepo)
	if err != nil {
		log.Fatalf("load trusted CIDRs: %v", err)
	}
	ipResolver := clientip.NewResolver(trustedCIDRs)
	deps.ClientIPResolver = ipResolver

	// Step 6b: Create rate limiter.
	if cfg.RateLimit.Enabled && deps.DB != nil {
		var perKeyLimiter, globalLimiter ratelimit.RateLimiter
		isMemory := true

		if cfg.RateLimit.Backend == "redis" {
			redisClient, redisErr := cache.NewRedisClient(cfg.Redis)
			if redisErr != nil {
				log.Fatalf("failed to create Redis client for rate limiting: %v", redisErr)
			}
			if redisClient != nil {
				perKeyLimiter = ratelimit.NewRedisLimiter(redisClient)
				globalLimiter = ratelimit.NewRedisLimiter(redisClient)
				isMemory = false
				defer redisClient.Close()
			}
		}

		if isMemory {
			perKeyLimiter = ratelimit.NewMemoryLimiter()
			globalLimiter = ratelimit.NewMemoryLimiter()
		}
		defer perKeyLimiter.Close()
		defer globalLimiter.Close()

		rateLimitMW := ratelimit.NewMiddleware(perKeyLimiter, globalLimiter, settingsRepo, isMemory)
		if err := rateLimitMW.Init(context.Background()); err != nil {
			log.Fatalf("failed to init rate limiter: %v", err)
		}

		// Subscribe for multi-instance reload (only fires if EventBus is Redis-backed)
		_ = eventBus.Subscribe(appCtx, cache.ChannelAdmin, func(event cache.Event) {
			if event.Type == cache.EventSettingsChanged {
				if reloadErr := rateLimitMW.Reload(context.Background()); reloadErr != nil {
					slog.Warn("rate limit config reload from event failed", "error", reloadErr)
				}
				// Reload trusted proxies
				cidrs, loadErr := clientip.LoadTrustedCIDRs(context.Background(), settingsRepo)
				if loadErr != nil {
					slog.Warn("clientip config reload failed", "error", loadErr)
				} else {
					ipResolver.UpdateTrustedCIDRs(cidrs)
				}
			}
		})

		deps.RateLimitMW = rateLimitMW
	}

	// Activity log writer + consumer.
	if err := activitylog.SeedDefaults(ctx, settingsRepo); err != nil {
		log.Fatalf("seed activitylog defaults: %v", err)
	}

	// Seed default page sections for home and existing libraries.
	sectionRepo := sections.NewRepository(pool)
	var folders []*models.MediaFolder
	if deps.FolderRepo != nil {
		var listErr error
		folders, listErr = deps.FolderRepo.List(ctx)
		if listErr != nil {
			log.Fatalf("list libraries for section defaults: %v", listErr)
		}
	}
	if err := sectionRepo.SeedDefaults(ctx, "home", nil, sections.DefaultHomeSections(folders)); err != nil {
		log.Fatalf("seed home section defaults: %v", err)
	}
	if deps.FolderRepo != nil {
		for _, f := range folders {
			id := f.ID
			if seedErr := sectionRepo.SeedDefaults(ctx, "library", &id, sections.DefaultLibrarySectionsForType(&id, f.Type)); seedErr != nil {
				slog.Warn("seed library section defaults", "library_id", id, "error", seedErr)
			}
		}
	}
	activityPM := partman.NewManager(pool, "activity_log", partman.Weekly, 2)
	if err := activityPM.EnsureFuturePartitions(appCtx); err != nil {
		log.Fatalf("ensure activity log partitions: %v", err)
	}
	var activityWriter activitylog.Writer
	activityConsumer := activitylog.NewConsumer(pool, nil, logStreamHub)

	if cfg.Redis.URL != "" {
		actRedisClient, actRedisErr := cache.NewRedisClient(cfg.Redis)
		if actRedisErr == nil && actRedisClient != nil {
			activityWriter = activitylog.NewRedisWriter(actRedisClient)
			activityConsumer = activitylog.NewConsumer(pool, actRedisClient, logStreamHub)
			go activityConsumer.RunRedis(appCtx)
			defer actRedisClient.Close()
		}
	}

	if activityWriter == nil {
		memWriter := activitylog.NewMemoryWriter(10000)
		activityWriter = memWriter
		go activityConsumer.RunMemory(appCtx, memWriter.Chan())
	}
	deps.ActivityLogWriter = activityWriter
	deps.ActivityLogRepo = activitylog.NewRepo(pool)
	deps.NodeID = nodeID

	// Create refresh worker early so the task manager can use it for FindCandidates.
	var refreshWorker *worker.RefreshWorker
	var personRefreshWorker *worker.PersonRefreshWorker
	if needsWorkers && deps.DB != nil {
		refreshWorker = worker.NewRefreshWorker(deps.DB)
		if deps.PersonRefreshQueue != nil {
			personRefreshWorker, _ = deps.PersonRefreshQueue.(*worker.PersonRefreshWorker)
		}
	}

	// Construct collection service for both the router and the collection sync scheduler.
	var collectionSyncScheduler *catalog.CollectionSyncScheduler
	var userCollectionScheduler *usercollections.Scheduler
	var trendingRefresher *sections.TrendingRefresher
	if needsWorkers && deps.DB != nil {
		collectionRepo := catalog.NewLibraryCollectionRepository(deps.DB)
		collItemRepo := catalog.NewItemRepository(deps.DB)
		libraryItemRepo := catalog.NewLibraryItemRepository(deps.DB)
		collectionService := catalog.NewLibraryCollectionService(collectionRepo, collItemRepo, libraryItemRepo, nil)
		collectionService.TMDBCollections = api.NewTMDBCollectionFetcher(cfg.TMDBAPIKey)
		deps.CollectionService = collectionService
		collectionSyncScheduler = catalog.NewCollectionSyncScheduler(collectionRepo, collectionService, slog.Default())

		// The trending refresher reuses the section repo (to find used source/
		// window combos), a snapshot repo, an item repo (external-ID matching),
		// and the TMDB fetcher. The Trakt fetcher needs settingsRepo and is
		// propagated onto deps.TrendingRefresher later in router.go.
		trendingRefresher = sections.NewTrendingRefresher(
			sectionRepo,
			sections.NewTrendingSnapshotRepository(pool),
			catalog.NewItemRepository(deps.DB),
			collectionService.TMDBCollections,
			collectionService.TraktCollections,
		)
		deps.TrendingRefresher = trendingRefresher

		if deps.UserStoreProvider != nil {
			userSync := usercollections.NewService(deps.UserStoreProvider, collItemRepo, libraryItemRepo, nil, slog.Default())
			userSync.TMDBCollections = collectionService.TMDBCollections
			// Trakt fetchers are wired in router.go (they need settingsRepo);
			// router.go propagates them onto userSync once configured.
			userCollectionScheduler = usercollections.NewScheduler(deps.DB, userSync, slog.Default())
			deps.UserCollectionSync = userSync
			deps.UserCollectionScheduler = userCollectionScheduler
			deps.MDBListClient = mdblist.NewClient(cfg.MDBListAPIKey, nil)
		}
	}

	// Wire up task manager for admin task API.
	if needsWorkers && deps.DB != nil {
		triggerRepo := taskrepository.NewPgTriggerRepository(deps.DB)
		historyRepo := taskrepository.NewPgExecutionRepository(deps.DB)
		taskMgr := taskmanager.New(triggerRepo, historyRepo, triggers.New, slog.Default())
		if deps.EventsHub != nil {
			taskMgr.AddObserver(evt.NewTaskObserver(deps.EventsHub))
		}

		if deps.FolderRepo != nil && deps.LibraryScanQueue != nil {
			taskMgr.Register(tasks.NewScanLibrariesTask(deps.FolderRepo, deps.LibraryScanQueue, deps.EventBus))
		}
		if deps.IntroAnalyzer != nil {
			taskMgr.Register(tasks.NewDetectIntroMarkersTask(deps.IntroAnalyzer, settingsRepo))
		}
		if chapterBackfiller, ok := deps.ChapterThumbnailQueuer.(*chapterthumbs.Service); ok {
			taskMgr.Register(tasks.NewChapterThumbnailBackfillTask(chapterBackfiller, 25))
		}
		taskMgr.Register(tasks.NewActivityLogCleanupTask(deps.DB, settingsRepo, activityPM))
		taskMgr.Register(tasks.NewOperationalLogCleanupTask(deps.DB, settingsRepo, opsPM))
		if matchWorker != nil {
			taskMgr.Register(tasks.NewMatchMediaTask(matchWorker))
		}
		if refreshWorker != nil && metadataService != nil {
			taskMgr.Register(tasks.NewRefreshMetadataTask(refreshWorker, metadataService))
		}
		if pluginAutoUpdater != nil {
			taskMgr.Register(tasks.NewCheckPluginUpdatesTask(pluginAutoUpdater))
		}
		if collectionSyncScheduler != nil {
			taskMgr.Register(tasks.NewSyncCollectionsTask(collectionSyncScheduler))
		}
		if trendingRefresher != nil {
			taskMgr.Register(tasks.NewRefreshTrendingDiscoverTask(trendingRefresher))
		}
		if userCollectionScheduler != nil {
			taskMgr.Register(tasks.NewSyncUserCollectionsTask(userCollectionScheduler))
		}
		if watchProviderService != nil {
			taskMgr.Register(tasks.NewSyncWatchProvidersTask(watchProviderService))
		}
		requestReconcileSvc := mediarequests.NewService(
			mediarequests.NewRepository(deps.DB),
			nil,
			mediarequests.NewCatalogPresence(
				catalog.NewItemRepository(deps.DB),
				catalog.NewProviderIDRepository(deps.DB),
			),
		)
		requestReconcileSvc.SetSecretResolver(settingsRepo)
		requestReconcileSvc.SetFulfillmentAdapters(radarr.NewClient(nil), sonarr.NewClient(nil))
		if userStoreProvider != nil {
			reconcileResolver := access.NewResolver(
				auth.NewUserRepository(deps.DB),
				userStoreProvider,
				access.NewProfileTokenService(cfg.Auth.JWTSecret, 0),
			)
			requestReconcileSvc.SetEntitlementResolver(mediarequests.NewAccessEntitlements(reconcileResolver))
		}
		taskMgr.Register(tasks.NewReconcileRequestsTask(requestReconcileSvc, 100))
		if deps.FolderRepo != nil && deps.LibraryScanQueue != nil && pluginService != nil && pluginInstallationStore != nil {
			autoscanRepo := autoscan.NewRepository(deps.DB)
			autoscanSvc := api.BuildAutoscanService(
				autoscanRepo,
				pluginService,
				pluginInstallationStore,
				mediarequests.NewRepository(deps.DB),
				settingsRepo,
				deps.FolderRepo,
				deps.LibraryScanQueue,
				deps.RedisClient,
			)
			// The poll task's default interval seeds the schedule from the stored
			// settings (DefaultPollIntervalSeconds); per-cycle gating still runs
			// off the live settings inside PollOnce. Seed in MILLISECONDS as
			// seconds*1000 — the SAME computation HandleUpdateSettings uses to
			// reschedule — so startup and reschedule agree for sub-minute and
			// non-60-multiple intervals (the old seconds/60 minutes path diverged).
			var intervalMs int64 = 10 * 60 * 1000
			if settings, serr := autoscanRepo.GetSettings(appCtx); serr == nil && settings.DefaultPollIntervalSeconds > 0 {
				intervalMs = int64(settings.DefaultPollIntervalSeconds) * 1000
			}
			taskMgr.Register(tasks.NewAutoscanPollTask(autoscanSvc, intervalMs))
		}
		reconcileProviderIDRepo := catalog.NewProviderIDRepository(deps.DB)
		reconcileEpisodeRepo := catalog.NewEpisodeRepository(deps.DB)
		historyResolver := watchstate.NewStableIdentityResolver(nil, reconcileEpisodeRepo, reconcileProviderIDRepo)
		historyReconciler := watchstate.NewHistoryReconciler(deps.DB, historyResolver)
		taskMgr.Register(tasks.NewRepairProviderIDIntegrityTask(metadata.NewProviderIDIntegrityRepairer(deps.DB), historyReconciler))
		taskMgr.Register(tasks.NewReconcileWatchHistoryTask(historyReconciler))
		taskMgr.Register(tasks.NewSyncPodcastFeedsTask(podcastfeed.New(), podcastfeed.NewDBStore(deps.DB)))
		if audiobooksEnabled && audiobookEnricher != nil {
			taskMgr.Register(tasks.NewSyncAudiobookMetadataTask(audiobookEnricher))
		}
		if pluginInstallationStore != nil && pluginRuntimeConfigStore != nil && pluginService != nil {
			pluginTasks, err := plugins.NewTaskRegistryWithTypedResolver(pluginInstallationStore, pluginRuntimeConfigStore, pluginService).Tasks(appCtx)
			if err != nil {
				log.Fatalf("plugin task registry: %v", err)
			}
			for _, pluginTask := range pluginTasks {
				taskMgr.Register(pluginTask)
			}
		}

		taskMgr.Start(appCtx)
		defer taskMgr.Stop()
		deps.TaskManager = taskMgr
		slog.Info("task manager started")
	}

	// Build the ABS-compatible REST + Socket.io handler when a DB pool is
	// available. Routes are mounted at the root level by NewRouter (not under
	// /api/v1/) so ABS clients resolve /login, /api/*, /abs/api/*, and
	// /abs/socket.io/* without path prefix hacks.
	if audiobooksEnabled && deps.DB != nil {
		absUserRepo := auth.NewUserRepository(deps.DB)
		absSessionRepo := auth.NewSessionRepository(deps.DB)
		absJWTService := auth.NewJWTService(
			cfg.Auth.JWTSecret,
			cfg.Auth.AccessTokenExpiry,
			cfg.Auth.RefreshTokenExpiry,
		)
		absAuthSvc := auth.NewService(
			auth.NewLocalProvider(absUserRepo, absSessionRepo),
			absJWTService,
			absSessionRepo,
			absUserRepo,
			nil, // invite codes: not needed for ABS compat
			nil, // settings: not needed here
			nil, // user store: not needed here
		)
		absItemRepo := catalog.NewItemRepository(deps.DB)
		absEpisodeRepo := catalog.NewEpisodeRepository(deps.DB)
		absSeasonRepo := catalog.NewSeasonRepository(deps.DB)
		absPersonRepo := catalog.NewPersonRepository(deps.DB)
		var absFileFetcher catalog.FileVersionFetcher
		if deps.FileRepo != nil {
			absFileFetcher = deps.FileRepo
		}
		absDetailSvc := catalog.NewDetailService(absItemRepo, absEpisodeRepo, absSeasonRepo, absPersonRepo, absFileFetcher)
		if deps.ImageResolver != nil {
			absDetailSvc.SetImageResolver(deps.ImageResolver)
		}
		absHDeps := audiobooks.ABSHandlerDeps{
			Pool:     deps.DB,
			Items:    absItemRepo,
			Files:    deps.FileRepo,
			Settings: settingsRepo,
			Auth: &audiobooks.SiloCredValidator{
				Auth: absAuthSvc,
				Pool: deps.DB,
			},
			AccessResolver: audiobooks.NewABSAccessResolver(absUserRepo, userStoreProvider),
			Recs:           recommendations.NewRepo(deps.DB),
			Detail:         absDetailSvc,
		}
		absH := audiobooksService.BuildABSHandler(absHDeps)
		deps.ABSHandler = absH
	}
	_ = audiobooksService

	if deps.DB != nil && pluginInstallationStore != nil && pluginRuntimeConfigStore != nil && deps.PluginService != nil {
		userRepo := auth.NewUserRepository(deps.DB)
		sessionRepo := auth.NewSessionRepository(deps.DB)
		authBindings, err := pluginRuntimeConfigStore.ListAuthBindings(appCtx)
		if err != nil {
			log.Fatalf("list plugin auth bindings: %v", err)
		}
		for _, binding := range authBindings {
			if binding == nil || !binding.Enabled {
				continue
			}
			installation, err := pluginInstallationStore.GetByID(appCtx, binding.InstallationID)
			if err != nil {
				log.Fatalf("load plugin auth installation %d: %v", binding.InstallationID, err)
			}
			if !installation.Enabled {
				continue
			}
			displayName := binding.CapabilityID
			mode := "credentials"
			iconURL := ""
			capabilities, err := pluginInstallationStore.ListCapabilities(appCtx, binding.InstallationID)
			if err == nil {
				for _, capability := range capabilities {
					if capability != nil && capability.Type == "auth_provider.v1" && capability.ID == binding.CapabilityID {
						if name, ok := capability.Metadata["display_name"].(string); ok && strings.TrimSpace(name) != "" {
							displayName = name
						}
						// auth_modes ["oauth2"] flips the login button into
						// an OAuth-style "Sign in with X" path. Mode is "oauth"
						// when oauth2 is the only declared mode; "credentials"
						// when password is supported alongside or alone.
						if rawModes, ok := capability.Metadata["auth_modes"].([]any); ok {
							hasPassword := false
							hasOAuth := false
							for _, m := range rawModes {
								switch m {
								case "password":
									hasPassword = true
								case "oauth2":
									hasOAuth = true
								}
							}
							if hasOAuth && !hasPassword {
								mode = "oauth"
							}
						}
						if url, ok := capability.Metadata["icon_url"].(string); ok {
							iconURL = url
						}
						break
					}
				}
			}

			// Generic OIDC and similar multi-instance plugins ship one binary
			// but install once per IdP. Their admin SPA writes display_name
			// + icon_url_path to runtime config so each install renders its
			// own brand on the login page. Manifest values are the fallback.
			if runtimeConfigs, err := pluginRuntimeConfigStore.ListGlobalConfigs(appCtx, binding.InstallationID); err == nil {
				for _, rc := range runtimeConfigs {
					switch rc.Key {
					case "display_name":
						if v, ok := rc.Value["value"].(string); ok && strings.TrimSpace(v) != "" {
							displayName = v
						}
					case "icon_url_path":
						if v, ok := rc.Value["value"].(string); ok && strings.TrimSpace(v) != "" {
							iconURL = fmt.Sprintf("/api/v1/plugins/%d/assets/%s", binding.InstallationID, strings.TrimLeft(v, "/"))
						}
					}
				}
			}

			deps.AuthProviders = append(deps.AuthProviders, auth.RegisteredProvider{
				Info: auth.LoginProviderInfo{
					ID:             fmt.Sprintf("plugin:%d:%s", binding.InstallationID, binding.CapabilityID),
					DisplayName:    displayName,
					Mode:           mode,
					Default:        binding.DefaultLogin,
					IconURL:        iconURL,
					InstallationID: binding.InstallationID,
				},
				Provider: auth.NewPluginProvider(
					auth.PluginProviderConfig{
						InstallationID: binding.InstallationID,
						CapabilityID:   binding.CapabilityID,
						DisplayName:    displayName,
						AutoProvision:  binding.AutoProvision,
					},
					sessionRepo,
					userRepo,
					deps.DB,
					deps.PluginService,
				),
			})
		}
	}

	// Step 7: Build HTTP router with all dependencies.
	// compatServer is populated after the compat server is constructed below;
	// the closure captures the pointer so revocation calls reach the live instance.
	var compatServer *jellycompat.Server
	deps.OnUserSessionsRevoked = func(ctx context.Context, userID int) {
		if compatServer != nil {
			compatServer.SessionStore().DeleteByUserID(userID)
		}
	}

	distFS, fsErr := fs.Sub(siloweb.DistFS, "dist")
	if fsErr != nil {
		log.Fatalf("failed to create frontend FS: %v", fsErr)
	}
	deps.FrontendFS = distFS
	server.WebDistFS = distFS
	router := api.NewRouter(deps)

	// Step 8: Expose Prometheus metrics endpoint (not behind auth).
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsMux.Handle("/api/", router)
	// ABS-compat is NOT mounted on the main listener — see the "ABS compat
	// listener" block below. It binds its own port so the discovery probes
	// (/ping, /healthcheck, /status, /init, /login, /socket.io) own the URL
	// space without collision with silo's SPA fallback. Mirrors how the
	// Jellyfin compat server is set up at :8096.
	metricsMux.Handle("/", server.FrontendHandler())

	// Step 9: Start background workers (if needed).
	var sessionCleaner *worker.SessionCleaner
	var adminJobRunner *adminjob.Runner

	if needsWorkers && deps.DB != nil {
		if reconciler == nil {
			log.Fatal("reconciler must be initialized before starting workers")
		}
		reconciler.Start()
		defer reconciler.Stop()

		if heartbeatWriter != nil {
			heartbeatWriter.Start()
			defer heartbeatWriter.Stop()
		}

		// RefreshWorker is kept as a RefreshCandidateFinder for the task manager's
		// RefreshMetadataTask but no longer runs its own background loop.
		// Scanning is handled exclusively by the task manager's ScanLibrariesTask.
		if personRefreshWorker != nil {
			personRefreshWorker.Start()
			defer personRefreshWorker.Stop()
		}

		sessionCleaner = worker.NewSessionCleaner(deps.DB, cfg.UserDB.StaleGraceSeconds)
		sessionCleaner.EventBus = deps.EventBus
		sessionCleaner.EventsHub = deps.EventsHub
		sessionCleaner.Start()
		defer sessionCleaner.Stop()

		var templateBundleApplyExecutor interface {
			ExecuteTemplateBundleApply(context.Context, adminjob.TemplateBundleApplyRequest, func(int, int, string)) (any, error)
		}
		if deps.CollectionService != nil {
			collectionRepo := catalog.NewLibraryCollectionRepository(deps.DB)
			itemRepo := catalog.NewItemRepository(deps.DB)
			collectionHandler := handlers.NewLibraryCollectionHandler(
				collectionRepo,
				deps.CollectionService,
				itemRepo,
				4*time.Hour,
				nil,
				deps.S3Public,
			)
			collectionHandler.FrontendFS = deps.FrontendFS
			collectionHandler.SectionRepo = sectionRepo
			collectionHandler.FolderRepo = deps.FolderRepo
			if collectionHandler.FolderRepo == nil {
				collectionHandler.FolderRepo = catalog.NewFolderRepository(deps.DB)
			}
			templateBundleApplyExecutor = collectionHandler
		}

		adminJobRunner = adminjob.NewRunner(
			adminjob.NewRepository(deps.DB),
			catalogseed.NewService(deps.DB, catalog.NewPersonRepository(deps.DB), recommendations.NewRepo(deps.DB)),
			deps.S3Private,
			itemRefreshExecutor,
			libraryRefreshExecutor,
			adminjob.NewLibraryDeleteExecutor(deps.FolderRepo, sectionRepo),
			adminjob.NewImageCacheCleanupExecutor(deps.S3Public),
			templateBundleApplyExecutor,
			deps.RealtimeHub,
		)
		adminJobRunner.SetCancelRegistry(adminJobCancelRegistry)
		adminJobRunner.Start()
		defer adminJobRunner.Stop()

		// Start recommendation worker if enabled (reuse worker created above).
		if recWorker != nil {
			recWorker.Start()
			defer recWorker.Stop()

			// Check if this is first run (no embeddings yet).
			embCount, _ := recommendations.NewRepo(deps.DB).EmbeddingCount(appCtx)
			if embCount == 0 {
				slog.Info("first run detected, triggering initial embedding")
				recWorker.RunEmbeddingsNow()
			}
		}

		slog.Info("background workers started")
	}

	// Step 10: Create and start the HTTP server.
	srv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      metricsMux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	var compatSrv *http.Server
	if (mode == "integrated" || mode == "api") && cfg.JellyfinCompat.Listen != "" {
		compatDeps := jellycompat.Dependencies{
			Config:           cfg,
			DB:               deps.DB,
			ClientIPResolver: ipResolver,
			ProxyPool:        deps.ProxyPool,
			TranscodePool:    deps.TranscodePool,
			JWTSecret:        cfg.Auth.JWTSecret,
			RecWorker:        recWorker,
		}

		// Wire direct dependencies when DB is available.
		if deps.DB != nil {
			browseRepo := catalog.NewBrowseRepository(deps.DB)
			itemRepo := catalog.NewItemRepository(deps.DB)
			seasonRepo := catalog.NewSeasonRepository(deps.DB)
			episodeRepo := catalog.NewEpisodeRepository(deps.DB)
			providerIDRepo := catalog.NewProviderIDRepository(deps.DB)
			personRepo := catalog.NewPersonRepository(deps.DB)
			folderRepo := deps.FolderRepo

			var fileFetcher catalog.FileVersionFetcher
			if deps.FileRepo != nil {
				fileFetcher = deps.FileRepo
			}

			detailSvc := catalog.NewDetailService(itemRepo, episodeRepo, seasonRepo, personRepo, fileFetcher)
			detailSvc.SetFolderRepository(folderRepo)
			detailSvc.SetGroupClaimRepository(catalog.NewGroupClaimRepository(deps.DB))
			detailSvc.SetProbeEnsurer(deps.ProbeEnsurer)
			detailSvc.SetChapterThumbnailQueuer(deps.ChapterThumbnailQueuer)
			if deps.ImageResolver != nil {
				detailSvc.SetImageResolver(deps.ImageResolver)
			}

			compatDeps.BrowseRepo = browseRepo
			compatDeps.ItemRepo = itemRepo
			compatDeps.SeasonRepo = seasonRepo
			compatDeps.EpisodeRepo = episodeRepo
			compatDeps.ProviderIDRepo = providerIDRepo
			compatDeps.DetailSvc = detailSvc
			compatDeps.FolderRepo = folderRepo
			compatDeps.SessionMgr = sessionMgr
			compatDeps.UserStoreProvider = userStoreProvider
			compatDeps.SettingsRepo = settingsRepo
			compatDeps.PersonRepo = personRepo

			if deps.S3Public != nil {
				compatDeps.PosterPresigner = deps.S3Public
				compatDeps.S3Client = deps.S3Public
				compatDeps.S3Bucket = deps.S3Public.Bucket()
			}

			if deps.FileRepo != nil {
				compatDeps.FileResolver = deps.FileRepo
			}

			compatDeps.SubtitleRepo = subtitles.NewPgRepository(deps.DB)

			// Construct auth service for jellycompat login.
			userRepo := auth.NewUserRepository(deps.DB)
			compatDeps.APIKeyValidator = auth.NewAPIKeyRepository(deps.DB)
			compatDeps.APIKeyUserLoader = userRepo
			compatDeps.ScanQueue = deps.LibraryScanQueue
			sessionRepo := auth.NewSessionRepository(deps.DB)
			jwtService := auth.NewJWTService(
				cfg.Auth.JWTSecret,
				cfg.Auth.AccessTokenExpiry,
				cfg.Auth.RefreshTokenExpiry,
			)
			provider := auth.NewLocalProvider(userRepo, sessionRepo)
			compatDeps.AuthService = auth.NewService(provider, jwtService, sessionRepo, userRepo, nil, nil, nil)

			// Access filter resolver for profile-scoped library access.
			compatDeps.AccessFilterFn = func(ctx context.Context, userID int, profileID string) catalog.AccessFilter {
				if userStoreProvider == nil {
					return catalog.AccessFilter{}
				}
				store, err := userStoreProvider.ForUser(ctx, userID)
				if err != nil {
					return catalog.AccessFilter{}
				}
				profile, err := store.GetProfile(ctx, profileID)
				if err != nil || profile == nil {
					return catalog.AccessFilter{}
				}
				filter := catalog.AccessFilter{
					MaxContentRating: profile.MaxContentRating,
				}
				if profile.LibraryRestrictionsEnabled && len(profile.AllowedLibraryIDs) > 0 {
					filter.AllowedLibraryIDs = profile.AllowedLibraryIDs
				}
				// Apply user-disabled library IDs.
				if raw, err := store.GetSetting(ctx, "disabled_library_ids"); err == nil && raw != "" {
					var disabled []int
					if json.Unmarshal([]byte(raw), &disabled) == nil && len(disabled) > 0 {
						if filter.AllowedLibraryIDs != nil {
							filtered := make([]int, 0, len(filter.AllowedLibraryIDs))
							disSet := make(map[int]struct{}, len(disabled))
							for _, id := range disabled {
								disSet[id] = struct{}{}
							}
							for _, id := range filter.AllowedLibraryIDs {
								if _, ok := disSet[id]; !ok {
									filtered = append(filtered, id)
								}
							}
							filter.AllowedLibraryIDs = filtered
						} else {
							filter.DisabledLibraryIDs = disabled
						}
					}
				}
				return filter
			}
		}

		compat := jellycompat.NewServerWithDependencies(compatDeps)
		compatServer = compat
		compat.StartBackgroundTasks(context.Background())
		compatSrv = compat.HTTPServer()
		compatSrv.ReadTimeout = 30 * time.Second
		compatSrv.WriteTimeout = 0
		compatSrv.IdleTimeout = 120 * time.Second
	}

	// ABS-compat listener — dedicated http.Server bound to its own port
	// (default :13378) that hosts the Audiobookshelf-compatible API.
	// Mirrors the Jellyfin compat layout above. The ABS handler mounts
	// onto a fresh chi router here so /ping, /healthcheck, /status, /login,
	// /socket.io, etc. own the URL space at the root — no SPA fallback,
	// no collision with silo's /api/v1.
	var absSrv *http.Server
	if (mode == "integrated" || mode == "api") && deps.ABSHandler != nil && cfg.AudiobookshelfCompat.Listen != "" {
		absRouter := chi.NewRouter()
		absRouter.Use(chimiddleware.Recoverer)
		absRouter.Use(chimiddleware.Compress(5))
		deps.ABSHandler.Mount(absRouter)
		absSrv = &http.Server{
			Addr:              cfg.AudiobookshelfCompat.Listen,
			Handler:           absRouter,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      0,
			IdleTimeout:       120 * time.Second,
		}
	}

	// Run non-critical startup work in the background so it doesn't delay the
	// HTTP listener from accepting connections. Steps run sequentially and stop
	// early if the app context is cancelled (shutdown).
	if len(backgroundInit) > 0 {
		go func() {
			start := time.Now()
			for _, step := range backgroundInit {
				if appCtx.Err() != nil {
					return
				}
				func() {
					defer func() {
						if p := recover(); p != nil {
							slog.Error("deferred startup init step panicked; continuing",
								"panic", p, "stack", string(debug.Stack()))
						}
					}()
					step(appCtx)
				}()
			}
			slog.Info("deferred startup init completed", "steps", len(backgroundInit), "duration", time.Since(start))
		}()
	}

	errCh := make(chan error, 3)
	go func() {
		slog.Info("HTTP server listening", "addr", cfg.Server.Listen)
		if listenErr := srv.ListenAndServe(); listenErr != nil && listenErr != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", listenErr)
		}
	}()
	if compatSrv != nil {
		go func() {
			slog.Info("Jellyfin compat server listening", "addr", compatSrv.Addr)
			if listenErr := compatSrv.ListenAndServe(); listenErr != nil && listenErr != http.ErrServerClosed {
				errCh <- fmt.Errorf("jellyfin compat server error: %w", listenErr)
			}
		}()
	}
	if absSrv != nil {
		go func() {
			slog.Info("ABS compat server listening", "addr", absSrv.Addr)
			if listenErr := absSrv.ListenAndServe(); listenErr != nil && listenErr != http.ErrServerClosed {
				errCh <- fmt.Errorf("abs compat server error: %w", listenErr)
			}
		}()
	}

	// Step 11: Wait for termination signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		appCancel()
		slog.Info("received signal, shutting down", "signal", sig)
	case serverErr := <-errCh:
		appCancel()
		slog.Error("server error, shutting down", "error", serverErr)
	}

	// Step 12: Graceful shutdown sequence.
	slog.Info("beginning graceful shutdown")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	// 1. Stop accepting new requests.
	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		slog.Error("HTTP shutdown error", "error", shutdownErr)
	}
	if compatSrv != nil {
		if shutdownErr := compatSrv.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.Error("jellyfin compat shutdown error", "error", shutdownErr)
		}
	}
	if absSrv != nil {
		if shutdownErr := absSrv.Shutdown(shutdownCtx); shutdownErr != nil {
			slog.Error("abs compat shutdown error", "error", shutdownErr)
		}
	}

	// 2. Clean up stale sessions.
	if sessionCleaner != nil {
		cleaned, cleanErr := sessionCleaner.CleanStale(shutdownCtx)
		if cleanErr != nil {
			slog.Error("stale session cleanup error", "error", cleanErr)
		} else if cleaned > 0 {
			slog.Info("cleaned stale sessions", "count", cleaned)
		}
	}

	// 2b. Remove this node's heartbeat and sessions from shared state.
	if heartbeatWriter != nil {
		if err := heartbeatWriter.CleanupSelf(shutdownCtx); err != nil {
			slog.Error("heartbeat cleanup error", "error", err)
		}
	}

	// 3. Close user store provider.
	if userStoreProvider != nil {
		if closeErr := userStoreProvider.Close(); closeErr != nil {
			slog.Error("user store provider close error", "error", closeErr)
		}
	}

	// 4. (match worker is now managed by the task manager — no separate cancel needed)

	// Suppress unused variable warnings for workers used only in deferred calls.
	_ = reconciler
	_ = heartbeatWriter
	_ = refreshWorker
	_ = adminJobRunner

	slog.Info("server stopped")
}

// startStandaloneServer runs a standalone HTTP server for proxy/transcode modes.
// It listens on the given address, handles graceful shutdown on SIGTERM/SIGINT.
func startStandaloneServer(addr string, handler http.Handler) {
	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // no timeout for long streams
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("HTTP server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server error: %w", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig)
	case serverErr := <-errCh:
		slog.Error("server error, shutting down", "error", serverErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP shutdown error", "error", err)
	}
	slog.Info("server stopped")
}

// newS3ClientIfConfigured creates an S3 client only if the bucket name is
// configured. Returns nil if the bucket is empty (not configured).
func newS3ClientIfConfigured(cfg s3client.BucketConfig) *s3client.Client {
	if cfg.Bucket == "" {
		return nil
	}
	return s3client.NewClient(cfg)
}

func configureS3Clients(cfg *config.Config, deps *api.Dependencies) {
	if s3Public := newS3ClientIfConfigured(s3client.BucketConfig{
		Endpoint:       cfg.S3.Public.Endpoint,
		PublicEndpoint: cfg.S3.Public.ReadEndpoint,
		Region:         cfg.S3.Public.Region,
		Bucket:         cfg.S3.Public.Bucket,
		KeyPrefix:      cfg.S3.Public.KeyPrefix,
		AccessKey:      cfg.S3.Public.AccessKey,
		SecretKey:      cfg.S3.Public.SecretKey,
		PathStyle:      cfg.S3.Public.PathStyle,
		URLAuth:        cfg.S3.Public.URLAuth,
		TokenSecret:    cfg.S3.Public.TokenSecret,
		TokenParam:     cfg.S3.Public.TokenParam,
		TokenTTL:       cfg.S3.Public.TokenTTL,
	}); s3Public != nil {
		deps.S3Public = s3Public
		slog.Info("S3 public assets client configured", "bucket", s3Public.Bucket())

		// Allow browsers to fetch presigned client-facing assets directly from S3.
		// Skip for public/token auth (e.g. Cloudflare R2) where CORS is managed externally.
		if !s3Public.UsesExternalAuth() {
			corsCtx, corsCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if corsErr := s3Public.SetBucketCORS(corsCtx, s3Public.Bucket(), []string{"*"}); corsErr != nil {
				slog.Warn("failed to set CORS on public assets bucket", "error", corsErr)
			}
			corsCancel()
		}
	}

	if s3Private := newS3ClientIfConfigured(s3client.BucketConfig{
		Endpoint:  cfg.S3.Private.Endpoint,
		Region:    cfg.S3.Private.Region,
		Bucket:    cfg.S3.Private.Bucket,
		KeyPrefix: cfg.S3.Private.KeyPrefix,
		AccessKey: cfg.S3.Private.AccessKey,
		SecretKey: cfg.S3.Private.SecretKey,
		PathStyle: cfg.S3.Private.PathStyle,
	}); s3Private != nil {
		deps.S3Private = s3Private
		slog.Info("S3 private internal client configured", "bucket", s3Private.Bucket())
		if !s3Private.UsesExternalAuth() {
			corsCtx, corsCancel := context.WithTimeout(context.Background(), 10*time.Second)
			if corsErr := s3Private.SetBucketCORS(corsCtx, s3Private.Bucket(), []string{"*"}); corsErr != nil {
				slog.Warn("failed to set CORS on private assets bucket", "error", corsErr)
			}
			corsCancel()
		}
	}

	if s3UserDB := newS3ClientIfConfigured(s3client.BucketConfig{
		Endpoint:  cfg.S3.UserDB.Endpoint,
		Region:    cfg.S3.UserDB.Region,
		Bucket:    cfg.S3.UserDB.Bucket,
		KeyPrefix: cfg.S3.UserDB.KeyPrefix,
		AccessKey: cfg.S3.UserDB.AccessKey,
		SecretKey: cfg.S3.UserDB.SecretKey,
		PathStyle: cfg.S3.UserDB.PathStyle,
	}); s3UserDB != nil {
		deps.S3UserDB = s3UserDB
		slog.Info("S3 user-db client configured", "bucket", s3UserDB.Bucket())
	}
}

// mapFolderTypeToMediaType maps silo's MediaFolder.Type values
// ("movies", "series", "mixed") to the SDK's MediaType values
// ("movie", "tv", "mixed"). Unknown values map to "mixed".
func mapFolderTypeToMediaType(t string) string {
	switch t {
	case "movies":
		return "movie"
	case "series":
		return "tv"
	default:
		return "mixed"
	}
}

// audiobooksSettingsAdapter bridges catalog.ServerSettingsRepo (which
// exposes Get) to the audiobooks.SettingsReader interface (which
// requires GetString). The two signatures are identical modulo name.
type audiobooksSettingsAdapter struct {
	repo *catalog.ServerSettingsRepo
}

func (a *audiobooksSettingsAdapter) GetString(ctx context.Context, key string) (string, error) {
	return a.repo.Get(ctx, key)
}
