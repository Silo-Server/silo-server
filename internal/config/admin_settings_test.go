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

func TestNormalizeAdminSettingRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "database.max_connections", value: "0"},
		{key: "metadata.cache_images", value: "maybe"},
		{key: "auth.access_token_expiry", value: "forever"},
		{key: "recommendations.embeddings_cron", value: "not a cron"},
		{key: "notifications.server_channels.batch_seconds", value: "119"},
		{key: "catalog.search.meilisearch.semantic_ratio", value: "1.2"},
		{key: "email.smtp_port", value: "70000"},
		{key: "theme.catalog_url", value: "http://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"},
		{key: "theme.catalog_url", value: "https://example.com/catalog.json"},
		{key: "redis.url", value: "not-a-url"},
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
