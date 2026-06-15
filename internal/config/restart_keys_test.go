package config

import "testing"

func TestRestartRequired(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// Explicit infrastructure keys.
		{"server.listen", true},
		{"auth.jwt_secret", true},
		{"ratelimit.backend", true},
		// Prefix-covered namespaces.
		{"database.max_connections", true},
		{"userdb.backend", true},
		{"s3.public_endpoint", true},
		{"redis.url", true},
		{"recommendations.enabled", true},
		// Legacy AI aliases classify like their canonical keys.
		{"subtitle_ai.max_concurrent_jobs", true},
		// Compat listeners and session stores are only bound at process startup.
		{"jellyfin_compat.enabled", true},
		{"jellyfin_compat.listen", true},
		{"jellyfin_compat.server_id", true},
		{"jellyfin_compat.session_ttl", true},
		{"jellyfin_compat.playback_session_ttl", true},
		// Jellyfin identity and Web UI settings are live-read.
		{"jellyfin_compat.public_url", false},
		{"jellyfin_compat.server_name", false},
		{"jellyfin_compat.emulated_server_version", false},
		{"jellyfin_compat.web_enabled", false},
		{"jellyfin_compat.web_version", false},
		{"jellyfin_compat.web_dir", false},
		{"jellyfin_compat.web_install_dir", false},
		{"subtitle_ai.base_url", false}, // like ai.base_url — hot-reloaded
		{"ai.api_key", false},
		{"subtitle_ai.enabled", false},
		{"metadata_ai.on_view", false},
		// Live settings read from the settings repo per request.
		{"branding.server_name", false},
		{"overlays.enabled", false},
		{"markers.mode", false},
		{"download.enabled", false},
		{"allow_4k_transcode", false},
		{"defaults.card_overlays", false},
		// Unknown keys default to live.
		{"some.future_setting", false},
	}
	for _, tc := range cases {
		if got := RestartRequired(tc.key); got != tc.want {
			t.Errorf("RestartRequired(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
