package config

import "strings"

// restartRequiredKeys lists server_settings keys whose values are captured at
// process startup (listeners, connection pools, HTTP clients, worker pools)
// and cannot take effect until the server restarts.
//
// Keys absent from this map and from restartRequiredPrefixes apply without a
// restart: they are either read live from the settings repo at request time
// (overlays, branding, markers, download.*, ...) or hot-reloaded through the
// nodeconfig watcher. When converting a frozen consumer to the live config,
// remove its key here — the admin UI restart banner is driven by this
// registry.
var restartRequiredKeys = map[string]bool{
	// Process & logging. log_level/log_quiet hot-reload via the config
	// watcher (shared slog.LevelVar + logfilter.SetQuiet).
	"server.listen":        true,
	"server.mode":          true,
	"server.log_format":    true,
	"opslog.capture_level": true,

	// Auth. The JWT secret is baked into token services and stream signers;
	// the expiry durations hot-reload (new tokens only).
	"auth.jwt_secret": true,

	// Rate limiting gates middleware construction; tier/limit values inside
	// the dedicated /admin/rate-limits endpoint hot-reload and report their
	// own restart state.
	"ratelimit.enabled": true,
	"ratelimit.backend": true,

	// Playback transcode infrastructure. The playback/stream handlers read
	// ffmpeg path and hwaccel live (new transcode sessions), but several
	// startup-built consumers still freeze them (scanner ffprobe, chapter
	// thumbnails, audiobook enricher) — keep restart-required until those
	// convert. transcode_dir is fully live (only the playback handler reads
	// it). The chapter-thumbnail worker pool is sized at construction.
	"playback.ffmpeg_path":               true,
	"playback.hw_accel":                  true,
	"playback.hw_device":                 true,
	"playback.chapter_thumbnail_workers": true,

	// Scanner / matcher toggles captured at construction. Worker counts,
	// batch size, metadata.cache_images, and mdblist.api_key hot-reload via
	// atomic setters wired to the config watcher.
	"scanner.max_concurrent_libraries":     true,
	"scanner.max_concurrent_scoped":        true,
	"scanner.empty_trash_after_scan":       true,
	"matcher.enable_tv_series_root_queue":  true,
	"matcher.enable_tv_series_group_queue": true,

	// External API clients built once at startup.
	"tmdb.api_key": true,

	// Compat listeners and session stores.
	"audiobookshelf_compat.enabled": true,
	// public_url / server_name / emulated_server_version are read live per
	// request; Jellyfin Web settings are read from the settings repo by
	// the dynamic web handler; server_id is generate-once and baked into
	// the resource mapper; the session TTLs are baked into store constructors.
	"jellyfin_compat.enabled":              true,
	"jellyfin_compat.listen":               true,
	"jellyfin_compat.server_id":            true,
	"jellyfin_compat.session_ttl":          true,
	"jellyfin_compat.playback_session_ttl": true,

	// AI: connection settings, models, toggles, and quotas hot-reload via
	// UpdateConfig/setters wired to the config watcher. Only the dispatch
	// semaphore (ai.max_concurrent_jobs, plus its legacy alias) is a
	// fixed-capacity channel sized at construction.
	"ai.max_concurrent_jobs":          true,
	"subtitle_ai.max_concurrent_jobs": true,
}

// restartRequiredPrefixes covers whole namespaces of infrastructure settings:
// connection pools and storage clients that are constructed once at startup.
var restartRequiredPrefixes = []string{
	"database.",
	"userdb.",
	"s3.",
	"redis.",
	"recommendations.",
}

// RestartRequired reports whether changing the given server_settings key
// requires a server restart to take effect.
func RestartRequired(key string) bool {
	if restartRequiredKeys[key] {
		return true
	}
	for _, prefix := range restartRequiredPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
