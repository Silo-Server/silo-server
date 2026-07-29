package config

import (
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"strconv"
	"strings"

	redisv9 "github.com/redis/go-redis/v9"
	"github.com/robfig/cron/v3"
)

const cloudflareURLMode = "cloudflare_token"

// adminSettingDefaults is the effective value shown by the Admin UI when no
// row exists in server_settings. Keep these values aligned with the runtime
// readers that own each setting. The UI must never invent a second set of
// defaults: an untouched form should describe the behavior the server is
// actually running.
var adminSettingDefaults = map[string]string{
	"auth.access_token_expiry":  "8h",
	"auth.refresh_token_expiry": "30d",
	"server.log_level":          "info",
	"server.log_quiet":          "",
	"branding.server_name":      "Silo",
	"branding.login_subtitle":   "Sign in with an existing account.",
	"clientip.trusted_proxies":  "10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, ::1/128",
	"theme.catalog_url":         DefaultThemeCatalogURL,

	"database.max_connections": "20",
	"s3.public_path_style":     "true",
	"s3.public_url_auth":       "presigned",
	"s3.public_token_param":    "verify",
	"s3.public_token_ttl":      "10800",
	"s3.private_path_style":    "true",
	"s3.user_db_path_style":    "true",
	"userdb.backend":           "postgres",
	"userdb.pool_max_open":     "500",
	"userdb.idle_timeout":      "12h",

	"scanner.workers":       "8",
	"matcher.workers":       "8",
	"matcher.batch_size":    "500",
	"metadata.cache_images": "false",
	"markers.mode":          "local",
	"markers.lazy_playback": "false",

	"playback.ffmpeg_path":                     "/usr/lib/jellyfin-ffmpeg/ffmpeg",
	"playback.transcode_dir":                   DefaultTranscodeDir,
	"playback.hw_accel":                        "auto",
	"playback.transcode_enabled":               "true",
	"playback.local_transcode_fallback":        "true",
	"playback.chapter_thumbnail_workers":       "1",
	"playback.chapter_thumbnail_execution":     "local",
	"playback.chapter_thumbnail_node_capacity": "1",
	"playback.chapter_thumbnail_hdr_policy":    "best_effort",
	"playback.watched_threshold":               "90",
	"playback.min_resume_threshold":            "5",
	"allow_4k_transcode":                       "false",
	"enable_transcode_throttle":                "false",
	"transcode_throttle_seconds":               "300",

	"audiobookshelf_compat.enabled":           "true",
	"jellyfin_compat.enabled":                 "true",
	"jellyfin_compat.public_url":              "http://127.0.0.1:8096",
	"jellyfin_compat.emulated_server_version": DefaultJellyfinCompatEmulatedServerVersion,
	"jellyfin_compat.server_name":             "Silo",
	"jellyfin_compat.web_enabled":             "true",
	"jellyfin_compat.web_version":             DefaultJellyfinWebVersion,
	"jellyfin_compat.web_install_dir":         DefaultJellyfinWebInstallDir,
	"jellyfin_compat.session_ttl":             "87600h",
	"jellyfin_compat.playback_session_ttl":    "6h",

	"recommendations.enabled":                    "false",
	"recommendations.embedding_base_url":         "http://ollama:11434",
	"recommendations.embedding_model":            "all-minilm",
	"recommendations.embeddings_cron":            "0 3 * * *",
	"recommendations.taste_profiles_cron":        "0 4 * * *",
	"recommendations.cowatch_cron":               "30 4 * * *",
	"recommendations.recommendations_cron":       "0 5 * * *",
	"recommendations.taste_decay_half_life_days": "180",
	"recommendations.diversity_lambda":           "0.7",

	"ai.base_url":                         "https://api.openai.com",
	"ai.chat_model":                       "gpt-4o-mini",
	"ai.asr_model":                        "whisper-1",
	"ai.max_concurrent_jobs":              "2",
	"subtitle_ai.enabled":                 "false",
	"subtitle_ai.transcribe_enabled":      "false",
	"subtitle_ai.batch_size":              "40",
	"subtitle_ai.context_neighbors":       "2",
	"subtitle_ai.asr_chunk_seconds":       "600",
	"subtitle_ai.transcribe_quota_jobs":   "0",
	"subtitle_ai.transcribe_quota_period": "day",
	"metadata_ai.enabled":                 "false",
	"metadata_ai.on_view":                 "off",

	"download.enabled":                 "false",
	"download.server_bandwidth_mbps":   "0",
	"download.user_bandwidth_mbps":     "0",
	"download.max_concurrent_per_user": "3",
	"download.max_per_period":          "0",
	"download.period_duration":         "24h",
	"download.transcode_enabled":       "false",
	"download.max_concurrent_prepares": "2",
	"download.artifact_max_bytes":      "0",

	"policy.decision_log_verbosity":         "digest",
	"policy.decision_log_scope_sample_rate": "50",
	"policy.decision_log_retention_days":    "14",

	"email.enabled":       "false",
	"email.smtp_port":     "587",
	"email.smtp_security": "starttls",
	"email.from_name":     "Silo",

	"notifications.release_events_enabled":                     "true",
	"notifications.fanout_enabled":                             "true",
	"notifications.ui_enabled":                                 "true",
	"notifications.fanout.settle_seconds":                      "30",
	"notifications.fanout.max_series_burst":                    "3",
	"notifications.fanout.max_event_age_hours":                 "72",
	"notifications.retention.read_days":                        "90",
	"notifications.retention.unread_days":                      "180",
	"notifications.retention.event_days":                       "30",
	"notifications.webhooks_enabled":                           "false",
	"notifications.webhooks.max_per_profile":                   "10",
	"notifications.webhooks.allow_private_destinations":        "false",
	"notifications.webhooks.deliveries_per_minute_per_profile": "60",
	"notifications.email_enabled":                              "true",
	"notifications.email.allow_per_episode":                    "true",
	"notifications.email.digest_hour":                          "8",
	"notifications.discord_enabled":                            "false",
	"notifications.discord.allow_per_episode":                  "true",
	"notifications.discord.digest_hour":                        "8",
	"notifications.discord.poster_mode":                        "provider",
	"notifications.server_channels_enabled":                    "true",
	"notifications.server_channels.batch_seconds":              "300",
	"notifications.server_channels.mention_requesters":         "false",
	"notifications.web_push_enabled":                           "true",
	"notifications.apple_push_delivery_enabled":                "false",
	"notifications.android_push_delivery_enabled":              "false",

	"opslog.retention_days":           "7",
	"opslog.cleanup_interval_minutes": "15",
	"opslog.max_rows":                 "1000000",
	"opslog.max_size_mb":              "1024",
	"overlays.enabled":                "true",
	"signup.enabled":                  "false",

	"catalog.search.provider":                             "postgres",
	"catalog.search.meilisearch.index":                    "silo_media_items",
	"catalog.search.meilisearch.timeout_ms":               "800",
	"catalog.search.meilisearch.matching_strategy":        "last",
	"catalog.search.meilisearch.sync_batch_size":          "500",
	"catalog.search.meilisearch.rebuild_batch_size":       "5000",
	"catalog.search.meilisearch.rebuild_task_queue_depth": "4",
	"catalog.search.meilisearch.semantic_enabled":         "false",
	"catalog.search.meilisearch.semantic_ratio":           "0.5",
	"catalog.search.meilisearch.embedder":                 "silo_recommendations",
	"catalog.search.meilisearch.binary_quantized":         "false",
}

