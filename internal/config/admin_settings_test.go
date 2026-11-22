//nolint:goconst // Settings contract tests intentionally repeat literal keys in input and expected maps.
package config

import (
	"context"
	"strings"
	"testing"
)

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

type mapSettings map[string]string

func (m mapSettings) Get(_ context.Context, key string) (string, error) { return m[key], nil }

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
		{key: DashboardHiddenAttentionSettingKey, value: "email"},
		{key: DashboardHiddenAttentionSettingKey, value: `{"email":true}`},
		{key: DashboardHiddenAttentionSettingKey, value: `[1]`},
		{key: DashboardHiddenAttentionSettingKey, value: `null`},
		{key: DashboardHiddenAttentionSettingKey, value: `["email",null]`},
		{key: DashboardHiddenAttentionSettingKey, value: `["` + strings.Repeat("x", 65) + `"]`},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if _, err := NormalizeAdminSetting(tc.key, tc.value); err == nil {
				t.Fatalf("NormalizeAdminSetting(%q, %q) returned nil error", tc.key, tc.value)
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
