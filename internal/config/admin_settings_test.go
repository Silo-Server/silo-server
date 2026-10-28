//nolint:goconst // Settings contract tests intentionally repeat literal keys in input and expected maps.
package config

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestEffectiveAdminSettingsUsesRuntimeDefaults(t *testing.T) {
	effective := EffectiveAdminSettings(map[string]string{
		"database.max_connections": "",
		"server.log_level":         "debug",
		"custom.setting":           "kept",
	})

	if got := effective["database.max_connections"]; got != "20" {
		t.Fatalf("database.max_connections = %q, want 20", got)
	}
	if got := effective["s3.public_path_style"]; got != "true" {
		t.Fatalf("s3.public_path_style = %q, want true", got)
	}
	if got := effective["playback.transcode_enabled"]; got != "true" {
		t.Fatalf("playback.transcode_enabled = %q, want true", got)
	}
	if got := effective["theme.catalog_url"]; got != DefaultThemeCatalogURL {
		t.Fatalf("theme.catalog_url = %q, want %q", got, DefaultThemeCatalogURL)
	}
	if got := effective["server.log_level"]; got != "debug" {
		t.Fatalf("server.log_level = %q, want debug", got)
	}
	if got := effective["custom.setting"]; got != "kept" {
		t.Fatalf("custom.setting = %q, want kept", got)
	}
}

func TestEffectiveAdminSettingsMarkerDefaultsPreserveExplicitModes(t *testing.T) {
	for _, mode := range []string{"", "off", "local", "online", "both"} {
		t.Run("mode_"+mode, func(t *testing.T) {
			effective := EffectiveAdminSettings(map[string]string{"markers.mode": mode})
			want := mode
			if want == "" {
				want = "both"
			}
			if got := effective["markers.mode"]; got != want {
				t.Fatalf("markers.mode = %q, want %q", got, want)
			}
			if got := effective["markers.online_storage"]; got != "stored" {
				t.Fatalf("markers.online_storage = %q, want stored", got)
			}
			for _, key := range []string{"markers.detect_intros", "markers.detect_credits"} {
				if got := effective[key]; got != "true" {
					t.Fatalf("%s = %q, want true", key, got)
				}
			}
		})
	}
}

func TestEffectiveAdminSettingsCanonicalS3ValuesOverrideLegacyFallbacks(t *testing.T) {
	effective := EffectiveAdminSettings(map[string]string{
		"s3.public_path_style":      "true",
		"s3.private_path_style":     "false",
		"s3.operational_path_style": "true",
		"s3.public_token_ttl":       "7200",
		"s3.operational_token_ttl":  "3600",
	})

	if got := effective["s3.public_path_style"]; got != "true" {
		t.Fatalf("s3.public_path_style = %q, want canonical true", got)
	}
	if got := effective["s3.private_path_style"]; got != "false" {
		t.Fatalf("s3.private_path_style = %q, want canonical false", got)
	}
	if got := effective["s3.public_token_ttl"]; got != "7200" {
		t.Fatalf("s3.public_token_ttl = %q, want canonical 7200", got)
	}
}

func TestEffectiveAdminSettingsEmptyCanonicalS3ValuesUseLegacyFallbacks(t *testing.T) {
	effective := EffectiveAdminSettings(map[string]string{
		"s3.public_path_style":      "",
		"s3.private_path_style":     "",
		"s3.operational_path_style": "false",
		"s3.public_token_ttl":       "",
		"s3.operational_token_ttl":  "3600",
	})

	if got := effective["s3.public_path_style"]; got != "false" {
		t.Fatalf("s3.public_path_style = %q, want legacy false", got)
	}
	if got := effective["s3.private_path_style"]; got != "false" {
		t.Fatalf("s3.private_path_style = %q, want legacy false", got)
	}
	if got := effective["s3.public_token_ttl"]; got != "3600" {
		t.Fatalf("s3.public_token_ttl = %q, want legacy 3600", got)
	}
}