var legacyAdminSettingFallbacks = []struct {
	canonical string
	legacy    string
}{
	{"s3.public_endpoint", "s3.operational_endpoint"},             //nolint:goconst // Explicit compatibility pair.
	{"s3.public_read_endpoint", "s3.operational_public_endpoint"}, //nolint:goconst // Explicit compatibility pair.
	{"s3.public_region", "s3.operational_region"},                 //nolint:goconst // Explicit compatibility pair.
	{"s3.public_path_style", "s3.operational_path_style"},         //nolint:goconst // Explicit compatibility pair.
	{"s3.public_bucket", "s3.operational_bucket"},                 //nolint:goconst // Explicit compatibility pair.
	{"s3.public_key_prefix", "s3.operational_key_prefix"},         //nolint:goconst // Explicit compatibility pair.
	{"s3.public_access_key", "s3.operational_access_key"},         //nolint:goconst // Explicit compatibility pair.
	{"s3.public_secret_key", "s3.operational_secret_key"},         //nolint:goconst // Explicit compatibility pair.
	{"s3.public_url_auth", "s3.operational_url_auth"},
	{"s3.public_token_secret", "s3.operational_token_secret"},
	{"s3.public_token_param", "s3.operational_token_param"},
	{"s3.public_token_ttl", "s3.operational_token_ttl"}, //nolint:goconst // Explicit compatibility pair.
	{"s3.private_endpoint", "s3.operational_endpoint"},  //nolint:goconst // Explicit compatibility pair.
	{"s3.private_region", "s3.operational_region"},
	{"s3.private_path_style", "s3.operational_path_style"},
	{"s3.private_bucket", "s3.operational_bucket"},
	{"s3.private_key_prefix", "s3.operational_key_prefix"},
	{"s3.private_access_key", "s3.operational_access_key"},
	{"s3.private_secret_key", "s3.operational_secret_key"},
	{"ai.base_url", "subtitle_ai.base_url"}, //nolint:goconst // Explicit compatibility pair.
	{"ai.api_key", "subtitle_ai.api_key"},   //nolint:goconst // Explicit compatibility pair.
	{"ai.chat_model", "subtitle_ai.chat_model"},
}

