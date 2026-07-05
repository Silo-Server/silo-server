package config

import (
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	subtitleai "github.com/Silo-Server/silo-server/internal/subtitles/ai"
)

// stringOr returns the value from the map for the given key, or the fallback if absent/empty.
func stringOr(m map[string]string, key, fallback string) string {
	if v, ok := m[key]; ok && v != "" {
		return v
	}
	return fallback
}

func firstConfiguredString(m map[string]string, fallback string, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok && v != "" {
			return v
		}
	}
	return fallback
}

// intOr returns the parsed integer value from the map for the given key, or the fallback if absent/empty.
// Returns an error if the value is present but cannot be parsed.
func intOr(m map[string]string, key string, fallback int) (int, error) {
	v, ok := m[key]
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int for %q: %w", key, err)
	}
	return n, nil
}

// boolOr returns the parsed boolean value from the map for the given key, or the fallback if absent/empty.
// Returns an error if the value is present but cannot be parsed.
func boolOr(m map[string]string, key string, fallback bool) (bool, error) {
	v, ok := m[key]
	if !ok || v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid bool for %q: %w", key, err)
	}
	return b, nil
}

func jellyfinCompatEnabledOrLegacyDefault(m map[string]string) (bool, error) {
	if v, ok := m["jellyfin_compat.enabled"]; ok && v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("invalid bool for %q: %w", "jellyfin_compat.enabled", err)
		}
		return b, nil
	}
	return stringOr(m, "jellyfin_compat.listen", ":8096") != "", nil
}

func firstConfiguredBool(m map[string]string, fallback bool, keys ...string) (bool, error) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == "" {
			continue
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false, fmt.Errorf("invalid bool for %q: %w", key, err)
		}
		return b, nil
	}
	return fallback, nil
}

// int64Or returns the parsed int64 value from the map for the given key, or the fallback if absent/empty.
func int64Or(m map[string]string, key string, fallback int64) (int64, error) {
	v, ok := m[key]
	if !ok || v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid int64 for %q: %w", key, err)
	}
	return n, nil
}

func firstConfiguredInt(m map[string]string, fallback int, keys ...string) (int, error) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, fmt.Errorf("invalid int for %q: %w", key, err)
		}
		return n, nil
	}
	return fallback, nil
}

// floatOr returns the parsed float64 value from the map for the given key, or the fallback if absent/empty.
func floatOr(m map[string]string, key string, fallback float64) (float64, error) {
	v, ok := m[key]
	if !ok || v == "" {
		return fallback, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid float for %q: %w", key, err)
	}
	return f, nil
}

// durationOr returns the parsed duration value from the map for the given key, or the fallback if absent/empty.
// Returns an error if the value is present but cannot be parsed.
func durationOr(m map[string]string, key string, fallback time.Duration) (time.Duration, error) {
	v, ok := m[key]
	if !ok || v == "" {
		return fallback, nil
	}
	d, err := parseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %q: %w", key, err)
	}
	return d, nil
}

// defaultJellyfinCompatServerIDFromDB is the computed default server ID used when
// no server_id is set in the database settings.
var defaultJellyfinCompatServerIDFromDB = uuid.NewSHA1(
	uuid.NameSpaceURL,
	[]byte("https://silo.local/jellycompat"),
).String()

