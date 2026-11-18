package config

import (
	"fmt"
	"sort"
	"strings"
)

// EnvScope says where a setting set by an environment variable applies.
type EnvScope int

const (
	// EnvScopeStored settings are cluster-wide. An API server that starts with
	// the variable set validates the value, stores it in server_settings, and
	// refuses admin saves of the key. Every API server sharing a database
	// needs the same value, or the last one to start wins.
	EnvScopeStored EnvScope = iota
	// EnvScopeProcess settings apply to the process that reads them only. The
	// value is never stored: it overlays the loaded settings at startup and on
	// every reload, so replicas can differ.
	EnvScopeProcess
)

// EnvSettingVar maps one environment variable to the setting it sets.
type EnvSettingVar struct {
	Var   string
	Key   string
	Scope EnvScope
}

// envSettingVars is the one list of settings an operator can set from the
// environment. API servers (integrated and api mode) read it; proxies and
// transcode nodes do not. A key belongs here only when setting it needs
// nothing an admin save does beyond normalization, the cross-field checks,
// and the guards CheckEnvironmentSettings runs.
//
//nolint:goconst // The table spells each key out so it reads as the contract it is.
var envSettingVars = []EnvSettingVar{
	{"SILO_SERVER_NAME", "branding.server_name", EnvScopeStored},
	{"SILO_PUBLIC_URL", "server.public_url", EnvScopeStored},
	{"SILO_LOCAL_URL", ServerLocalURLSettingKey, EnvScopeStored},
	{"SILO_LAN_DISCOVERY", ServerLANDiscoverySettingKey, EnvScopeStored},
	{"SILO_LOG_LEVEL", "server.log_level", EnvScopeStored},
	{"SILO_LOG_FORMAT", serverLogFormatSettingKey, EnvScopeStored},
	{"SILO_TRUSTED_PROXIES", "clientip.trusted_proxies", EnvScopeStored},
	{"SILO_SIGNUP_ENABLED", "signup.enabled", EnvScopeStored},

	{"SILO_JELLYFIN_ENABLED", "jellyfin_compat.enabled", EnvScopeStored},
	{"SILO_JELLYFIN_PUBLIC_URL", "jellyfin_compat.public_url", EnvScopeStored},
	{"SILO_JELLYFIN_SERVER_NAME", "jellyfin_compat.server_name", EnvScopeStored},
	{"SILO_AUDIOBOOKSHELF_ENABLED", "audiobookshelf_compat.enabled", EnvScopeStored},

	{"SILO_SEARCH_PROVIDER", "catalog.search.provider", EnvScopeStored},
	{"SILO_MEILISEARCH_URL", "catalog.search.meilisearch.url", EnvScopeStored},
	{"SILO_MEILISEARCH_API_KEY", "catalog.search.meilisearch.api_key", EnvScopeStored},

	{"SILO_ARTWORK_STORAGE", "artwork.storage_backend", EnvScopeStored},
	{"SILO_ARTWORK_PATH", "artwork.local_path", EnvScopeStored},
	{"SILO_S3_PUBLIC_ENDPOINT", "s3.public_endpoint", EnvScopeStored},
	{"SILO_S3_PUBLIC_READ_ENDPOINT", "s3.public_read_endpoint", EnvScopeStored},
	{"SILO_S3_PUBLIC_REGION", "s3.public_region", EnvScopeStored},
	{"SILO_S3_PUBLIC_BUCKET", "s3.public_bucket", EnvScopeStored},
	{"SILO_S3_PUBLIC_KEY_PREFIX", "s3.public_key_prefix", EnvScopeStored},
	{"SILO_S3_PUBLIC_PATH_STYLE", "s3.public_path_style", EnvScopeStored},
	{"SILO_S3_PUBLIC_ACCESS_KEY", "s3.public_access_key", EnvScopeStored},
	{"SILO_S3_PUBLIC_SECRET_KEY", "s3.public_secret_key", EnvScopeStored},
	{"SILO_S3_PUBLIC_URL_AUTH", "s3.public_url_auth", EnvScopeStored},
	{"SILO_S3_PUBLIC_TOKEN_SECRET", "s3.public_token_secret", EnvScopeStored},
	{"SILO_S3_PUBLIC_TOKEN_PARAM", "s3.public_token_param", EnvScopeStored},
	{"SILO_S3_PUBLIC_TOKEN_TTL", "s3.public_token_ttl", EnvScopeStored},
	{"SILO_S3_PRIVATE_ENDPOINT", "s3.private_endpoint", EnvScopeStored},
	{"SILO_S3_PRIVATE_REGION", "s3.private_region", EnvScopeStored},
	{"SILO_S3_PRIVATE_BUCKET", "s3.private_bucket", EnvScopeStored},
	{"SILO_S3_PRIVATE_KEY_PREFIX", "s3.private_key_prefix", EnvScopeStored},
	{"SILO_S3_PRIVATE_PATH_STYLE", "s3.private_path_style", EnvScopeStored},
	{"SILO_S3_PRIVATE_ACCESS_KEY", "s3.private_access_key", EnvScopeStored},
	{"SILO_S3_PRIVATE_SECRET_KEY", "s3.private_secret_key", EnvScopeStored},

	{"SILO_SMTP_ENABLED", "email.enabled", EnvScopeStored},
	{"SILO_SMTP_HOST", "email.smtp_host", EnvScopeStored},
	{"SILO_SMTP_PORT", "email.smtp_port", EnvScopeStored},
	{"SILO_SMTP_SECURITY", "email.smtp_security", EnvScopeStored},
	{"SILO_SMTP_USER", "email.smtp_username", EnvScopeStored},
	{"SILO_SMTP_PASSWORD", "email.smtp_password", EnvScopeStored},
	{"SILO_SMTP_FROM_ADDRESS", "email.from_address", EnvScopeStored},
	{"SILO_SMTP_FROM_NAME", "email.from_name", EnvScopeStored},

	{"SILO_FFMPEG_PATH", "playback.ffmpeg_path", EnvScopeStored},
	{"SILO_HW_ACCEL", "playback.hw_accel", EnvScopeStored},
	{"SILO_HW_DEVICE", "playback.hw_device", EnvScopeStored},
	{"SILO_TRANSCODE_DIR", playbackTranscodeDirSettingKey, EnvScopeStored},
	{"SILO_TRANSCODE_ENABLED", "playback.transcode_enabled", EnvScopeStored},
	{"SILO_DOWNLOAD_ARTIFACT_DIR", downloadArtifactDirSettingKey, EnvScopeStored},

	{"SILO_TMDB_API_KEY", "tmdb.api_key", EnvScopeStored},

	{"SILO_DB_MAX_CONNECTIONS", "database.max_connections", EnvScopeProcess},
	{"SILO_USERDB_POOL_MAX_OPEN", "userdb.pool_max_open", EnvScopeProcess},
	{"SILO_SCANNER_WORKERS", "scanner.workers", EnvScopeProcess},
	{"SILO_MATCHER_WORKERS", "matcher.workers", EnvScopeProcess},
}