// EffectiveAdminSettings overlays persisted values onto the runtime defaults
// used by the Admin UI. An empty persisted value means "use the default" for
// keys that have one, matching stringOr/boolOr/intOr in LoadFromDB.
func EffectiveAdminSettings(stored map[string]string) map[string]string {
	effective := make(map[string]string, len(adminSettingDefaults)+len(stored))
	for key, value := range adminSettingDefaults {
		effective[key] = value
	}
	for key, value := range stored {
		if value == "" {
			if _, hasDefault := adminSettingDefaults[key]; hasDefault {
				continue
			}
		}
		effective[key] = value
	}
	// Preserve the canonical-then-legacy precedence used by LoadFromDB. Apply
	// aliases after the stored overlay so an explicitly empty canonical key
	// cannot erase a configured legacy fallback.
	for _, fallback := range legacyAdminSettingFallbacks {
		applyLegacyAdminSettingFallback(
			effective,
			stored,
			fallback.canonical,
			fallback.legacy,
		)
	}
	applyLegacyPositiveIntAdminSettingFallback(
		effective,
		stored,
		"ai.max_concurrent_jobs",
		"subtitle_ai.max_concurrent_jobs",
	)
	return effective
}

func applyLegacyAdminSettingFallback(effective, stored map[string]string, canonical, legacy string) {
	if stored[canonical] != "" {
		return
	}
	if value := stored[legacy]; value != "" {
		effective[canonical] = value
	}
}

func applyLegacyPositiveIntAdminSettingFallback(
	effective,
	stored map[string]string,
	canonical,
	legacy string,
) {
	value := stored[canonical]
	if value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed > 0 {
			return
		}
	}
	if fallback := stored[legacy]; fallback != "" {
		effective[canonical] = fallback
		return
	}
	if fallback, ok := adminSettingDefaults[canonical]; ok {
		effective[canonical] = fallback
	}
}

