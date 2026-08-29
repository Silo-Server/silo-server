//nolint:goconst // Settings contract tests intentionally repeat literal keys in input and expected maps.
package config

import (
	"reflect"
	"strings"
	"testing"
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

func TestEffectiveAdminSettingsUsesLegacyS3FallbacksBeforeDefaults(t *testing.T) {
	effective := EffectiveAdminSettings(map[string]string{
		"s3.operational_path_style": "false",
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
		"subtitle_ai.base_url":            "https://legacy-ai.example.invalid",
		"subtitle_ai.api_key":             "legacy-ai-key",
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
		"ai.base_url":             stored["subtitle_ai.base_url"],
		"ai.api_key":              stored["subtitle_ai.api_key"],
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

func TestChapterThumbnailSoftwareToneMapDefaultsDisabled(t *testing.T) {
	effective := EffectiveAdminSettings(nil)
	if got := effective[chapterThumbnailSoftwareToneMapKey]; got != "false" {
		t.Fatalf("software tone-map default = %q, want false", got)
	}
}

// TestTranscodeToneMapPoliciesDefaultDisabled verifies tone mapping remains opt-in.
func TestTranscodeToneMapPoliciesDefaultDisabled(t *testing.T) {
	effective := EffectiveAdminSettings(nil)
	for _, key := range []string{
		PlaybackTranscodeHardwareToneMapSettingKey,
		PlaybackTranscodeSoftwareToneMapSettingKey,
	} {
		if got := effective[key]; got != "false" {
			t.Fatalf("%s default = %q, want false", key, got)
		}
	}
}

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
		{key: chapterThumbnailSoftwareToneMapKey, value: "maybe"},
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
		{key: "scanner.max_concurrent_libraries", value: "0"},
		{key: "scanner.max_concurrent_scoped", value: "-1"},
		{key: "scanner.empty_trash_after_scan", value: "sometimes"},
		{key: "scanner.file_removal_grace", value: "a while"},
		{key: "matcher.enable_tv_series_root_queue", value: "yes please"},
		{key: "matcher.enable_tv_series_group_queue", value: "yes please"},
		{key: "policy.editor_enabled", value: "maybe"},
		{key: "policy.eval_timeout_ms", value: "0"},
		{key: "subtitle_ai.live_asr_chunk_seconds", value: "0"},
		{key: "opslog.capture_level", value: "chatty"},
		{key: "s3.metadata_presign_expiry", value: "0s"},
		{key: "recommendations.embeddings_job_timeout", value: "soon"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if _, err := NormalizeAdminSetting(tc.key, tc.value); err == nil {
				t.Fatalf("NormalizeAdminSetting(%q, %q) returned nil error", tc.key, tc.value)
			}
		})
	}
}

func TestNormalizeAdminSettingAcceptsApprovedThemeCatalogURL(t *testing.T) {
	got, err := NormalizeAdminSetting(
		"theme.catalog_url",
		"https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json/",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json" {
		t.Fatalf("normalized URL = %q", got)
	}
}

func TestNormalizeAdminSettingAcceptsVideoToolbox(t *testing.T) {
	got, err := NormalizeAdminSetting("playback.hw_accel", " VideoToolbox ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "videotoolbox" {
		t.Fatalf("normalized hardware acceleration = %q, want videotoolbox", got)
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
		t.Fatalf("bootstrap Sentinel transport was rejected: %v", err)
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

func TestNormalizeAdminSettingCanonicalizesRedisURL(t *testing.T) {
	got, err := NormalizeAdminSetting("redis.url", "  rediss://cache.example.invalid:6380/2  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rediss://cache.example.invalid:6380/2" {
		t.Fatalf("normalized Redis URL = %q", got)
	}
}

// TestNormalizeAdminSettingKeepsPermissiveScannerGrace locks the loader's
// documented behavior: a zero or negative grace means "remove missing files
// immediately", so the admin API must not reject it.
func TestNormalizeAdminSettingKeepsPermissiveScannerGrace(t *testing.T) {
	for _, value := range []string{"0s", "-1h", "72h"} {
		got, err := NormalizeAdminSetting("scanner.file_removal_grace", "  "+value+"  ")
		if err != nil {
			t.Fatalf("NormalizeAdminSetting(scanner.file_removal_grace, %q): %v", value, err)
		}
		if got != value {
			t.Fatalf("normalized grace = %q, want %q", got, value)
		}
	}
}

// TestOpslogCaptureLevelAcceptsWarningAlias mirrors the startup reader in
// cmd/silo, which treats "warning" as "warn".
func TestOpslogCaptureLevelAcceptsWarningAlias(t *testing.T) {
	got, err := NormalizeAdminSetting("opslog.capture_level", "WARNING")
	if err != nil {
		t.Fatal(err)
	}
	if got != "warning" {
		t.Fatalf("normalized capture level = %q, want warning", got)
	}
}

// TestHiddenTierDefaultsAreExposed guards the keys that have no admin UI: the
// API must still report the value the server is actually running.
func TestHiddenTierDefaultsAreExposed(t *testing.T) {
	effective := EffectiveAdminSettings(nil)
	want := map[string]string{
		"recommendations.embedding_provider":     "ollama",
		"recommendations.embeddings_job_timeout": "24h",
		"policy.editor_enabled":                  "false",
		"policy.eval_timeout_ms":                 "25",
		"subtitle_ai.live_asr_chunk_seconds":     "30",
		"scanner.max_concurrent_libraries":       "1",
		"scanner.max_concurrent_scoped":          "2",
		"scanner.file_removal_grace":             "24h",
		"scanner.empty_trash_after_scan":         "true",
		"matcher.enable_tv_series_root_queue":    "true",
		"matcher.enable_tv_series_group_queue":   "false",
		"opslog.capture_level":                   "info",
		"s3.metadata_presign_expiry":             "4h",
	}
	for key, value := range want {
		if got := effective[key]; got != value {
			t.Errorf("effective[%q] = %q, want %q", key, got, value)
		}
		if _, err := NormalizeAdminSetting(key, value); err != nil {
			t.Errorf("default for %q is rejected by NormalizeAdminSetting: %v", key, err)
		}
	}
}