// EnvSetting is one value the environment sets.
type EnvSetting struct {
	EnvSettingVar
	Value string
}

// EnvSettings are the values the environment sets, normalized per key.
type EnvSettings []EnvSetting

// ReadEnvSettings returns the settings the environment sets. A blank or unset
// variable sets nothing. Each value is normalized the way an admin save
// normalizes it, and an invalid value is an error naming the variable.
func ReadEnvSettings(getenv func(string) string) (EnvSettings, error) {
	var out EnvSettings
	for _, v := range envSettingVars {
		raw := strings.TrimSpace(getenv(v.Var))
		if raw == "" {
			continue
		}
		normalized, err := NormalizeAdminSetting(v.Key, raw)
		if err != nil {
			return nil, fmt.Errorf("invalid %s: %w", v.Var, err)
		}
		out = append(out, EnvSetting{EnvSettingVar: v, Value: normalized})
	}
	return out, nil
}

// Values returns the values of the given scope keyed by setting.
func (s EnvSettings) Values(scope EnvScope) map[string]string {
	out := map[string]string{}
	for _, setting := range s {
		if setting.Scope == scope {
			out[setting.Key] = setting.Value
		}
	}
	return out
}

// All returns every value keyed by setting.
func (s EnvSettings) All() map[string]string {
	out := make(map[string]string, len(s))
	for _, setting := range s {
		out[setting.Key] = setting.Value
	}
	return out
}

// Vars returns the variable that sets each key.
func (s EnvSettings) Vars() map[string]string {
	out := make(map[string]string, len(s))
	for _, setting := range s {
		out[setting.Key] = setting.Var
	}
	return out
}

// VarsFor returns the sorted variables that set any of keys, or every
// variable when keys is nil.
func (s EnvSettings) VarsFor(keys map[string]bool) []string {
	var out []string
	for _, setting := range s {
		if keys == nil || keys[setting.Key] {
			out = append(out, setting.Var)
		}
	}
	sort.Strings(out)
	return out
}