// NormalizeAdminSetting validates and canonicalizes settings shared by the
// generic single and batch Admin endpoints. Domain-specific validators may
// layer stricter checks on top of this function.
func NormalizeAdminSetting(key, raw string) (string, error) {
	value := strings.TrimSpace(raw)

	switch key {
	case "metadata.cache_images", "playback.transcode_enabled", "playback.local_transcode_fallback",
		"allow_4k_transcode", "enable_transcode_throttle", "audiobookshelf_compat.enabled",
		"jellyfin_compat.enabled", "jellyfin_compat.web_enabled", "recommendations.enabled",
		"subtitle_ai.enabled", "subtitle_ai.transcribe_enabled", "metadata_ai.enabled",
		"download.enabled", "download.transcode_enabled", "email.enabled", "signup.enabled",
		"overlays.enabled", "notifications.release_events_enabled", "notifications.fanout_enabled",
		"notifications.ui_enabled", "notifications.webhooks_enabled",
		"notifications.webhooks.allow_private_destinations", "notifications.email_enabled",
		"notifications.email.allow_per_episode", "notifications.discord_enabled",
		"notifications.discord.allow_per_episode", "notifications.server_channels_enabled",
		"notifications.server_channels.mention_requesters", "notifications.web_push_enabled",
		"notifications.apple_push_delivery_enabled", "notifications.android_push_delivery_enabled",
		"catalog.search.meilisearch.semantic_enabled", "catalog.search.meilisearch.binary_quantized",
		"s3.public_path_style", "s3.private_path_style", "s3.user_db_path_style":
		return normalizeAdminBool(key, value)

	case "database.max_connections":
		return normalizeAdminInt(key, value, 1, 10000)
	case "userdb.pool_max_open":
		return normalizeAdminInt(key, value, 1, 100000)
	case "scanner.workers", "matcher.workers":
		return normalizeAdminInt(key, value, 1, 1024)
	case "matcher.batch_size":
		return normalizeAdminInt(key, value, 1, 100000)
	case "playback.chapter_thumbnail_workers", "playback.chapter_thumbnail_node_capacity":
		return normalizeAdminInt(key, value, 1, 1024)
	case "playback.watched_threshold":
		return normalizeAdminInt(key, value, 1, 100)
	case "playback.min_resume_threshold":
		return normalizeAdminInt(key, value, 1, 99)
	case "transcode_throttle_seconds":
		return normalizeAdminInt(key, value, 60, 86400)
	case "ai.max_concurrent_jobs", "subtitle_ai.max_concurrent_jobs":
		return normalizeAdminInt(key, value, 1, 1024)
	case "subtitle_ai.batch_size":
		return normalizeAdminInt(key, value, 1, 1000)
	case "subtitle_ai.context_neighbors":
		return normalizeAdminInt(key, value, 0, 100)
	case "subtitle_ai.asr_chunk_seconds":
		return normalizeAdminInt(key, value, 60, 600)
	case "subtitle_ai.transcribe_quota_jobs":
		return normalizeAdminInt(key, value, 0, math.MaxInt32)
	case "download.server_bandwidth_mbps", "download.user_bandwidth_mbps":
		return normalizeAdminInt64(key, value, 0, 73_786_976_294_838)
	case "download.max_concurrent_per_user", "download.max_per_period",
		"download.max_concurrent_prepares", "download.artifact_max_bytes":
		return normalizeAdminInt64(key, value, 0, math.MaxInt64)
	case "policy.decision_log_scope_sample_rate", "policy.decision_log_retention_days":
		return normalizeAdminInt(key, value, 1, math.MaxInt32)
	case "email.smtp_port":
		return normalizeAdminInt(key, value, 1, 65535)
	case "notifications.fanout.settle_seconds":
		return normalizeAdminInt(key, value, 0, 3600)
	case "notifications.fanout.max_series_burst":
		return normalizeAdminInt(key, value, 1, 1000)
	case "notifications.fanout.max_event_age_hours":
		return normalizeAdminInt(key, value, 1, 24*365)
	case "notifications.retention.read_days", "notifications.retention.unread_days",
		"notifications.retention.event_days":
		return normalizeAdminInt(key, value, 1, 3650)
	case "notifications.webhooks.max_per_profile":
		return normalizeAdminInt(key, value, 1, 100)
	case "notifications.webhooks.deliveries_per_minute_per_profile":
		return normalizeAdminInt(key, value, 1, 10000)
	case "notifications.email.digest_hour", "notifications.discord.digest_hour":
		return normalizeAdminInt(key, value, 0, 23)
	case "notifications.server_channels.batch_seconds":
		return normalizeAdminInt(key, value, 120, 3600)
	case "catalog.search.meilisearch.timeout_ms":
		return normalizeAdminInt(key, value, 1, math.MaxInt32)
	case "catalog.search.meilisearch.sync_batch_size":
		return normalizeAdminInt(key, value, 1, 10000)
	case "catalog.search.meilisearch.rebuild_batch_size":
		return normalizeAdminInt(key, value, 1, 25000)
	case "catalog.search.meilisearch.rebuild_task_queue_depth":
		return normalizeAdminInt(key, value, 1, 16)
	case "opslog.retention_days", "opslog.cleanup_interval_minutes":
		return normalizeAdminInt(key, value, 1, math.MaxInt32)
	case "opslog.max_rows", "opslog.max_size_mb":
		return normalizeAdminInt64(key, value, 1, math.MaxInt64)
	case "s3.public_token_ttl":
		return normalizeAdminInt(key, value, 1, math.MaxInt32)

	case "recommendations.taste_decay_half_life_days":
		return normalizeAdminFloat(key, value, math.SmallestNonzeroFloat64, math.MaxFloat64)
	case "recommendations.diversity_lambda", "catalog.search.meilisearch.semantic_ratio":
		return normalizeAdminFloat(key, value, 0, 1)

	case "auth.access_token_expiry", "auth.refresh_token_expiry", "userdb.idle_timeout",
		"download.period_duration", "jellyfin_compat.session_ttl",
		"jellyfin_compat.playback_session_ttl":
		return normalizeAdminDuration(key, value)

	case "server.log_level":
		return normalizeAdminEnum(key, value, "debug", "info", "warn", "error")
	case "userdb.backend":
		return normalizeAdminEnum(key, value, "postgres", "sqlite")
	case "playback.hw_accel":
		return normalizeAdminEnum(key, value, "auto", "qsv", "vaapi", "nvenc", "none")
	case "playback.chapter_thumbnail_execution":
		return normalizeAdminEnum(key, value, "local", "prefer_transcode_nodes", "transcode_nodes_only")
	case "playback.chapter_thumbnail_hdr_policy":
		return normalizeAdminEnum(key, value, "disabled", "best_effort")
	case "metadata_ai.on_view":
		return normalizeAdminEnum(key, value, "off", "button", "auto")
	case "subtitle_ai.transcribe_quota_period":
		return normalizeAdminEnum(key, value, "day", "week", "month")
	case "policy.decision_log_verbosity":
		return normalizeAdminEnum(key, value, "digest", "verbose")
	case "email.smtp_security":
		return normalizeAdminEnum(key, value, "starttls", "tls", "none")
	case "notifications.discord.poster_mode":
		return normalizeAdminEnum(key, value, "off", "provider", "server")
	case "catalog.search.provider":
		return normalizeAdminEnum(key, value, "postgres", "meilisearch")
	case "catalog.search.meilisearch.matching_strategy":
		return normalizeAdminEnum(key, value, "last", "all")
	case "s3.public_url_auth":
		return normalizeAdminEnum(key, value, "", "presigned", "public", "cloudflare_token")

	case "recommendations.embeddings_cron", "recommendations.taste_profiles_cron",
		"recommendations.cowatch_cron", "recommendations.recommendations_cron":
		if _, err := cron.ParseStandard(value); err != nil {
			return "", fmt.Errorf("%s must be a valid five-field cron expression: %w", key, err)
		}
		return value, nil

	case "ai.base_url", "ai.asr_base_url", "recommendations.embedding_base_url",
		"jellyfin_compat.public_url", "notifications.email.external_url",
		"s3.public_endpoint", "s3.public_read_endpoint", "s3.private_endpoint",
		"s3.user_db_endpoint", "catalog.search.meilisearch.url":
		return normalizeAdminURL(key, value)
	case "redis.url":
		return NormalizeRedisURL(value)
	case "theme.catalog_url":
		return normalizeAdminThemeURL(key, value)

	case "email.from_address":
		if value == "" {
			return "", nil
		}
		address, err := mail.ParseAddress(value)
		if err != nil || address.Address != value {
			return "", fmt.Errorf("%s must be a valid email address", key)
		}
		return value, nil

	case "defaults.card_overlays", "opslog.bucket_policies", "ui.admin_theme_vars":
		if value == "" {
			return "", nil
		}
		var decoded any
		if err := json.Unmarshal([]byte(value), &decoded); err != nil {
			return "", fmt.Errorf("%s must be valid JSON: %w", key, err)
		}
		return value, nil
	}

	return raw, nil
}

