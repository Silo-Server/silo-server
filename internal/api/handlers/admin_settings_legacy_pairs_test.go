package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminUpdateSettingsGrandfathersUntouchedLegacyInvalidSettings(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]string
	}{
		{
			name: "auth expiry relationship",
			initial: map[string]string{
				"auth.access_token_expiry":  "48h",
				"auth.refresh_token_expiry": "24h",
			},
		},
		{
			name: "playback threshold relationship",
			initial: map[string]string{
				"playback.watched_threshold":    "90",
				"playback.min_resume_threshold": "95",
			},
		},
		{
			name: "S3 pair",
			initial: map[string]string{
				"s3.public_endpoint": "https://s3.example.invalid",
			},
		},
		{
			name: "S3 public URL auth relationship",
			initial: map[string]string{
				"s3.public_url_auth": "public",
			},
		},
		{
			name: "email prerequisites",
			initial: map[string]string{
				"email.enabled": "true",
			},
		},
		{
			name: "watchsync pair",
			initial: map[string]string{
				"watchsync.trakt.client_id": "configured-client-id",
			},
		},
		{
			name: "Redis transport relationship",
			initial: map[string]string{
				"ratelimit.backend": "redis",
			},
		},
		{
			name: "catalog value",
			initial: map[string]string{
				"catalog.search.provider": "legacy-invalid-provider",
			},
		},
		{
			name: "download period relationship",
			initial: map[string]string{
				"download.max_per_period":  "5",
				"download.period_duration": "invalid-duration",
			},
		},
		{
			name: "matcher legacy fallback",
			initial: map[string]string{
				"matcher.enable_tv_series_root_queue":  "false",
				"matcher.enable_tv_series_group_queue": "invalid-bool",
			},
		},
		{
			name: "AI concurrency legacy fallback",
			initial: map[string]string{
				"ai.max_concurrent_jobs":          "0",
				"subtitle_ai.max_concurrent_jobs": "invalid-integer",
			},
		},
		{
			name: "S3 path-style legacy fallback",
			initial: map[string]string{
				"s3.operational_path_style": "invalid-bool",
			},
		},
		{
			name: "S3 token TTL legacy fallback",
			initial: map[string]string{
				"s3.operational_token_ttl": "invalid-integer",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings",
				strings.NewReader(`{"values":{"branding.server_name":"Casa"}}`),
			)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values["branding.server_name"] != "Casa" {
				t.Fatalf("branding.server_name = %q, want Casa", settings.values["branding.server_name"])
			}
		})
	}
}

