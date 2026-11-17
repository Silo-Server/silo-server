package config

import "testing"

func TestRestartRequired(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		// An exact key and a prefix-covered namespace.
		{"server.listen", true},
		{"database.max_connections", true},
		// Legacy AI aliases classify like their canonical keys.
		{"subtitle_ai.max_concurrent_jobs", true},
		{"subtitle_ai.base_url", false}, // like ai.base_url — hot-reloaded
		// Compat listeners and session stores are only bound at process
		// startup; Jellyfin identity and Web UI settings in the same namespace
		// are live-read.
		{"jellyfin_compat.listen", true},
		{"jellyfin_compat.public_url", false},
		// The real-time monitor applies the server switch through OnChange,
		// unlike the startup-bound scanner keys.
		{"scanner.realtime_monitoring", false},
		// Unknown keys default to live.
		{"some.future_setting", false},
	}
	for _, tc := range cases {
		if got := RestartRequired(tc.key); got != tc.want {
			t.Errorf("RestartRequired(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
