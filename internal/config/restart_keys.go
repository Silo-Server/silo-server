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
	// expiries are frozen until the JWT conversion lands.
	"auth.jwt_secret":           true,
	"auth.access_token_expiry":  true,
	"auth.refresh_token_expiry": true,

	// Rate limiting gates middleware construction; tier/limit values inside
	// the dedicated /admin/rate-limits endpoint hot-reload and report their
	// own restart state.
	"ratelimit.enabled": true,
	"ratelimit.backend": true,

	// Playback transcode infrastructure. ffmpeg path and hwaccel feed several
	// startup-built consumers (stream handler, scanner ffprobe, chapter
	// thumbnails, audiobook enricher); the chapter-thumbnail worker pool is
	// sized at construction.
	"playback.ffmpeg_path":               true,
	"playback.hw_accel":                  true,
	"playback.hw_device":                 true,
	"playback.transcode_dir":             true, // until playback conversion lands
	"playback.chapter_thumbnail_workers": true,

	// Scanner / matcher / metadata worker pools and toggles captured at
	// construction.
	"scanner.workers":                      true, // until scanner conversion lands
	"scanner.max_concurrent_libraries":     true,
	"scanner.max_concurrent_scoped":        true,
	"scanner.empty_trash_after_scan":       true,
	"matcher.workers":                      true, // until matcher conversion lands
	"matcher.batch_size":                   true, // until matcher conversion lands
	"matcher.enable_tv_series_root_queue":  true,
	"matcher.enable_tv_series_group_queue": true,
	"metadata.cache_images":                true, // until metadata conversion lands

	// External API clients built once at startup.
	"tmdb.api_key":    true,
	"mdblist.api_key": true, // until mdblist conversion lands

	// Compat listeners and session stores.
	"audiobookshelf_compat.enabled":           true,
	"jellyfin_compat.listen":                  true,
	"jellyfin_compat.public_url":              true, // until jellycompat conversion lands
	"jellyfin_compat.server_name":             true, // until jellycompat conversion lands
	"jellyfin_compat.emulated_server_version": true, // until jellycompat conversion lands
	"jellyfin_compat.server_id":               true,
	"jellyfin_compat.web_version":             true,
	"jellyfin_compat.web_dir":                 true,
	"jellyfin_compat.session_ttl":             true,
	"jellyfin_compat.playback_session_ttl":    true,

	// AI clients and job services built once at startup (the semaphore for
	// max_concurrent_jobs is a fixed-capacity channel). Legacy subtitle_ai.*
	// connection aliases classify identically to their ai.* counterparts.
	"ai.base_url":                         true,
	"ai.api_key":                          true,
	"ai.chat_model":                       true,
	"ai.asr_base_url":                     true,
	"ai.asr_api_key":                      true,
	"ai.asr_model":                        true,
	"ai.max_concurrent_jobs":              true,
	"subtitle_ai.base_url":                true,
	"subtitle_ai.api_key":                 true,
	"subtitle_ai.chat_model":              true,
	"subtitle_ai.max_concurrent_jobs":     true,
	"subtitle_ai.enabled":                 true,
	"subtitle_ai.transcribe_enabled":      true,
	"subtitle_ai.batch_size":              true,
	"subtitle_ai.context_neighbors":       true,
	"subtitle_ai.asr_chunk_seconds":       true,
	"subtitle_ai.transcribe_quota_jobs":   true,
	"subtitle_ai.transcribe_quota_period": true,
	"metadata_ai.enabled":                 true,
	"metadata_ai.on_view":                 true,
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
