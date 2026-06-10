package config

import (
	"fmt"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// setIfNonEmpty sets m[key] = value only when value is non-empty.
func setIfNonEmpty(m map[string]string, key, value string) {
	if value != "" {
		m[key] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func yamlHasPath(data []byte, path ...string) (bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, err
	}

	node := &doc
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return false, nil
		}
		node = node.Content[0]
	}

	for _, key := range path {
		if node.Kind != yaml.MappingNode {
			return false, nil
		}
		found := false
		for i := 0; i+1 < len(node.Content); i += 2 {
			if node.Content[i].Value == key {
				node = node.Content[i+1]
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

// YAMLToSettingsMap reads a YAML config file and converts it to a flat
// map[string]string suitable for insertion into the server_settings table.
//
// setDefaults() is called first so that boolean and integer fields that are
// absent from the YAML file are represented with their default values rather
// than Go zero-values.
func YAMLToSettingsMap(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading YAML config: %w", err)
	}

	raw := setDefaults()
	if err := yaml.Unmarshal(data, raw); err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}
	jellyfinCompatEnabledConfigured, err := yamlHasPath(data, "jellyfin_compat", "enabled")
	if err != nil {
		return nil, fmt.Errorf("parsing YAML config: %w", err)
	}

	m := make(map[string]string)

	// Server
	setIfNonEmpty(m, "server.listen", raw.Server.Listen)
	setIfNonEmpty(m, "server.mode", raw.Server.Mode)
	setIfNonEmpty(m, "server.log_level", raw.Server.LogLevel)
	setIfNonEmpty(m, "server.log_format", raw.Server.LogFormat)
	setIfNonEmpty(m, "server.log_quiet", raw.Server.LogQuiet)

	// Database
	setIfNonEmpty(m, "database.url", raw.Database.URL)
	if raw.Database.MaxConnections != 0 {
		m["database.max_connections"] = strconv.Itoa(raw.Database.MaxConnections)
	}

	legacyOperationalConfigured :=
		raw.S3.OperationalEndpoint != "" ||
			raw.S3.OperationalPublicEndpoint != "" ||
			raw.S3.OperationalRegion != "" ||
			raw.S3.OperationalBucket != "" ||
			raw.S3.OperationalKeyPrefix != "" ||
			raw.S3.OperationalAccessKey != "" ||
			raw.S3.OperationalSecretKey != "" ||
			raw.S3.OperationalURLAuth != "" ||
			raw.S3.OperationalTokenSecret != "" ||
			raw.S3.OperationalTokenParam != "" ||
			raw.S3.OperationalTokenTTL > 0 ||
			!raw.S3.OperationalPathStyle
	publicConfigured :=
		raw.S3.PublicEndpoint != "" ||
			raw.S3.PublicReadEndpoint != "" ||
			raw.S3.PublicRegion != "" ||
			raw.S3.PublicBucket != "" ||
			raw.S3.PublicKeyPrefix != "" ||
			raw.S3.PublicAccessKey != "" ||
			raw.S3.PublicSecretKey != "" ||
			raw.S3.PublicURLAuth != "" ||
			raw.S3.PublicTokenSecret != "" ||
			raw.S3.PublicTokenParam != "" ||
			raw.S3.PublicTokenTTL > 0 ||
			raw.S3.MetadataPresignExpiry != ""
	privateConfigured :=
		raw.S3.PrivateEndpoint != "" ||
			raw.S3.PrivateRegion != "" ||
			raw.S3.PrivateBucket != "" ||
			raw.S3.PrivateKeyPrefix != "" ||
			raw.S3.PrivateAccessKey != "" ||
			raw.S3.PrivateSecretKey != ""

	publicPathStyle := raw.S3.PublicPathStyle
	if !publicConfigured && legacyOperationalConfigured {
		publicPathStyle = raw.S3.OperationalPathStyle
	}
	privatePathStyle := raw.S3.PrivatePathStyle
	if !privateConfigured && legacyOperationalConfigured {
		privatePathStyle = raw.S3.OperationalPathStyle
	}

	// S3 — Public assets
	setIfNonEmpty(m, "s3.public_endpoint", firstNonEmpty(raw.S3.PublicEndpoint, raw.S3.OperationalEndpoint))
	setIfNonEmpty(m, "s3.public_read_endpoint", firstNonEmpty(raw.S3.PublicReadEndpoint, raw.S3.OperationalPublicEndpoint))
	setIfNonEmpty(m, "s3.public_region", firstNonEmpty(raw.S3.PublicRegion, raw.S3.OperationalRegion))
	m["s3.public_path_style"] = strconv.FormatBool(publicPathStyle)
	setIfNonEmpty(m, "s3.public_bucket", firstNonEmpty(raw.S3.PublicBucket, raw.S3.OperationalBucket))
	setIfNonEmpty(m, "s3.public_key_prefix", firstNonEmpty(raw.S3.PublicKeyPrefix, raw.S3.OperationalKeyPrefix))
	setIfNonEmpty(m, "s3.public_access_key", firstNonEmpty(raw.S3.PublicAccessKey, raw.S3.OperationalAccessKey))
	setIfNonEmpty(m, "s3.public_secret_key", firstNonEmpty(raw.S3.PublicSecretKey, raw.S3.OperationalSecretKey))
	setIfNonEmpty(m, "s3.public_url_auth", firstNonEmpty(raw.S3.PublicURLAuth, raw.S3.OperationalURLAuth))
	setIfNonEmpty(m, "s3.public_token_secret", firstNonEmpty(raw.S3.PublicTokenSecret, raw.S3.OperationalTokenSecret))
	setIfNonEmpty(m, "s3.public_token_param", firstNonEmpty(raw.S3.PublicTokenParam, raw.S3.OperationalTokenParam))
	if ttl := raw.S3.PublicTokenTTL; ttl > 0 {
		m["s3.public_token_ttl"] = strconv.Itoa(ttl)
	} else if raw.S3.OperationalTokenTTL > 0 {
		m["s3.public_token_ttl"] = strconv.Itoa(raw.S3.OperationalTokenTTL)
	}
	setIfNonEmpty(m, "s3.metadata_presign_expiry", raw.S3.MetadataPresignExpiry)

	// S3 — Private internal
	setIfNonEmpty(m, "s3.private_endpoint", firstNonEmpty(raw.S3.PrivateEndpoint, raw.S3.OperationalEndpoint))
	setIfNonEmpty(m, "s3.private_region", firstNonEmpty(raw.S3.PrivateRegion, raw.S3.OperationalRegion))
	m["s3.private_path_style"] = strconv.FormatBool(privatePathStyle)
	setIfNonEmpty(m, "s3.private_bucket", firstNonEmpty(raw.S3.PrivateBucket, raw.S3.OperationalBucket))
	setIfNonEmpty(m, "s3.private_key_prefix", firstNonEmpty(raw.S3.PrivateKeyPrefix, raw.S3.OperationalKeyPrefix))
	setIfNonEmpty(m, "s3.private_access_key", firstNonEmpty(raw.S3.PrivateAccessKey, raw.S3.OperationalAccessKey))
	setIfNonEmpty(m, "s3.private_secret_key", firstNonEmpty(raw.S3.PrivateSecretKey, raw.S3.OperationalSecretKey))

	// S3 — User DB
	setIfNonEmpty(m, "s3.user_db_endpoint", raw.S3.UserDBEndpoint)
	setIfNonEmpty(m, "s3.user_db_region", raw.S3.UserDBRegion)
	m["s3.user_db_path_style"] = strconv.FormatBool(raw.S3.UserDBPathStyle)
	setIfNonEmpty(m, "s3.user_db_bucket", raw.S3.UserDBBucket)
	setIfNonEmpty(m, "s3.user_db_key_prefix", raw.S3.UserDBKeyPrefix)
	setIfNonEmpty(m, "s3.user_db_access_key", raw.S3.UserDBAccessKey)
	setIfNonEmpty(m, "s3.user_db_secret_key", raw.S3.UserDBSecretKey)

	// UserDB (YAML key: user_db)
	setIfNonEmpty(m, "userdb.backend", raw.UserDB.Backend)
	if raw.UserDB.PoolMaxOpen != 0 {
		m["userdb.pool_max_open"] = strconv.Itoa(raw.UserDB.PoolMaxOpen)
	}
	setIfNonEmpty(m, "userdb.idle_timeout", raw.UserDB.IdleTimeout)
	setIfNonEmpty(m, "userdb.litestream_sync", raw.UserDB.LitestreamSync)
	if raw.UserDB.StaleGraceSeconds != 0 {
		m["userdb.stale_grace_seconds"] = strconv.Itoa(raw.UserDB.StaleGraceSeconds)
	}

	// Scanner
	if raw.Scanner.Workers != 0 {
		m["scanner.workers"] = strconv.Itoa(raw.Scanner.Workers)
	}
	if raw.Scanner.MaxConcurrentLibraries != 0 {
		m["scanner.max_concurrent_libraries"] = strconv.Itoa(raw.Scanner.MaxConcurrentLibraries)
	}
	if raw.Scanner.MaxConcurrentScoped != 0 {
		m["scanner.max_concurrent_scoped"] = strconv.Itoa(raw.Scanner.MaxConcurrentScoped)
	}
	setIfNonEmpty(m, "scanner.file_removal_grace", raw.Scanner.FileRemovalGrace)
	m["scanner.empty_trash_after_scan"] = strconv.FormatBool(raw.Scanner.EmptyTrashAfterScan)

	// Matcher
	if raw.Matcher.Workers != 0 {
		m["matcher.workers"] = strconv.Itoa(raw.Matcher.Workers)
	}
	if raw.Matcher.BatchSize != 0 {
		m["matcher.batch_size"] = strconv.Itoa(raw.Matcher.BatchSize)
	}
	m["matcher.enable_tv_series_root_queue"] = strconv.FormatBool(raw.Matcher.TVSeriesRootQueueEnabled())

	// Playback
	setIfNonEmpty(m, "playback.ffmpeg_path", raw.Playback.FFmpegPath)
	setIfNonEmpty(m, "playback.transcode_dir", raw.Playback.TranscodeDir)
	setIfNonEmpty(m, "playback.hw_accel", raw.Playback.HWAccel)
	if raw.Playback.ChapterThumbnailWorkers != 0 {
		m["playback.chapter_thumbnail_workers"] = strconv.Itoa(raw.Playback.ChapterThumbnailWorkers)
	}
	setIfNonEmpty(m, "playback.chapter_thumbnail_execution", raw.Playback.ChapterThumbnailExecution)
	if raw.Playback.ChapterThumbnailNodeCapacity != 0 {
		m["playback.chapter_thumbnail_node_capacity"] = strconv.Itoa(raw.Playback.ChapterThumbnailNodeCapacity)
	}
	m["playback.transcode_enabled"] = strconv.FormatBool(raw.Playback.TranscodeEnabled)

	// Redis
	m["redis.url"] = raw.Redis.URL
	// Redis Sentinel
	setIfNonEmpty(m, "redis.sentinel_master", raw.Redis.SentinelMaster)
	setIfNonEmpty(m, "redis.sentinel_password", raw.Redis.SentinelPassword)
	// SentinelAddresses is []string — stored in YAML only, not in the flat map

	// Rate Limiting
	m["ratelimit.enabled"] = strconv.FormatBool(raw.RateLimit.Enabled)
	setIfNonEmpty(m, "ratelimit.backend", raw.RateLimit.Backend)

	// Auth
	setIfNonEmpty(m, "auth.jwt_secret", raw.Auth.JWTSecret)
	setIfNonEmpty(m, "auth.access_token_expiry", raw.Auth.AccessTokenExpiry)
	setIfNonEmpty(m, "auth.refresh_token_expiry", raw.Auth.RefreshTokenExpiry)

	// JellyfinCompat
	if jellyfinCompatEnabledConfigured {
		m["jellyfin_compat.enabled"] = strconv.FormatBool(raw.JellyfinCompat.Enabled)
	} else if raw.JellyfinCompat.Listen != "" {
		m["jellyfin_compat.enabled"] = "true"
	}
	setIfNonEmpty(m, "jellyfin_compat.listen", raw.JellyfinCompat.Listen)
	setIfNonEmpty(m, "jellyfin_compat.public_url", raw.JellyfinCompat.PublicURL)
	setIfNonEmpty(m, "jellyfin_compat.emulated_server_version", raw.JellyfinCompat.EmulatedServerVersion)
	setIfNonEmpty(m, "jellyfin_compat.server_id", raw.JellyfinCompat.ServerID)
	setIfNonEmpty(m, "jellyfin_compat.server_name", raw.JellyfinCompat.ServerName)
	setIfNonEmpty(m, "jellyfin_compat.web_enabled", strconv.FormatBool(raw.JellyfinCompat.WebEnabled))
	setIfNonEmpty(m, "jellyfin_compat.web_version", raw.JellyfinCompat.WebVersion)
	setIfNonEmpty(m, "jellyfin_compat.web_dir", raw.JellyfinCompat.WebDir)
	setIfNonEmpty(m, "jellyfin_compat.web_install_dir", raw.JellyfinCompat.WebInstallDir)
	setIfNonEmpty(m, "jellyfin_compat.session_ttl", raw.JellyfinCompat.SessionTTL)
	setIfNonEmpty(m, "jellyfin_compat.playback_session_ttl", raw.JellyfinCompat.PlaybackSessionTTL)

	return m, nil
}