// AdminSettingsCapabilities describes durable bootstrap configuration that is
// intentionally absent from the flat server_settings map.
type AdminSettingsCapabilities struct {
	RedisBootstrapAvailable bool
}

// ValidateAdminSettings validates a stored settings snapshot without external
// bootstrap capabilities.
func ValidateAdminSettings(values map[string]string) error {
	return ValidateAdminSettingsWithCapabilities(values, AdminSettingsCapabilities{})
}

// ValidateAdminSettingsWithCapabilities validates the complete prospective
// settings snapshot against durable bootstrap configuration. It catches
// combinations that only become invalid once independently editable fields are
// considered together.
func ValidateAdminSettingsWithCapabilities(values map[string]string, capabilities AdminSettingsCapabilities) error {
	if _, err := LoadFromDB(values); err != nil {
		return err
	}

	access, err := parseDuration(EffectiveAdminSettings(values)["auth.access_token_expiry"])
	if err != nil || access <= 0 {
		return fmt.Errorf("auth.access_token_expiry must be a positive duration")
	}
	refresh, err := parseDuration(EffectiveAdminSettings(values)["auth.refresh_token_expiry"])
	if err != nil || refresh <= 0 {
		return fmt.Errorf("auth.refresh_token_expiry must be a positive duration")
	}
	if refresh < access {
		return fmt.Errorf("auth.refresh_token_expiry must be greater than or equal to auth.access_token_expiry")
	}

	if watched, _ := strconv.Atoi(EffectiveAdminSettings(values)["playback.watched_threshold"]); watched > 0 {
		if resume, _ := strconv.Atoi(EffectiveAdminSettings(values)["playback.min_resume_threshold"]); resume >= watched {
			return fmt.Errorf("playback.min_resume_threshold must be less than playback.watched_threshold")
		}
	}

	effective := EffectiveAdminSettings(values)
	for _, prefix := range []string{"s3.public", "s3.private"} {
		endpoint := strings.TrimSpace(effective[prefix+"_endpoint"])
		bucket := strings.TrimSpace(effective[prefix+"_bucket"])
		if (endpoint == "") != (bucket == "") {
			return fmt.Errorf("%s endpoint and bucket must be configured together", strings.ReplaceAll(prefix, ".", " "))
		}
		accessKey := strings.TrimSpace(effective[prefix+"_access_key"])
		secretKey := strings.TrimSpace(effective[prefix+"_secret_key"])
		if (accessKey == "") != (secretKey == "") {
			return fmt.Errorf("%s access key and secret key must be configured together", strings.ReplaceAll(prefix, ".", " "))
		}
	}

	switch effective["s3.public_url_auth"] {
	case "", "presigned":
	case "public", cloudflareURLMode:
		if strings.TrimSpace(effective["s3.public_read_endpoint"]) == "" {
			return fmt.Errorf("s3.public_read_endpoint is required for %s URL authentication", effective["s3.public_url_auth"])
		}
		if effective["s3.public_url_auth"] == cloudflareURLMode && strings.TrimSpace(effective["s3.public_token_secret"]) == "" {
			return fmt.Errorf("s3.public_token_secret is required for Cloudflare Token URL authentication")
		}
	default:
		return fmt.Errorf("s3.public_url_auth must be presigned, public, or cloudflare_token")
	}
	if effective["email.enabled"] == "true" {
		if strings.TrimSpace(effective["email.smtp_host"]) == "" {
			return fmt.Errorf("email.smtp_host is required when email is enabled")
		}
		if strings.TrimSpace(effective["email.from_address"]) == "" {
			return fmt.Errorf("email.from_address is required when email is enabled")
		}
	}
	for _, provider := range []string{"trakt", "simkl"} {
		clientID := strings.TrimSpace(effective["watchsync."+provider+".client_id"])
		clientSecret := strings.TrimSpace(effective["watchsync."+provider+".client_secret"])
		if (clientID == "") != (clientSecret == "") {
			return fmt.Errorf("watchsync.%s client ID and client secret must be configured together", provider)
		}
	}
	if err := ValidateRedisRateLimitTransport(effective, capabilities.RedisBootstrapAvailable); err != nil {
		return err
	}

	return nil
}