func TestAdminUpdateSettingsRequiresTouchedLegacyRelationshipsToBeValid(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]string
		body    string
	}{
		{
			name: "auth expiry relationship",
			initial: map[string]string{
				"auth.access_token_expiry":  "48h",
				"auth.refresh_token_expiry": "24h",
			},
			body: `{"values":{"auth.access_token_expiry":"72h"}}`,
		},
		{
			name: "playback threshold relationship",
			initial: map[string]string{
				"playback.watched_threshold":    "90",
				"playback.min_resume_threshold": "95",
			},
			body: `{"values":{"playback.watched_threshold":"80"}}`,
		},
		{
			name: "S3 pair",
			initial: map[string]string{
				"s3.public_endpoint": "https://old-s3.example.invalid",
			},
			body: `{"values":{"s3.public_endpoint":"https://new-s3.example.invalid"}}`,
		},
		{
			name: "S3 public URL auth relationship",
			initial: map[string]string{
				"s3.public_url_auth": "public",
			},
			body: `{"values":{"s3.public_token_secret":"configured-token"}}`,
		},
		{
			name: "email prerequisites",
			initial: map[string]string{
				"email.enabled": "true",
			},
			body: `{"values":{"email.smtp_host":"smtp.example.invalid"}}`,
		},
		{
			name: "watchsync pair",
			initial: map[string]string{
				"watchsync.trakt.client_id": "old-client-id",
			},
			body: `{"values":{"watchsync.trakt.client_id":"new-client-id"}}`,
		},
		{
			name: "Redis transport relationship",
			initial: map[string]string{
				"ratelimit.backend": "redis",
			},
			body: `{"values":{"redis.url":""}}`,
		},
		{
			name: "catalog value",
			initial: map[string]string{
				"catalog.search.provider": "legacy-invalid-provider",
			},
			body: `{"values":{"catalog.search.provider":"still-invalid"}}`,
		},
		{
			name: "download period relationship",
			initial: map[string]string{
				"download.max_per_period":  "5",
				"download.period_duration": "invalid-duration",
			},
			body: `{"values":{"download.max_per_period":"6"}}`,
		},
		{
			name: "matcher legacy fallback",
			initial: map[string]string{
				"matcher.enable_tv_series_root_queue":  "false",
				"matcher.enable_tv_series_group_queue": "invalid-bool",
			},
			body: `{"values":{"matcher.enable_tv_series_root_queue":"false"}}`,
		},
		{
			name: "S3 path-style legacy fallback",
			initial: map[string]string{
				"s3.operational_path_style": "true",
			},
			body: `{"values":{"s3.operational_path_style":"invalid-bool"}}`,
		},
		{
			name: "S3 token TTL legacy fallback",
			initial: map[string]string{
				"s3.operational_token_ttl": "3600",
			},
			body: `{"values":{"s3.operational_token_ttl":"invalid-integer"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			handler.HandleUpdateSettings(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if settings.setManyCalls != 0 {
				t.Fatalf("SetMany calls = %d, want 0", settings.setManyCalls)
			}
		})
	}
}

func TestAdminUpdateSettingsDoesNotHideNewInvalidRelationshipBehindGrandfatheredState(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		"s3.public_endpoint": "https://s3.example.invalid",
	}}
	handler := &AdminHandler{SettingsRepo: settings}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"watchsync.trakt.client_id":"new-client-id"}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 0 {
		t.Fatalf("SetMany calls = %d, want 0", settings.setManyCalls)
	}
}

func TestAdminUpdateSettingsCanRepairLegacyInvalidRelationship(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		"auth.access_token_expiry":  "48h",
		"auth.refresh_token_expiry": "24h",
	}}
	handler := &AdminHandler{SettingsRepo: settings}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"auth.refresh_token_expiry":"72h"}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if settings.values["auth.refresh_token_expiry"] != "72h" {
		t.Fatalf("auth.refresh_token_expiry = %q, want 72h", settings.values["auth.refresh_token_expiry"])
	}
}

func TestAdminUpdateSettingsValidatesResolvedLegacyS3Values(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]string
		body    string
	}{
		{
			name: "canonical endpoint with legacy bucket",
			initial: map[string]string{
				"s3.operational_bucket": "legacy-bucket",
			},
			body: `{"values":{"s3.public_endpoint":"https://s3.example.invalid"}}`,
		},
		{
			name: "legacy endpoint with unchanged legacy bucket",
			initial: map[string]string{
				"s3.operational_bucket": "legacy-bucket",
			},
			body: `{"values":{"s3.operational_endpoint":"https://s3.example.invalid"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			handler.HandleUpdateSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.setManyCalls != 1 {
				t.Fatalf("SetMany calls = %d, want 1", settings.setManyCalls)
			}
		})
	}
}

func TestAdminUpdateSettingsExplicitS3ValueDoesNotPullOperationalFallback(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]string
		body    string
		key     string
		want    string
	}{
		{
			name: "public path style",
			initial: map[string]string{
				"s3.operational_path_style": "legacy-invalid-bool",
				"s3.private_path_style":     "also-invalid-bool",
			},
			body: `{"values":{"s3.public_path_style":"false"}}`,
			key:  "s3.public_path_style",
			want: "false",
		},
		{
			name: "private path style",
			initial: map[string]string{
				"s3.operational_path_style": "legacy-invalid-bool",
				"s3.public_path_style":      "also-invalid-bool",
			},
			body: `{"values":{"s3.private_path_style":"false"}}`,
			key:  "s3.private_path_style",
			want: "false",
		},
		{
			name: "public token TTL",
			initial: map[string]string{
				"s3.operational_token_ttl": "legacy-invalid-integer",
			},
			body: `{"values":{"s3.public_token_ttl":"7200"}}`,
			key:  "s3.public_token_ttl",
			want: "7200",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			handler.HandleUpdateSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values[tc.key] != tc.want {
				t.Fatalf("%s = %q, want %q", tc.key, settings.values[tc.key], tc.want)
			}
		})
	}
}

func TestAdminUpdateSettingsShadowedOperationalS3ValueDoesNotBlockSave(t *testing.T) {
	tests := []struct {
		name    string
		initial map[string]string
		body    string
		key     string
		want    string
	}{
		{
			name: "path style",
			initial: map[string]string{
				"s3.public_path_style":  "false",
				"s3.private_path_style": "true",
			},
			body: `{"values":{"s3.operational_path_style":"invalid-but-shadowed"}}`,
			key:  "s3.operational_path_style",
			want: "invalid-but-shadowed",
		},
		{
			name: "token TTL",
			initial: map[string]string{
				"s3.public_token_ttl": "7200",
			},
			body: `{"values":{"s3.operational_token_ttl":"invalid-but-shadowed"}}`,
			key:  "s3.operational_token_ttl",
			want: "invalid-but-shadowed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			handler.HandleUpdateSettings(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values[tc.key] != tc.want {
				t.Fatalf("%s = %q, want %q", tc.key, settings.values[tc.key], tc.want)
			}
		})
	}
}