func TestEffectiveAdminSettingsProjectsRuntimeLegacyFallbacks(t *testing.T) {
	stored := map[string]string{
		"s3.operational_endpoint":         "https://s3.example.invalid",
		"s3.operational_public_endpoint":  "https://cdn.example.invalid",
		"s3.operational_region":           "us-test-1",
		"s3.operational_path_style":       "false",
		"s3.operational_bucket":           "legacy-bucket",
		"s3.operational_key_prefix":       "legacy-prefix",
		"s3.operational_access_key":       "legacy-access",
		"s3.operational_secret_key":       "legacy-secret",
		"s3.operational_url_auth":         "presigned",
		"s3.operational_token_secret":     "legacy-token",
		"s3.operational_token_param":      "signature",
		"s3.operational_token_ttl":        "3600",
		"subtitle_ai.chat_model":          "legacy-chat-model",
		"subtitle_ai.max_concurrent_jobs": "7",
		"ai.max_concurrent_jobs":          "0",
	}

	effective := EffectiveAdminSettings(stored)
	expected := map[string]string{
		"s3.public_endpoint":      stored["s3.operational_endpoint"],
		"s3.public_read_endpoint": stored["s3.operational_public_endpoint"],
		"s3.public_region":        stored["s3.operational_region"],
		"s3.public_path_style":    stored["s3.operational_path_style"],
		"s3.public_bucket":        stored["s3.operational_bucket"],
		"s3.public_key_prefix":    stored["s3.operational_key_prefix"],
		"s3.public_access_key":    stored["s3.operational_access_key"],
		"s3.public_secret_key":    stored["s3.operational_secret_key"],
		"s3.public_url_auth":      stored["s3.operational_url_auth"],
		"s3.public_token_secret":  stored["s3.operational_token_secret"],
		"s3.public_token_param":   stored["s3.operational_token_param"],
		"s3.public_token_ttl":     stored["s3.operational_token_ttl"],
		"s3.private_endpoint":     stored["s3.operational_endpoint"],
		"s3.private_region":       stored["s3.operational_region"],
		"s3.private_path_style":   stored["s3.operational_path_style"],
		"s3.private_bucket":       stored["s3.operational_bucket"],
		"s3.private_key_prefix":   stored["s3.operational_key_prefix"],
		"s3.private_access_key":   stored["s3.operational_access_key"],
		"s3.private_secret_key":   stored["s3.operational_secret_key"],
		"ai.chat_model":           stored["subtitle_ai.chat_model"],
		"ai.max_concurrent_jobs":  stored["subtitle_ai.max_concurrent_jobs"],
	}
	for key, want := range expected {
		if got := effective[key]; got != want {
			t.Errorf("%s = %q, want legacy fallback %q", key, got, want)
		}
	}
}

func TestEffectiveAdminSettingsUsesRuntimeDefaultForNonpositiveCanonicalAIConcurrency(t *testing.T) {
	for _, canonical := range []string{"0", "-1"} {
		t.Run(canonical, func(t *testing.T) {
			effective := EffectiveAdminSettings(map[string]string{
				"ai.max_concurrent_jobs": canonical,
			})
			if got := effective["ai.max_concurrent_jobs"]; got != "2" {
				t.Fatalf("ai.max_concurrent_jobs = %q, want runtime default 2", got)
			}
		})
	}
}

func TestAdminSettingDefaultsAlignWithConfigRuntimeDefaults(t *testing.T) {
	baseline, err := LoadFromDB(nil)
	if err != nil {
		t.Fatal(err)
	}
	normalizeEffectiveRuntimeDefaults(baseline)

	for key, value := range adminSettingDefaults {
		t.Run(key, func(t *testing.T) {
			withExplicitDefault, err := LoadFromDB(map[string]string{key: value})
			if err != nil {
				t.Fatal(err)
			}
			normalizeEffectiveRuntimeDefaults(withExplicitDefault)
			if !reflect.DeepEqual(withExplicitDefault, baseline) {
				t.Fatalf("admin default %q does not match the runtime default", value)
			}
		})
	}
}

// TestPlaybackTranscodeDefaults pins the transcode capability defaults and
// checks that a runtime reader of an unset row sees the same value as the
// Admin UI.
func TestPlaybackTranscodeDefaults(t *testing.T) {
	effective := EffectiveAdminSettings(nil)
	for key, want := range map[string]bool{
		Allow4KTranscodeSettingKey:                  true,
		PlaybackAllowHEVCEncodingSettingKey:         false,
		PlaybackTranscodeHardwareToneMapSettingKey:  true,
		PlaybackTranscodeSoftwareToneMapSettingKey:  true,
		ChapterThumbnailSoftwareToneMapSettingKey:   true,
		playback.TranscodeThrottleEnabledSettingKey: true,
	} {
		if got := effective[key]; got != strconv.FormatBool(want) {
			t.Errorf("%s default = %q, want %t", key, got, want)
		}
		if got := AdminSettingEnabled(key, ""); got != want {
			t.Errorf("AdminSettingEnabled(%s, unset) = %t, want %t", key, got, want)
		}
		if got := AdminSettingEnabled(key, " "+strconv.FormatBool(!want)+" "); got == want {
			t.Errorf("AdminSettingEnabled(%s, stored %t) ignored the stored value", key, !want)
		}
	}
	if got := playback.ConfiguredTranscodeThrottleSeconds(t.Context(), mapSettings{}); got != playback.DefaultTranscodeThrottleSeconds {
		t.Errorf("throttle with no stored settings = %d, want %d", got, playback.DefaultTranscodeThrottleSeconds)
	}
	if got := playback.ConfiguredTranscodeThrottleSeconds(t.Context(), mapSettings{playback.TranscodeThrottleEnabledSettingKey: "false"}); got != 0 {
		t.Errorf("throttle explicitly disabled = %d, want 0", got)
	}
}