// ValidateRedisRateLimitTransport ensures a persisted Redis limiter selection
// will still have a usable transport after restart. Active process state is not
// sufficient: it may be using a URL that this same update clears.
func ValidateRedisRateLimitTransport(values map[string]string, redisBootstrapAvailable bool) error {
	effective := EffectiveAdminSettings(values)
	redisURL, err := NormalizeRedisURL(effective["redis.url"])
	if err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(effective["ratelimit.backend"]), "redis") &&
		redisURL == "" &&
		!redisBootstrapAvailable {
		return fmt.Errorf("redis.url or a bootstrap Redis/Sentinel transport is required when ratelimit.backend is redis")
	}
	return nil
}

// NormalizeRedisURL applies the same parser used by the runtime Redis client.
func NormalizeRedisURL(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := redisv9.ParseURL(value); err != nil {
		return "", fmt.Errorf("redis.url must be a valid redis://, rediss://, or unix:// URL: %w", err)
	}
	return value, nil
}

func normalizeAdminBool(key, value string) (string, error) {
	parsed, err := strconv.ParseBool(strings.ToLower(value))
	if err != nil {
		return "", fmt.Errorf("%s must be true or false", key)
	}
	return strconv.FormatBool(parsed), nil
}

func normalizeAdminInt(key, value string, minValue, maxValue int) (string, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minValue || parsed > maxValue {
		return "", fmt.Errorf("%s must be an integer between %d and %d", key, minValue, maxValue)
	}
	return strconv.Itoa(parsed), nil
}