// LoadFromDB builds a Config from a map of server_settings key-value pairs.
// The map keys use dot-notation (e.g., "auth.jwt_secret"). Missing or empty
// keys fall back to the same defaults as setDefaults().
func LoadFromDB(m map[string]string) (*Config, error) {
	cfg := &Config{}

	// Server
	cfg.Server.Listen = stringOr(m, "server.listen", ":8080")
	cfg.Server.Mode = stringOr(m, "server.mode", "integrated")
	cfg.Server.LogLevel = stringOr(m, "server.log_level", "info")
	cfg.Server.LogFormat = stringOr(m, "server.log_format", "text")
	cfg.Server.LogQuiet = stringOr(m, "server.log_quiet", "")

	// Database
	cfg.Database.URL = stringOr(m, "database.url", "")
	maxConn, err := intOr(m, "database.max_connections", 20)
	if err != nil {
		return nil, err
	}
	cfg.Database.MaxConnections = maxConn

	// S3 — Public assets
	cfg.S3.Public.Endpoint = firstConfiguredString(m, "", "s3.public_endpoint", "s3.operational_endpoint")
	cfg.S3.Public.ReadEndpoint = firstConfiguredString(m, "", "s3.public_read_endpoint", "s3.operational_public_endpoint")
	cfg.S3.Public.Region = firstConfiguredString(m, "", "s3.public_region", "s3.operational_region")
	publicPathStyle, err := firstConfiguredBool(m, true, "s3.public_path_style", "s3.operational_path_style")
	if err != nil {
		return nil, err
	}
	cfg.S3.Public.PathStyle = publicPathStyle
	cfg.S3.Public.Bucket = firstConfiguredString(m, "", "s3.public_bucket", "s3.operational_bucket")
	cfg.S3.Public.KeyPrefix = firstConfiguredString(m, "", "s3.public_key_prefix", "s3.operational_key_prefix")
	cfg.S3.Public.AccessKey = firstConfiguredString(m, "", "s3.public_access_key", "s3.operational_access_key")
	cfg.S3.Public.SecretKey = firstConfiguredString(m, "", "s3.public_secret_key", "s3.operational_secret_key")
	cfg.S3.Public.URLAuth = firstConfiguredString(m, "", "s3.public_url_auth", "s3.operational_url_auth")
	cfg.S3.Public.TokenSecret = firstConfiguredString(m, "", "s3.public_token_secret", "s3.operational_token_secret")
	cfg.S3.Public.TokenParam = firstConfiguredString(m, "", "s3.public_token_param", "s3.operational_token_param")
	publicTokenTTL, err := firstConfiguredInt(m, 0, "s3.public_token_ttl", "s3.operational_token_ttl")
	if err != nil {
		return nil, err
	}
	cfg.S3.Public.TokenTTL = publicTokenTTL
	metadataPresignExpiry, err := durationOr(m, "s3.metadata_presign_expiry", 4*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.S3.MetadataPresignExpiry = metadataPresignExpiry

	// S3 — Private internal
	cfg.S3.Private.Endpoint = firstConfiguredString(m, "", "s3.private_endpoint", "s3.operational_endpoint")
	cfg.S3.Private.Region = firstConfiguredString(m, "", "s3.private_region", "s3.operational_region")
	privatePathStyle, err := firstConfiguredBool(m, true, "s3.private_path_style", "s3.operational_path_style")
	if err != nil {
		return nil, err
	}
	cfg.S3.Private.PathStyle = privatePathStyle
	cfg.S3.Private.Bucket = firstConfiguredString(m, "", "s3.private_bucket", "s3.operational_bucket")
	cfg.S3.Private.KeyPrefix = firstConfiguredString(m, "", "s3.private_key_prefix", "s3.operational_key_prefix")
	cfg.S3.Private.AccessKey = firstConfiguredString(m, "", "s3.private_access_key", "s3.operational_access_key")
	cfg.S3.Private.SecretKey = firstConfiguredString(m, "", "s3.private_secret_key", "s3.operational_secret_key")

	// S3 — User DB
	cfg.S3.UserDB.Endpoint = stringOr(m, "s3.user_db_endpoint", "")
	cfg.S3.UserDB.Region = stringOr(m, "s3.user_db_region", "")
	userDBPathStyle, err := boolOr(m, "s3.user_db_path_style", true)
	if err != nil {
		return nil, err
	}
	cfg.S3.UserDB.PathStyle = userDBPathStyle
	cfg.S3.UserDB.Bucket = stringOr(m, "s3.user_db_bucket", "")
	cfg.S3.UserDB.KeyPrefix = stringOr(m, "s3.user_db_key_prefix", "")
	cfg.S3.UserDB.AccessKey = stringOr(m, "s3.user_db_access_key", "")
	cfg.S3.UserDB.SecretKey = stringOr(m, "s3.user_db_secret_key", "")

	// Client IP resolution ("" = clientip package defaults). Kept in the
	// config snapshot so the nodeconfig watcher hot-reloads the resolver.
	cfg.ClientIP.TrustedProxies = stringOr(m, "clientip.trusted_proxies", "")

	// TMDB collection presets (independent of metadata providers)
	cfg.TMDBAPIKey = stringOr(m, "tmdb.api_key", "")

	// MDBList list search/browse (lists themselves are public; only discovery
	// endpoints require an apikey).
	cfg.MDBListAPIKey = stringOr(m, "mdblist.api_key", "")

	// UserDB
	cfg.UserDB.Backend = stringOr(m, "userdb.backend", "postgres")
	poolMaxOpen, err := intOr(m, "userdb.pool_max_open", 500)
	if err != nil {
		return nil, err
	}
	cfg.UserDB.PoolMaxOpen = poolMaxOpen
	idleTimeout, err := durationOr(m, "userdb.idle_timeout", 12*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.UserDB.IdleTimeout = idleTimeout
	litestreamSync, err := durationOr(m, "userdb.litestream_sync", 1*time.Second)
	if err != nil {
		return nil, err
	}
	cfg.UserDB.LitestreamSync = litestreamSync
	staleGrace, err := intOr(m, "userdb.stale_grace_seconds", 120)
	if err != nil {
		return nil, err
	}
	cfg.UserDB.StaleGraceSeconds = staleGrace

	// Scanner (schedule is now managed by the task manager, not config)
	scannerWorkers, err := intOr(m, "scanner.workers", 8)
	if err != nil {
		return nil, err
	}
	cfg.Scanner.Workers = scannerWorkers
	maxConcurrentLibraries, err := intOr(m, "scanner.max_concurrent_libraries", 1)
	if err != nil {
		return nil, err
	}
	cfg.Scanner.MaxConcurrentLibraries = maxConcurrentLibraries
	maxConcurrentScoped, err := intOr(m, "scanner.max_concurrent_scoped", 2)
	if err != nil {
		return nil, err
	}
	cfg.Scanner.MaxConcurrentScoped = maxConcurrentScoped
	emptyTrash, err := boolOr(m, "scanner.empty_trash_after_scan", true)
	if err != nil {
		return nil, err
	}
	cfg.Scanner.EmptyTrashAfterScan = emptyTrash

	// Matcher
	matcherWorkers, err := intOr(m, "matcher.workers", 8)
	if err != nil {
		return nil, err
	}
	cfg.Matcher.Workers = matcherWorkers
	batchSize, err := intOr(m, "matcher.batch_size", 500)
	if err != nil {
		return nil, err
	}
	cfg.Matcher.BatchSize = batchSize
	enableTVSeriesRootQueue, err := boolOr(m, "matcher.enable_tv_series_root_queue", true)
	if err != nil {
		return nil, err
	}
	if !enableTVSeriesRootQueue {
		enableTVSeriesRootQueue, err = boolOr(m, "matcher.enable_tv_series_group_queue", false)
		if err != nil {
			return nil, err
		}
	}
	cfg.Matcher.EnableTVSeriesRootQueue = enableTVSeriesRootQueue

	// Metadata
	cacheImages, err := boolOr(m, "metadata.cache_images", false)
	if err != nil {
		return nil, err
	}
	cfg.Metadata.CacheImages = cacheImages

	// Playback
	cfg.Playback.FFmpegPath = stringOr(m, "playback.ffmpeg_path", "/usr/lib/jellyfin-ffmpeg/ffmpeg")
	cfg.Playback.TranscodeDir = stringOr(m, "playback.transcode_dir", DefaultTranscodeDir)
	cfg.Playback.HWAccel = stringOr(m, "playback.hw_accel", "auto")
	cfg.Playback.HWDevice = stringOr(m, "playback.hw_device", "")
	chapterThumbnailWorkers, err := intOr(m, "playback.chapter_thumbnail_workers", 1)
	if err != nil {
		return nil, err
	}
	cfg.Playback.ChapterThumbnailWorkers = chapterThumbnailWorkers
	cfg.Playback.ChapterThumbnailExecution = stringOr(m, "playback.chapter_thumbnail_execution", "local")
	chapterThumbnailNodeCapacity, err := intOr(m, "playback.chapter_thumbnail_node_capacity", 1)
	if err != nil {
		return nil, err
	}
	cfg.Playback.ChapterThumbnailNodeCapacity = chapterThumbnailNodeCapacity
	transcodeEnabled, err := boolOr(m, "playback.transcode_enabled", true)
	if err != nil {
		return nil, err
	}
	cfg.Playback.TranscodeEnabled = transcodeEnabled

	// Redis
	cfg.Redis.URL = stringOr(m, "redis.url", "")
	cfg.Redis.SentinelMaster = stringOr(m, "redis.sentinel_master", "")
	cfg.Redis.SentinelPassword = stringOr(m, "redis.sentinel_password", "")
	// SentinelAddresses loaded from YAML only (slice not suitable for key-value settings)

	// Rate Limiting
	rateLimitEnabled, err := boolOr(m, "ratelimit.enabled", true)
	if err != nil {
		return nil, err
	}
	cfg.RateLimit.Enabled = rateLimitEnabled
	cfg.RateLimit.Backend = stringOr(m, "ratelimit.backend", "memory")

	// Auth
	cfg.Auth.JWTSecret = stringOr(m, "auth.jwt_secret", "")
	accessTokenExpiry, err := durationOr(m, "auth.access_token_expiry", 8*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.Auth.AccessTokenExpiry = accessTokenExpiry
	refreshTokenExpiry, err := durationOr(m, "auth.refresh_token_expiry", 30*24*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.Auth.RefreshTokenExpiry = refreshTokenExpiry

	// AudiobookshelfCompat — dedicated listener for ABS client apps. Docker
	// deployments publish :13378, so listen by default unless disabled.
	absCompatEnabled, err := boolOr(m, "audiobookshelf_compat.enabled", true)
	if err != nil {
		return nil, err
	}
	if absCompatEnabled {
		cfg.AudiobookshelfCompat.Listen = ":13378"
	}

	// JellyfinCompat
	compatEnabled, err := jellyfinCompatEnabledOrLegacyDefault(m)
	if err != nil {
		return nil, err
	}
	cfg.JellyfinCompat.Enabled = compatEnabled
	cfg.JellyfinCompat.Listen = stringOr(m, "jellyfin_compat.listen", ":8096")
	cfg.JellyfinCompat.PublicURL = stringOr(m, "jellyfin_compat.public_url", "http://127.0.0.1:8096")
	cfg.JellyfinCompat.EmulatedServerVersion = stringOr(m, "jellyfin_compat.emulated_server_version", DefaultJellyfinCompatEmulatedServerVersion)
	cfg.JellyfinCompat.ServerID = stringOr(m, "jellyfin_compat.server_id", defaultJellyfinCompatServerIDFromDB)
	cfg.JellyfinCompat.ServerName = stringOr(m, "jellyfin_compat.server_name", "Silo")
	webEnabled, err := boolOr(m, "jellyfin_compat.web_enabled", true)
	if err != nil {
		return nil, err
	}
	cfg.JellyfinCompat.WebEnabled = webEnabled
	cfg.JellyfinCompat.WebVersion = stringOr(m, "jellyfin_compat.web_version", DefaultJellyfinWebVersion)
	cfg.JellyfinCompat.WebDir = stringOr(m, "jellyfin_compat.web_dir", DefaultJellyfinWebDir)
	cfg.JellyfinCompat.WebInstallDir = stringOr(m, "jellyfin_compat.web_install_dir", DefaultJellyfinWebInstallDir)
	sessionTTL, err := durationOr(m, "jellyfin_compat.session_ttl", 87600*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.JellyfinCompat.SessionTTL = sessionTTL
	playbackSessionTTL, err := durationOr(m, "jellyfin_compat.playback_session_ttl", 6*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.JellyfinCompat.PlaybackSessionTTL = playbackSessionTTL

	// Recommendations
	recsEnabled, err := boolOr(m, "recommendations.enabled", false)
	if err != nil {
		return nil, err
	}
	cfg.Recommendations.Enabled = recsEnabled
	legacyOpenAIConfigured := m["recommendations.openai_model"] != "" || m["recommendations.openai_api_key"] != "" || m["recommendations.embedding_provider"] == "openai"
	cfg.Recommendations.EmbeddingBaseURL = stringOr(m, "recommendations.embedding_base_url", func() string {
		if legacyOpenAIConfigured {
			return "https://api.openai.com"
		}
		return "http://ollama:11434"
	}())
	cfg.Recommendations.EmbeddingModel = stringOr(m, "recommendations.embedding_model", func() string {
		if model := m["recommendations.openai_model"]; model != "" {
			return model
		}
		if m["recommendations.embedding_provider"] == "openai" {
			return "text-embedding-3-small"
		}
		return "all-minilm"
	}())
	cfg.Recommendations.EmbeddingAuthToken = stringOr(m, "recommendations.embedding_auth_token", stringOr(m, "recommendations.openai_api_key", ""))
	cfg.Recommendations.EmbeddingsCron = stringOr(m, "recommendations.embeddings_cron", "0 3 * * *")
	cfg.Recommendations.EmbeddingsJobTimeout, err = durationOr(m, "recommendations.embeddings_job_timeout", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	cfg.Recommendations.TasteProfilesCron = stringOr(m, "recommendations.taste_profiles_cron", "0 4 * * *")
	cfg.Recommendations.RecommendationsCron = stringOr(m, "recommendations.recommendations_cron", "0 5 * * *")
	tasteDecayHalfLife, err := floatOr(m, "recommendations.taste_decay_half_life_days", 180)
	if err != nil {
		return nil, err
	}
	cfg.Recommendations.TasteDecayHalfLifeDays = tasteDecayHalfLife
	diversityLambda, err := floatOr(m, "recommendations.diversity_lambda", 0.7)
	if err != nil {
		return nil, err
	}
	cfg.Recommendations.DiversityLambda = diversityLambda
	cfg.Recommendations.CowatchCron = stringOr(m, "recommendations.cowatch_cron", "30 4 * * *")

	// Shared AI endpoint (subtitle translation, metadata translation, Whisper
	// ASR). The connection settings read ai.* with a fallback to the legacy
	// subtitle_ai.* rows; the legacy rows are never renamed in SQL because
	// encrypted values are GCM-bound to their setting key.
	cfg.AI.BaseURL = stringOr(m, "ai.base_url", stringOr(m, "subtitle_ai.base_url", "https://api.openai.com"))
	cfg.AI.APIKey = stringOr(m, "ai.api_key", stringOr(m, "subtitle_ai.api_key", ""))
	cfg.AI.ChatModel = stringOr(m, "ai.chat_model", stringOr(m, "subtitle_ai.chat_model", "gpt-4o-mini"))
	cfg.AI.ASRBaseURL = stringOr(m, "ai.asr_base_url", "")
	cfg.AI.ASRAPIKey = stringOr(m, "ai.asr_api_key", "")
	cfg.AI.ASRModel = stringOr(m, "ai.asr_model", "whisper-1")
	aiMaxConcurrent, err := intOr(m, "ai.max_concurrent_jobs", 0)
	if err != nil {
		return nil, err
	}
	if aiMaxConcurrent <= 0 {
		if aiMaxConcurrent, err = intOr(m, "subtitle_ai.max_concurrent_jobs", 2); err != nil {
			return nil, err
		}
	}
	cfg.AI.MaxConcurrentJobs = aiMaxConcurrent

	// Subtitle AI feature toggles and tuning.
	subtitleAIEnabled, err := boolOr(m, "subtitle_ai.enabled", false)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.Enabled = subtitleAIEnabled
	subtitleAITranscribe, err := boolOr(m, "subtitle_ai.transcribe_enabled", false)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.TranscribeEnabled = subtitleAITranscribe
	subtitleAIBatchSize, err := intOr(m, "subtitle_ai.batch_size", 40)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.BatchSize = subtitleAIBatchSize
	subtitleAIContextNeighbors, err := intOr(m, "subtitle_ai.context_neighbors", 2)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.ContextNeighbors = subtitleAIContextNeighbors
	subtitleAIChunkSeconds, err := intOr(m, "subtitle_ai.asr_chunk_seconds", 600)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.ASRChunkSeconds = subtitleAIChunkSeconds
	subtitleAILiveChunkSeconds, err := intOr(m, "subtitle_ai.live_asr_chunk_seconds", 30)
	if err != nil {
		return nil, err
	}
	cfg.SubtitleAI.LiveASRChunkSeconds = subtitleAILiveChunkSeconds
	// A bad quota row must not block startup: fall back to "no quota" /
	// the daily window with a warning instead of failing the config load.
	transcribeQuotaJobs, err := intOr(m, "subtitle_ai.transcribe_quota_jobs", 0)
	if err != nil || transcribeQuotaJobs < 0 {
		slog.Warn("invalid subtitle_ai.transcribe_quota_jobs setting; quota disabled",
			"value", m["subtitle_ai.transcribe_quota_jobs"], "error", err)
		transcribeQuotaJobs = 0
	}
	cfg.SubtitleAI.TranscribeQuotaJobs = transcribeQuotaJobs
	period := stringOr(m, "subtitle_ai.transcribe_quota_period", subtitleai.QuotaPeriodDay)
	if !subtitleai.ValidQuotaPeriod(period) {
		slog.Warn("invalid subtitle_ai.transcribe_quota_period setting; using day", "value", period)
		period = subtitleai.QuotaPeriodDay
	}
	cfg.SubtitleAI.TranscribeQuotaPeriod = period

	// Metadata AI translation feature toggles.
	metadataAIEnabled, err := boolOr(m, "metadata_ai.enabled", false)
	if err != nil {
		return nil, err
	}
	cfg.MetadataAI.Enabled = metadataAIEnabled
	switch onView := stringOr(m, "metadata_ai.on_view", "off"); onView {
	case "off", "button", "auto":
		cfg.MetadataAI.OnView = onView
	default:
		// A bad row must not block startup; the feature just stays off.
		slog.Warn("invalid metadata_ai.on_view setting; using off", "value", onView)
		cfg.MetadataAI.OnView = "off"
	}

	// Download
	downloadEnabled, err := boolOr(m, "download.enabled", false)
	if err != nil {
		return nil, err
	}
	cfg.Download.Enabled = downloadEnabled
	serverBandwidthMbps, err := int64Or(m, "download.server_bandwidth_mbps", 0)
	if err != nil {
		return nil, err
	}
	userBandwidthMbps, err := int64Or(m, "download.user_bandwidth_mbps", 0)
	if err != nil {
		return nil, err
	}
	maxConcurrent, err := intOr(m, "download.max_concurrent_per_user", 3)
	if err != nil {
		return nil, err
	}
	maxPerPeriod, err := intOr(m, "download.max_per_period", 0)
	if err != nil {
		return nil, err
	}
	periodDuration, err := durationOr(m, "download.period_duration", 24*time.Hour)
	if err != nil {
		return nil, err
	}
	// Validate download config values.
	if serverBandwidthMbps < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.server_bandwidth_mbps")
	}
	if userBandwidthMbps < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.user_bandwidth_mbps")
	}
	const maxMbps int64 = 73_786_976_294_838 // math.MaxInt64 / 125000
	if serverBandwidthMbps > maxMbps {
		return nil, fmt.Errorf("invalid value for %q: exceeds maximum", "download.server_bandwidth_mbps")
	}
	if userBandwidthMbps > maxMbps {
		return nil, fmt.Errorf("invalid value for %q: exceeds maximum", "download.user_bandwidth_mbps")
	}
	if maxConcurrent < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.max_concurrent_per_user")
	}
	if maxPerPeriod < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.max_per_period")
	}
	if maxPerPeriod > 0 && periodDuration <= 0 {
		return nil, fmt.Errorf("invalid config: %q requires a positive %q", "download.max_per_period", "download.period_duration")
	}
	cfg.Download.ServerBandwidthBPS = serverBandwidthMbps * 125000 // Mbps → bytes/sec
	cfg.Download.UserBandwidthBPS = userBandwidthMbps * 125000     // Mbps → bytes/sec
	cfg.Download.MaxConcurrentPerUser = maxConcurrent
	cfg.Download.MaxPerPeriod = maxPerPeriod
	cfg.Download.PeriodDuration = periodDuration

	// Downloads v2 (offline sync). Default-off; the prepare-to-file pipeline that
	// consumes these ships in Phase 3.
	downloadTranscodeEnabled, err := boolOr(m, "download.transcode_enabled", false)
	if err != nil {
		return nil, err
	}
	maxConcurrentPrepares, err := intOr(m, "download.max_concurrent_prepares", 2)
	if err != nil {
		return nil, err
	}
	artifactMaxBytes, err := int64Or(m, "download.artifact_max_bytes", 0)
	if err != nil {
		return nil, err
	}
	if maxConcurrentPrepares < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.max_concurrent_prepares")
	}
	if artifactMaxBytes < 0 {
		return nil, fmt.Errorf("invalid value for %q: must be non-negative", "download.artifact_max_bytes")
	}
	cfg.Download.TranscodeEnabled = downloadTranscodeEnabled
	cfg.Download.ArtifactDir = stringOr(m, "download.artifact_dir", "")
	cfg.Download.MaxConcurrentPrepares = maxConcurrentPrepares
	cfg.Download.ArtifactMaxBytes = artifactMaxBytes

	return cfg, nil
}