type mapSettings map[string]string

func (m mapSettings) Get(_ context.Context, key string) (string, error) { return m[key], nil }

func normalizeEffectiveRuntimeDefaults(cfg *Config) {
	if cfg.S3.Public.URLAuth == "" {
		cfg.S3.Public.URLAuth = "presigned"
	}
	if cfg.S3.Public.TokenParam == "" {
		cfg.S3.Public.TokenParam = "verify"
	}
	if cfg.S3.Public.TokenTTL <= 0 {
		cfg.S3.Public.TokenTTL = 10800
	}
	// The client IP loader treats an empty value as its built-in private-range
	// default. Normalize formatting as well so equivalent CIDR lists compare.
	cfg.ClientIP.TrustedProxies = strings.ReplaceAll(
		firstConfiguredString(
			map[string]string{"trusted": cfg.ClientIP.TrustedProxies},
			adminSettingDefaults["clientip.trusted_proxies"],
			"trusted",
		),
		" ",
		"",
	)
}

// TestNormalizeAdminSettingRejectsInvalidValues verifies invalid admin settings are rejected.
func TestNormalizeAdminSettingRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "database.max_connections", value: "0"},
		{key: "metadata.cache_images", value: "maybe"},
		{key: ChapterThumbnailSoftwareToneMapSettingKey, value: "maybe"},
		{key: PlaybackTranscodeHardwareToneMapSettingKey, value: "maybe"},
		{key: PlaybackTranscodeSoftwareToneMapSettingKey, value: "maybe"},
		{key: "auth.access_token_expiry", value: "forever"},
		{key: "recommendations.embeddings_cron", value: "not a cron"},
		{key: "notifications.server_channels.batch_seconds", value: "119"},
		{key: "catalog.search.meilisearch.semantic_ratio", value: "1.2"},
		{key: "email.smtp_port", value: "70000"},
		{key: "theme.catalog_url", value: "http://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"},
		{key: "theme.catalog_url", value: "https://example.com/catalog.json"},
		{key: "redis.url", value: "not-a-url"},
		{key: "playback.segment_retention_seconds", value: "119"},
		{key: "scanner.max_concurrent_libraries", value: "0"},
		{key: "scanner.max_concurrent_scoped", value: "-1"},
		{key: "scanner.empty_trash_after_scan", value: "sometimes"},
		{key: "scanner.realtime_monitoring", value: "sometimes"},
		{key: "scanner.file_removal_grace", value: "a while"},
		{key: "matcher.enable_tv_series_root_queue", value: "yes please"},
		{key: "matcher.enable_tv_series_group_queue", value: "yes please"},
		{key: "policy.editor_enabled", value: "maybe"},
		{key: "policy.eval_timeout_ms", value: "0"},
		{key: "subtitle_ai.live_asr_chunk_seconds", value: "0"},
		{key: "opslog.capture_level", value: "chatty"},
		{key: "s3.metadata_presign_expiry", value: "0s"},
		{key: "recommendations.embeddings_job_timeout", value: "soon"},
		{key: PlaybackRoutingRemuxExecutionSettingKey, value: "sometimes_worker"},
		{key: PlaybackRoutingRemuxEgressSettingKey, value: "worker"},
		{key: ServerLocalURLSettingKey, value: "192.168.1.20:8080"},
		{key: ServerLocalURLSettingKey, value: "ftp://192.168.1.20"},
		{key: ServerLocalURLSettingKey, value: "http://192.168.1.20:8080/silo"},
		{key: ServerLocalURLSettingKey, value: "http://192.168.1.20:8080/?x=1"},
		{key: ServerLocalURLSettingKey, value: "http://user@192.168.1.20:8080"},
		// An administrator default is validated against the value schema of the
		// setting it stands in for, so the resolver never has to discard one.
		{key: "defaults.ui.title_art", value: "off"},
		{key: "defaults.ui.title_art", value: `"false"`},
		{key: "defaults.ui.title_art", value: "1"},
		{key: "defaults.ui.title_art", value: "null"},
		{key: "autoscan.events_retention_days", value: "0"},
		{key: "autoscan.events_retention_days", value: "3651"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if _, err := NormalizeAdminSetting(tc.key, tc.value); err == nil {
				t.Fatalf("NormalizeAdminSetting(%q, %q) returned nil error", tc.key, tc.value)
			}
		})
	}
}

