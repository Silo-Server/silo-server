package handlers

import (
	"net/http"
	"testing"
)

// Stored settings an older build accepted can break a cross-field rule. A save
// that leaves them untouched succeeds; one that touches them must leave them
// valid.
func TestAdminUpdateSettingsLegacyInvalidRelationships(t *testing.T) { //nolint:funlen // ported unchanged from the retired v1 handler test
	for _, tc := range []struct {
		name    string
		initial map[string]string
		// touch is a batch that edits the invalid relationship without
		// repairing it.
		touch map[string]string
	}{
		{
			name:    "auth expiry relationship",
			initial: map[string]string{"auth.access_token_expiry": "48h", "auth.refresh_token_expiry": "24h"},
			touch:   map[string]string{"auth.access_token_expiry": "72h"},
		},
		{
			name:    "playback threshold relationship",
			initial: map[string]string{"playback.watched_threshold": "90", "playback.min_resume_threshold": "95"},
			touch:   map[string]string{"playback.watched_threshold": "80"},
		},
		{
			name:    "S3 pair",
			initial: map[string]string{"s3.public_endpoint": "https://s3.example.invalid"},
			touch:   map[string]string{"s3.public_endpoint": "https://new-s3.example.invalid"},
		},
		{
			name:    "S3 public URL auth relationship",
			initial: map[string]string{"s3.public_url_auth": "public"},
			touch:   map[string]string{"s3.public_token_secret": "configured-token"},
		},
		{
			name:    "email prerequisites",
			initial: map[string]string{"email.enabled": "true"},
			touch:   map[string]string{"email.smtp_host": "smtp.example.invalid"},
		},
		{
			name:    "watchsync pair",
			initial: map[string]string{"watchsync.simkl.client_id": "configured-client-id"},
			touch:   map[string]string{"watchsync.simkl.client_id": "new-client-id"},
		},
		{
			name:    "Redis transport relationship",
			initial: map[string]string{"ratelimit.backend": "redis"},
			touch:   map[string]string{"redis.url": ""},
		},
		{
			name:    "catalog value",
			initial: map[string]string{"catalog.search.provider": "legacy-invalid-provider"},
			touch:   map[string]string{"catalog.search.provider": "still-invalid"},
		},
		{
			name:    "download period relationship",
			initial: map[string]string{"download.max_per_period": "5", "download.period_duration": "invalid-duration"},
			touch:   map[string]string{"download.max_per_period": "6"},
		},
		{
			name:    "matcher legacy fallback",
			initial: map[string]string{"matcher.enable_tv_series_root_queue": "false", "matcher.enable_tv_series_group_queue": "invalid-bool"},
			touch:   map[string]string{"matcher.enable_tv_series_root_queue": "false"},
		},
		{
			name:    "AI concurrency legacy fallback",
			initial: map[string]string{"ai.max_concurrent_jobs": "0", "subtitle_ai.max_concurrent_jobs": "invalid-integer"},
		},
		{
			name:    "S3 path-style legacy fallback",
			initial: map[string]string{"s3.operational_path_style": "invalid-bool"},
		},
		{
			name:    "S3 token TTL legacy fallback",
			initial: map[string]string{"s3.operational_token_ttl": "invalid-integer"},
		},
	} {
		t.Run(tc.name+"/untouched", func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: cloneSettings(tc.initial)}
			if _, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), map[string]string{"branding.server_name": "Casa"}, nil); err != nil {
				t.Fatalf("unrelated save refused: %v", err)
			}
			if settings.values["branding.server_name"] != "Casa" {
				t.Fatalf("branding.server_name = %q, want Casa", settings.values["branding.server_name"])
			}
		})
		if tc.touch == nil {
			continue
		}
		t.Run(tc.name+"/touched", func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: cloneSettings(tc.initial)}
			_, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), tc.touch, nil)
			requireAdminError(t, err, http.StatusBadRequest, "")
			if settings.setManyCalls != 0 {
				t.Fatalf("SetMany calls = %d, want 0", settings.setManyCalls)
			}
		})
	}

	// Each touched value counts on its own: a grandfathered pair elsewhere
	// does not let a new broken pair through.
	settings := &fakeServerSettingsStore{values: map[string]string{"s3.public_endpoint": "https://s3.example.invalid"}}
	_, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), map[string]string{"watchsync.simkl.client_id": "new-client-id"}, nil)
	requireAdminError(t, err, http.StatusBadRequest, "")
	if settings.setManyCalls != 0 {
		t.Fatalf("new invalid pair was written next to a grandfathered one")
	}

	// A save that repairs the relationship is accepted.
	settings = &fakeServerSettingsStore{values: map[string]string{"auth.access_token_expiry": "48h", "auth.refresh_token_expiry": "24h"}}
	if _, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), map[string]string{"auth.refresh_token_expiry": "72h"}, nil); err != nil {
		t.Fatalf("repair refused: %v", err)
	}
	if settings.values["auth.refresh_token_expiry"] != "72h" {
		t.Fatalf("auth.refresh_token_expiry = %q, want 72h", settings.values["auth.refresh_token_expiry"])
	}
}

// Validation reads S3 settings the way the runtime resolves them: canonical
// keys first, then the legacy operational aliases.
func TestAdminUpdateSettingsResolvesLegacyS3Values(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial map[string]string
		values  map[string]string
	}{
		// The bucket comes from the legacy alias.
		{name: "canonical endpoint with legacy bucket", initial: map[string]string{"s3.operational_bucket": "legacy-bucket"}, values: map[string]string{"s3.public_endpoint": "https://s3.example.invalid"}},
		{name: "legacy endpoint with unchanged legacy bucket", initial: map[string]string{"s3.operational_bucket": "legacy-bucket"}, values: map[string]string{"s3.operational_endpoint": "https://s3.example.invalid"}},
		// An explicit canonical value does not pull in a broken alias.
		{name: "public path style", initial: map[string]string{"s3.operational_path_style": "legacy-invalid-bool", "s3.private_path_style": "also-invalid-bool"}, values: map[string]string{"s3.public_path_style": "false"}},
		{name: "private path style", initial: map[string]string{"s3.operational_path_style": "legacy-invalid-bool", "s3.public_path_style": "also-invalid-bool"}, values: map[string]string{"s3.private_path_style": "false"}},
		{name: "public token TTL", initial: map[string]string{"s3.operational_token_ttl": "legacy-invalid-integer"}, values: map[string]string{"s3.public_token_ttl": "7200"}},
		// An alias that canonical values shadow is never read, so it cannot
		// block a save.
		{name: "shadowed path style", initial: map[string]string{"s3.public_path_style": "false", "s3.private_path_style": "true"}, values: map[string]string{"s3.operational_path_style": "invalid-but-shadowed"}},
		{name: "shadowed token TTL", initial: map[string]string{"s3.public_token_ttl": "7200"}, values: map[string]string{"s3.operational_token_ttl": "invalid-but-shadowed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: cloneSettings(tc.initial)}
			if _, err := (&AdminHandler{SettingsRepo: settings}).UpdateAdminSettings(t.Context(), tc.values, nil); err != nil {
				t.Fatalf("save refused: %v", err)
			}
			for key, want := range tc.values {
				if settings.values[key] != want {
					t.Fatalf("%s = %q, want %q", key, settings.values[key], want)
				}
			}
		})
	}
}