func normalizeAdminInt64(key, value string, minValue, maxValue int64) (string, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < minValue || parsed > maxValue {
		return "", fmt.Errorf("%s must be an integer between %d and %d", key, minValue, maxValue)
	}
	return strconv.FormatInt(parsed, 10), nil
}

func normalizeAdminFloat(key, value string, minValue, maxValue float64) (string, error) {
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minValue || parsed > maxValue {
		return "", fmt.Errorf("%s must be a number between %g and %g", key, minValue, maxValue)
	}
	return strconv.FormatFloat(parsed, 'f', -1, 64), nil
}

func normalizeAdminDuration(key, value string) (string, error) {
	parsed, err := parseDuration(value)
	if err != nil || parsed <= 0 {
		return "", fmt.Errorf("%s must be a positive duration", key)
	}
	return value, nil
}

func normalizeAdminEnum(key, value string, allowed ...string) (string, error) {
	normalized := strings.ToLower(value)
	for _, candidate := range allowed {
		if normalized == candidate {
			return normalized, nil
		}
	}
	return "", fmt.Errorf("%s must be one of: %s", key, strings.Join(allowed, ", "))
}

func normalizeAdminURL(key, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%s must include a URL scheme and host", key)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%s must use http or https", key)
	}
	return strings.TrimRight(value, "/"), nil
}