func TestNormalizeAdminSettingAcceptsValidValues(t *testing.T) {
	tests := []struct{ key, value, want string }{
		{"playback.segment_retention_seconds", "0", "0"},
		{"playback.segment_retention_seconds", "120", "120"},
		{"playback.segment_retention_seconds", "86400", "86400"},
		{"theme.catalog_url", "https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json/", "https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"},
		{PlaybackRoutingRemuxExecutionSettingKey, "  prefer_transcode  ", "prefer_transcode"},
		{PlaybackRoutingVideoTranscodeExecutionSettingKey, "  prefer_api  ", "prefer_api"},
		{PlaybackRoutingDirectPlayEgressSettingKey, "  proxy_only  ", "proxy_only"},
		{PlaybackRoutingRemuxEgressSettingKey, "  api_only  ", "api_only"},
		{PlaybackRoutingVideoTranscodeEgressSettingKey, "  prefer_proxy  ", "prefer_proxy"},
		// The loader reads a zero or negative grace as "remove missing files
		// immediately", so the admin API must not reject it.
		{"scanner.file_removal_grace", "  0s  ", "0s"},
		{"scanner.file_removal_grace", "  -1h  ", "-1h"},
		{"scanner.file_removal_grace", "  72h  ", "72h"},
		{ServerLocalURLSettingKey, "", ""},
		{ServerLocalURLSettingKey, " http://192.168.1.20:8080/ ", "http://192.168.1.20:8080"},
		{ServerLocalURLSettingKey, "https://silo.lan", "https://silo.lan"},
		{"defaults.ui.title_art", "false", "false"},
		{"defaults.ui.title_art", " true ", "true"},
		{"defaults.ui.title_art", "", ""},
		// A defaults.* key for a setting without admin_default is not an
		// administrator default and passes through untouched.
		{"defaults.ui.theme", "anything", "anything"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, err := NormalizeAdminSetting(tc.key, tc.value)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAdminSetting(%q, %q) = %q, %v; want %q", tc.key, tc.value, got, err, tc.want)
			}
		})
	}
}

func TestValidateAdminSettingsRejectsImpossibleHardPlaybackRoute(t *testing.T) {
	values := map[string]string{
		PlaybackRoutingRemuxExecutionSettingKey: "api_only",
		PlaybackRoutingRemuxEgressSettingKey:    "proxy_only",
	}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("API-only remux with proxy-only egress was accepted")
	}
	values = map[string]string{
		PlaybackRoutingVideoTranscodeExecutionSettingKey: "api_only",
		PlaybackRoutingVideoTranscodeEgressSettingKey:    "proxy_only",
	}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("API-only video transcode with proxy-only egress was accepted")
	}
}

func TestValidateAdminSettingsChecksProspectiveRelationships(t *testing.T) {
	values := map[string]string{
		"auth.access_token_expiry":      "48h",
		"auth.refresh_token_expiry":     "24h",
		"playback.watched_threshold":    "90",
		"playback.min_resume_threshold": "5",
	}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() returned nil for refresh shorter than access")
	}

	values["auth.refresh_token_expiry"] = "72h"
	values["playback.min_resume_threshold"] = "95"
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() returned nil for resume threshold above watched")
	}
}

func TestValidateAdminSettingsRequiresDurableRedisTransport(t *testing.T) {
	values := map[string]string{"ratelimit.backend": "redis"}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() accepted Redis backend without a durable transport")
	}

	if err := ValidateAdminSettingsWithCapabilities(values, AdminSettingsCapabilities{
		RedisBootstrapAvailable: true,
	}); err != nil {
		t.Fatalf("bootstrap Redis URL was rejected: %v", err)
	}

	values["redis.url"] = "redis://cache.example.invalid:6379"
	if err := ValidateAdminSettings(values); err != nil {
		t.Fatalf("persisted Redis URL was rejected: %v", err)
	}

	values["redis.url"] = "not-a-url"
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() accepted a malformed Redis URL")
	}
}

func TestAuthProviderRecheckInterval(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"":      12 * time.Hour,
		"bogus": 12 * time.Hour,
		"-1h":   12 * time.Hour,
		"30m":   30 * time.Minute,
		"2d":    48 * time.Hour,
		" 6h ":  6 * time.Hour,
	} {
		if got := AuthProviderRecheckInterval(raw); got != want {
			t.Errorf("AuthProviderRecheckInterval(%q) = %v, want %v", raw, got, want)
		}
	}
}
