package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/diagnostics"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type fakeServerSettingsStore struct {
	values       map[string]string
	setCalls     int
	setManyCalls int
	atomicCalls  int
}

func (f *fakeServerSettingsStore) Get(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *fakeServerSettingsStore) Set(_ context.Context, key, value string) error {
	f.setCalls++
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
}

func (f *fakeServerSettingsStore) SetMany(_ context.Context, values map[string]string) error {
	f.setManyCalls++
	if f.values == nil {
		f.values = map[string]string{}
	}
	for key, value := range values {
		f.values[key] = value
	}
	return nil
}

func (f *fakeServerSettingsStore) UpdateAtomic(
	ctx context.Context,
	update func(current map[string]string) (map[string]string, error),
) error {
	f.atomicCalls++
	current, err := f.GetAll(ctx)
	if err != nil {
		return err
	}
	writes, err := update(current)
	if err != nil || len(writes) == 0 {
		return err
	}
	return f.SetMany(ctx, writes)
}

func TestAdminGetEffectiveSettingsReturnsRuntimeDefaultsAndRedactsSecrets(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		"server.log_level": "debug",
		"tmdb.api_key":     "never-return-this",
	}}
	handler := &AdminHandler{SettingsRepo: settings}
	rec := httptest.NewRecorder()

	handler.HandleGetEffectiveSettings(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/effective", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var values map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&values); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if values["server.log_level"] != "debug" {
		t.Fatalf("server.log_level = %q, want debug", values["server.log_level"])
	}
	if values["database.max_connections"] != "20" {
		t.Fatalf("database.max_connections = %q, want effective default 20", values["database.max_connections"])
	}
	if values["playback.transcode_enabled"] != "true" {
		t.Fatalf("playback.transcode_enabled = %q, want effective default true", values["playback.transcode_enabled"])
	}
	if _, leaked := values["tmdb.api_key"]; leaked {
		t.Fatal("effective settings response leaked tmdb.api_key")
	}
}

func TestAdminSettingsReadsHideMachineManagedCheckpoint(t *testing.T) {
	const checkpoint = `{"baseline_identity":"old","target_identity":"new"}`
	settings := &fakeServerSettingsStore{values: map[string]string{
		"server.log_level":                          "debug",
		config.ArtworkStorageReconcileCheckpointKey: checkpoint,
	}}
	handler := &AdminHandler{SettingsRepo: settings}

	for _, tc := range []struct {
		name   string
		handle func(http.ResponseWriter, *http.Request)
		path   string
	}{
		{name: "raw settings", handle: handler.HandleGetSettings, path: "/admin/settings"},
		{name: "effective settings", handle: handler.HandleGetEffectiveSettings, path: "/admin/settings/effective"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handle(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var values map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&values); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if _, leaked := values[config.ArtworkStorageReconcileCheckpointKey]; leaked {
				t.Fatalf("response leaked machine-managed checkpoint: %#v", values)
			}
			if values["server.log_level"] != "debug" {
				t.Fatalf("server.log_level = %q, want debug", values["server.log_level"])
			}
		})
	}

	t.Run("single setting", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/settings/"+config.ArtworkStorageReconcileCheckpointKey, nil)
		req = withChiParam(req, "key", config.ArtworkStorageReconcileCheckpointKey)
		rec := httptest.NewRecorder()

		handler.HandleGetSetting(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), checkpoint) {
			t.Fatalf("response leaked machine-managed checkpoint: %s", rec.Body.String())
		}
	})
}

func TestAdminSettingsWritesRejectMachineManagedCheckpoint(t *testing.T) {
	const (
		storedCheckpoint      = `{"baseline_identity":"old","target_identity":"new"}`
		replacementCheckpoint = `{"baseline_identity":"wrong","target_identity":"wrong"}`
	)

	t.Run("batch settings", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			config.ArtworkStorageReconcileCheckpointKey: storedCheckpoint,
		}}
		handler := &AdminHandler{SettingsRepo: settings}
		body, err := json.Marshal(updateSettingsRequest{Values: map[string]string{
			config.ArtworkStorageReconcileCheckpointKey: replacementCheckpoint,
		}})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if got := settings.values[config.ArtworkStorageReconcileCheckpointKey]; got != storedCheckpoint {
			t.Fatalf("checkpoint = %q, want unchanged %q", got, storedCheckpoint)
		}
		if settings.atomicCalls != 0 {
			t.Fatalf("atomic update calls = %d, want 0", settings.atomicCalls)
		}
	})

	t.Run("single setting", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			config.ArtworkStorageReconcileCheckpointKey: storedCheckpoint,
		}}
		handler := &AdminHandler{SettingsRepo: settings}
		body, err := json.Marshal(updateSettingRequest{Value: replacementCheckpoint})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings/"+config.ArtworkStorageReconcileCheckpointKey,
			bytes.NewReader(body),
		)
		req = withChiParam(req, "key", config.ArtworkStorageReconcileCheckpointKey)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSetting(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if got := settings.values[config.ArtworkStorageReconcileCheckpointKey]; got != storedCheckpoint {
			t.Fatalf("checkpoint = %q, want unchanged %q", got, storedCheckpoint)
		}
		if settings.atomicCalls != 0 {
			t.Fatalf("atomic update calls = %d, want 0", settings.atomicCalls)
		}
	})
}

func TestAdminGetEffectiveSettingsUsesEnvironmentManagedRuntimeValue(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		"clientip.trusted_proxies": "10.0.0.0/8",
	}}
	handler := &AdminHandler{
		SettingsRepo: settings,
		BootstrapSensitiveConfigured: map[string]bool{
			"clientip.trusted_proxies": true,
			"redis.url":                true,
		},
		BootstrapSensitiveValues: map[string]string{
			"clientip.trusted_proxies": "192.0.2.0/24, 2001:db8::/32",
			"redis.url":                "redis://private.example.invalid:6379",
		},
	}
	rec := httptest.NewRecorder()

	handler.HandleGetEffectiveSettings(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/effective", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var values map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&values); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := values["clientip.trusted_proxies"]; got != "192.0.2.0/24, 2001:db8::/32" {
		t.Fatalf("clientip.trusted_proxies = %q, want active environment value", got)
	}
	if _, leaked := values["redis.url"]; leaked {
		t.Fatal("effective settings response leaked environment-managed redis.url")
	}
}

func TestAdminSettingsValidationIncludesEnvironmentManagedValues(t *testing.T) {
	newHandler := func() (*AdminHandler, *fakeServerSettingsStore) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"watchsync.trakt.client_id": "configured-client-id",
		}}
		return &AdminHandler{
			SettingsRepo: settings,
			BootstrapSensitiveConfigured: map[string]bool{
				"watchsync.trakt.client_secret": true,
			},
			BootstrapSensitiveValues: map[string]string{
				"watchsync.trakt.client_secret": "clawrouter-e2e-secret",
			},
		}, settings
	}

	t.Run("batch update", func(t *testing.T) {
		handler, settings := newHandler()
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

	t.Run("single update", func(t *testing.T) {
		handler, settings := newHandler()
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings/branding.server_name",
			strings.NewReader(`{"value":"Casa"}`),
		)
		req = withChiParam(req, "key", "branding.server_name")
		rec := httptest.NewRecorder()

		handler.HandleUpdateSetting(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.values["branding.server_name"] != "Casa" {
			t.Fatalf("branding.server_name = %q, want Casa", settings.values["branding.server_name"])
		}
	})
}

func TestAdminSensitiveStatusReportsNonSecretEnvironmentManagedSettings(t *testing.T) {
	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{values: map[string]string{}},
		BootstrapSensitiveConfigured: map[string]bool{
			"clientip.trusted_proxies": true,
		},
	}
	rec := httptest.NewRecorder()

	handler.HandleGetSensitiveStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/settings/sensitive-status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var response sensitiveStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.ManagedByEnv) != 1 || response.ManagedByEnv[0] != "clientip.trusted_proxies" {
		t.Fatalf("managed_by_env = %#v", response.ManagedByEnv)
	}
}

func TestAdminUpdateSettingsRejectsEnvironmentManagedSetting(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{
		SettingsRepo: settings,
		BootstrapSensitiveConfigured: map[string]bool{
			"clientip.trusted_proxies": true,
		},
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"clientip.trusted_proxies":"10.0.0.0/8"}}`),
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

func TestAdminUpdateSettingsCommitsOneValidatedBatch(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	restartStatus := NewServerRestartStatusTracker()
	handler := &AdminHandler{SettingsRepo: settings, RestartStatus: restartStatus}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"database.max_connections":" 40 ","branding.server_name":"Casa"}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 1 {
		t.Fatalf("SetMany calls = %d, want 1", settings.setManyCalls)
	}
	if settings.values["database.max_connections"] != "40" ||
		settings.values["branding.server_name"] != "Casa" {
		t.Fatalf("stored values = %#v", settings.values)
	}
	var response updateSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.RestartRequired ||
		len(response.RestartRequiredKeys) != 1 ||
		response.RestartRequiredKeys[0] != "database.max_connections" {
		t.Fatalf("response = %#v", response)
	}
}

func TestAdminUpdateSettingsRejectsWholeBatchBeforeWrite(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Silo"}}
	handler := &AdminHandler{SettingsRepo: settings}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"branding.server_name":"Casa","database.max_connections":"0"}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 0 {
		t.Fatalf("SetMany calls = %d, want 0", settings.setManyCalls)
	}
	if settings.values["branding.server_name"] != "Silo" {
		t.Fatalf("valid sibling value was partially persisted: %#v", settings.values)
	}
}

func TestAdminUpdateSettingsValidatesProspectiveDiagnosticsLimits(t *testing.T) {
	const (
		mib = 1024 * 1024
	)

	t.Run("rejects invalid final relationship", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			diagnostics.KeyMaxBundleBytes:       strconv.Itoa(10 * mib),
			diagnostics.KeyMaxUncompressedBytes: strconv.Itoa(64 * mib),
			diagnostics.KeyMaxBytesPerUser:      strconv.Itoa(200 * mib),
		}}
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(fmt.Sprintf(
				`{"values":{"%s":"%d","%s":"%d"}}`,
				diagnostics.KeyMaxBundleBytes,
				50*mib,
				diagnostics.KeyMaxUncompressedBytes,
				20*mib,
			)),
		)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 0 {
			t.Fatalf("invalid diagnostics batch wrote settings: SetMany=%d", settings.setManyCalls)
		}
	})

	t.Run("accepts valid paired repair", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			diagnostics.KeyMaxBundleBytes:       strconv.Itoa(64 * mib),
			diagnostics.KeyMaxUncompressedBytes: strconv.Itoa(128 * mib),
			diagnostics.KeyMaxBytesPerUser:      strconv.Itoa(200 * mib),
		}}
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(fmt.Sprintf(
				`{"values":{"%s":"%d","%s":"%d"}}`,
				diagnostics.KeyMaxBundleBytes,
				50*mib,
				diagnostics.KeyMaxUncompressedBytes,
				60*mib,
			)),
		)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 1 {
			t.Fatalf("valid diagnostics batch SetMany=%d, want 1", settings.setManyCalls)
		}
	})
}

func TestAdminGenericSettingsRoutesRejectUnsafeRateLimitValues(t *testing.T) {
	t.Run("batch", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(`{"values":{"ratelimit.global.requests_per_second":"1e308"}}`),
		)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 0 || settings.setCalls != 0 {
			t.Fatalf("invalid rate wrote settings: SetMany=%d Set=%d", settings.setManyCalls, settings.setCalls)
		}
	})

	t.Run("single", func(t *testing.T) {
		settings := &fakeServerSettingsStore{values: map[string]string{}}
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings/ratelimit.global.requests_per_second",
			strings.NewReader(`{"value":"1e308"}`),
		)
		req = withChiParam(req, "key", "ratelimit.global.requests_per_second")
		rec := httptest.NewRecorder()

		handler.HandleUpdateSetting(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 0 || settings.setCalls != 0 {
			t.Fatalf("invalid rate wrote settings: SetMany=%d Set=%d", settings.setManyCalls, settings.setCalls)
		}
	})
}

func TestAdminSettingsRejectClearingOnlyRedisTransport(t *testing.T) {
	newStore := func() *fakeServerSettingsStore {
		return &fakeServerSettingsStore{values: map[string]string{
			"ratelimit.backend": "redis",
			"redis.url":         "redis://cache.example.invalid:6379",
		}}
	}

	t.Run("batch", func(t *testing.T) {
		settings := newStore()
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(`{"values":{"redis.url":""}}`),
		)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 0 || settings.values["redis.url"] == "" {
			t.Fatalf("invalid clear was persisted: calls=%d values=%#v", settings.setManyCalls, settings.values)
		}
	})

	t.Run("single", func(t *testing.T) {
		settings := newStore()
		handler := &AdminHandler{SettingsRepo: settings}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings/redis.url",
			strings.NewReader(`{"value":""}`),
		)
		req = withChiParam(req, "key", "redis.url")
		rec := httptest.NewRecorder()

		handler.HandleUpdateSetting(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setCalls != 0 || settings.values["redis.url"] == "" {
			t.Fatalf("invalid clear was persisted: calls=%d values=%#v", settings.setCalls, settings.values)
		}
	})

	t.Run("bootstrap Sentinel", func(t *testing.T) {
		settings := newStore()
		handler := &AdminHandler{
			SettingsRepo:            settings,
			RedisBootstrapAvailable: true,
		}
		req := httptest.NewRequest(
			http.MethodPut,
			"/admin/settings",
			strings.NewReader(`{"values":{"redis.url":""}}`),
		)
		rec := httptest.NewRecorder()

		handler.HandleUpdateSettings(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		if settings.setManyCalls != 1 || settings.values["redis.url"] != "" {
			t.Fatalf("clear with bootstrap transport was not persisted: calls=%d values=%#v", settings.setManyCalls, settings.values)
		}
	})
}

func TestAdminSettingsRejectMalformedRedisURL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
		body   string
		single bool
	}{
		{name: "batch", target: "/admin/settings", body: `{"values":{"redis.url":"not-a-url"}}`},
		{name: "single", target: "/admin/settings/redis.url", body: `{"value":"not-a-url"}`, single: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: map[string]string{}}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(http.MethodPut, tc.target, strings.NewReader(tc.body))
			if tc.single {
				req = withChiParam(req, "key", "redis.url")
			}
			rec := httptest.NewRecorder()

			if tc.single {
				handler.HandleUpdateSetting(rec, req)
			} else {
				handler.HandleUpdateSettings(rec, req)
			}

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if settings.setManyCalls != 0 || settings.setCalls != 0 {
				t.Fatalf("malformed Redis URL was persisted: SetMany=%d Set=%d", settings.setManyCalls, settings.setCalls)
			}
		})
	}
}

func TestAdminUpdateSettingsSkipsFunctionalNoOp(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{}}
	handler := &AdminHandler{SettingsRepo: settings, RestartStatus: NewServerRestartStatusTracker()}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"database.max_connections":"20"}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 0 {
		t.Fatalf("SetMany calls = %d, want 0 for an effective-default no-op", settings.setManyCalls)
	}
	var response updateSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.RestartRequired || len(response.RestartRequiredKeys) != 0 {
		t.Fatalf("no-op response requested restart: %#v", response)
	}
}

func TestAdminUpdateSettingsPersistsClearWhenOverrideEqualsDefault(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Silo"}}
	handler := &AdminHandler{SettingsRepo: settings, RestartStatus: NewServerRestartStatusTracker()}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"branding.server_name":""}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 1 {
		t.Fatalf("SetMany calls = %d, want 1 for explicit clear", settings.setManyCalls)
	}
	if settings.values["branding.server_name"] != "" {
		t.Fatalf("stored value = %q, want cleared override", settings.values["branding.server_name"])
	}
	var response updateSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Values["branding.server_name"] != "Silo" || response.RestartRequired {
		t.Fatalf("response = %#v, want unchanged effective default without restart", response)
	}
}

func TestAdminUpdateSettingsReturnsEffectiveDefaultAfterClear(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Casa"}}
	var callbackValue string
	handler := &AdminHandler{
		SettingsRepo: settings,
		OnServerSettingUpdated: func(_ context.Context, _ string, value string) {
			callbackValue = value
		},
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings",
		strings.NewReader(`{"values":{"branding.server_name":""}}`),
	)
	rec := httptest.NewRecorder()

	handler.HandleUpdateSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var response updateSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Values["branding.server_name"] != "Silo" {
		t.Fatalf("response values = %#v, want effective default", response.Values)
	}
	if settings.values["branding.server_name"] != "" {
		t.Fatalf("stored value = %q, want cleared override", settings.values["branding.server_name"])
	}
	if callbackValue != "Silo" {
		t.Fatalf("callback value = %q, want effective default Silo", callbackValue)
	}
}

func TestAdminUpdateSettingReturnsEffectiveDefaultAfterClear(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Casa"}}
	var callbackValue string
	handler := &AdminHandler{
		SettingsRepo: settings,
		OnServerSettingUpdated: func(_ context.Context, _ string, value string) {
			callbackValue = value
		},
	}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/branding.server_name",
		strings.NewReader(`{"value":""}`),
	)
	req = withChiParam(req, "key", "branding.server_name")
	rec := httptest.NewRecorder()

	handler.HandleUpdateSetting(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var response adminSettingResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Value != "Silo" {
		t.Fatalf("response value = %q, want effective default", response.Value)
	}
	if callbackValue != "Silo" {
		t.Fatalf("callback value = %q, want effective default Silo", callbackValue)
	}
}

func TestAdminUpdateSettingPersistsClearWhenOverrideEqualsDefault(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{"branding.server_name": "Silo"}}
	handler := &AdminHandler{SettingsRepo: settings}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/branding.server_name",
		strings.NewReader(`{"value":""}`),
	)
	req = withChiParam(req, "key", "branding.server_name")
	rec := httptest.NewRecorder()

	handler.HandleUpdateSetting(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if settings.setManyCalls != 1 {
		t.Fatalf("atomic writes = %d, want 1 for explicit clear", settings.setManyCalls)
	}
	if settings.values["branding.server_name"] != "" {
		t.Fatalf("stored value = %q, want cleared override", settings.values["branding.server_name"])
	}
	var response adminSettingResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Value != "Silo" || response.RestartRequired {
		t.Fatalf("response = %#v, want unchanged effective default without restart", response)
	}
}

func TestAdminUpdateSettingPreservesLegacyPairedWriteFlow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial map[string]string
		key     string
		value   string
	}{
		{
			name:  "establish first half of pair",
			key:   "s3.public_endpoint",
			value: "https://s3.example.invalid",
		},
		{
			name: "unrelated update with legacy partial pair",
			initial: map[string]string{
				"s3.public_endpoint": "https://s3.example.invalid",
			},
			key:   "branding.server_name",
			value: "Casa",
		},
		{
			name: "clear first half of pair",
			initial: map[string]string{
				"watchsync.trakt.client_id":     "configured-client-id",
				"watchsync.trakt.client_secret": "clawrouter-e2e-secret",
			},
			key:   "watchsync.trakt.client_id",
			value: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: tc.initial}
			if settings.values == nil {
				settings.values = map[string]string{}
			}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+tc.key,
				strings.NewReader(`{"value":"`+tc.value+`"}`),
			)
			req = withChiParam(req, "key", tc.key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values[tc.key] != tc.value {
				t.Fatalf("stored %s = %q, want %q", tc.key, settings.values[tc.key], tc.value)
			}
		})
	}
}

func (f *fakeServerSettingsStore) GetAll(context.Context) (map[string]string, error) {
	cloned := make(map[string]string, len(f.values))
	for key, value := range f.values {
		cloned[key] = value
	}
	return cloned, nil
}

type fakeS3SettingsCheckClient struct {
	headBucket func(ctx context.Context, bucket string) error
	putObject  func(ctx context.Context, bucket, key string, data []byte) error
	getObject  func(ctx context.Context, bucket, key string) ([]byte, error)
	delete     func(ctx context.Context, bucket, key string) error
	objects    map[string][]byte
}

func (f *fakeS3SettingsCheckClient) PutObject(
	ctx context.Context,
	bucket,
	key string,
	data []byte,
) error {
	if f.putObject != nil {
		return f.putObject(ctx, bucket, key, data)
	}
	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[key] = append([]byte(nil), data...)
	return nil
}

func (f *fakeS3SettingsCheckClient) GetObject(
	ctx context.Context,
	bucket,
	key string,
) ([]byte, error) {
	if f.getObject != nil {
		return f.getObject(ctx, bucket, key)
	}
	return append([]byte(nil), f.objects[key]...), nil
}

func (f *fakeS3SettingsCheckClient) DeleteObject(ctx context.Context, bucket, key string) error {
	if f.delete != nil {
		return f.delete(ctx, bucket, key)
	}
	delete(f.objects, key)
	return nil
}

func (f *fakeS3SettingsCheckClient) HeadBucket(ctx context.Context, bucket string) error {
	if f.headBucket != nil {
		return f.headBucket(ctx, bucket)
	}
	return nil
}

func TestCheckS3ObjectPermissionsCleansUpAfterAmbiguousPutFailure(t *testing.T) {
	client := &fakeS3SettingsCheckClient{}
	client.putObject = func(_ context.Context, _, key string, data []byte) error {
		if client.objects == nil {
			client.objects = make(map[string][]byte)
		}
		client.objects[key] = append([]byte(nil), data...)
		return errors.New("response lost")
	}

	err := checkS3ObjectPermissions(context.Background(), client, "silo")

	if err == nil || !strings.Contains(err.Error(), "write probe object: response lost") {
		t.Fatalf("error = %v, want ambiguous write failure", err)
	}
	if len(client.objects) != 0 {
		t.Fatalf("probe objects = %#v, want ambiguous write cleaned up", client.objects)
	}
}

func TestCheckS3ObjectPermissionsUsesFreshContextForFailureCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var cleanupContextErr error
	client := &fakeS3SettingsCheckClient{
		getObject: func(context.Context, string, string) ([]byte, error) {
			cancel()
			return nil, context.Canceled
		},
		delete: func(ctx context.Context, _, _ string) error {
			cleanupContextErr = ctx.Err()
			return nil
		},
	}

	err := checkS3ObjectPermissions(ctx, client, "silo")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if cleanupContextErr != nil {
		t.Fatalf("cleanup context error = %v, want live cleanup context", cleanupContextErr)
	}
}

func TestCheckS3ObjectPermissionsSurfacesCleanupFailure(t *testing.T) {
	client := &fakeS3SettingsCheckClient{
		getObject: func(context.Context, string, string) ([]byte, error) {
			return nil, errors.New("read failed")
		},
		delete: func(context.Context, string, string) error {
			return errors.New("delete failed")
		},
	}

	err := checkS3ObjectPermissions(context.Background(), client, "silo")

	if err == nil || !strings.Contains(err.Error(), "cleanup probe object: delete failed") {
		t.Fatalf("error = %v, want cleanup failure", err)
	}
	if !strings.Contains(err.Error(), "read probe object: read failed") {
		t.Fatalf("error = %v, want original read failure", err)
	}
}

type fakeRedisSettingsCheckClient struct {
	ping func(ctx context.Context) error
}

func (f *fakeRedisSettingsCheckClient) Ping(ctx context.Context) error {
	if f.ping != nil {
		return f.ping(ctx)
	}
	return nil
}

func (f *fakeRedisSettingsCheckClient) Close() error {
	return nil
}

type fakeEmbeddingsSettingsCheckClient struct {
	embed func(ctx context.Context, texts []string) ([][]float32, error)
}

type fakeMDBListSettingsCheckClient struct {
	check func(context.Context) error
}

func (f *fakeMDBListSettingsCheckClient) Check(ctx context.Context) error {
	if f.check != nil {
		return f.check(ctx)
	}
	return nil
}

func (f *fakeEmbeddingsSettingsCheckClient) Embed(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, texts)
	}
	return [][]float32{{0.1, 0.2}}, nil
}

type fakeAISettingsCheckClient struct {
	chat       func(ctx context.Context, messages []llm.Message, jsonObject bool) (string, error)
	transcribe func(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error)
}

func (f *fakeAISettingsCheckClient) Chat(
	ctx context.Context,
	messages []llm.Message,
	jsonObject bool,
) (string, error) {
	if f.chat != nil {
		return f.chat(ctx, messages, jsonObject)
	}
	return `{"status":"ok"}`, nil
}

func (f *fakeAISettingsCheckClient) Transcribe(
	ctx context.Context,
	req llm.TranscribeRequest,
) (*llm.Transcription, error) {
	if f.transcribe != nil {
		return f.transcribe(ctx, req)
	}
	return &llm.Transcription{}, nil
}

func TestHandleCheckSettingsConnectionAIChatDoesNotSendStoredKeyToDraftEndpoint(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() {
		newAdminAISettingsCheckClient = originalFactory
	})

	var captured llm.Config
	var chatCalled bool
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		captured = cfg
		return &fakeAISettingsCheckClient{
			chat: func(_ context.Context, _ []llm.Message, jsonObject bool) (string, error) {
				chatCalled = true
				if !jsonObject {
					t.Fatal("chat connection check did not request a JSON response")
				}
				return `{"status":"ok"}`, nil
			},
		}
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"ai.base_url":   "https://persisted.example.test",
				"ai.chat_model": "persisted-model",
				"ai.api_key":    "persisted-secret",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/ai_chat",
		map[string]any{
			"values": map[string]string{
				"ai.base_url":   "https://draft.example.test",
				"ai.chat_model": "draft-model",
				"ai.api_key":    "",
			},
			"dirty_keys": []string{"ai.base_url", "ai.chat_model"},
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var response connectionCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if !response.Success || !chatCalled {
		t.Fatalf("response = %+v, chatCalled = %v", response, chatCalled)
	}
	if captured.BaseURL != "https://draft.example.test" || captured.ChatModel != "draft-model" {
		t.Fatalf("captured config = %+v, want draft endpoint and model", captured)
	}
	if captured.APIKey != "" {
		t.Fatalf("captured API key = %q, want no stored secret for a changed endpoint", captured.APIKey)
	}
}

func TestHandleCheckSettingsConnectionAIChatReusesStoredKeyForSameEndpoint(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() {
		newAdminAISettingsCheckClient = originalFactory
	})

	var captured llm.Config
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		captured = cfg
		return &fakeAISettingsCheckClient{}
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"ai.base_url":   "https://persisted.example.test/v1",
				"ai.chat_model": "persisted-model",
				"ai.api_key":    "persisted-secret",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/ai_chat",
		map[string]any{
			"values": map[string]string{
				"ai.base_url":   "https://persisted.example.test/other-path",
				"ai.chat_model": "draft-model",
			},
			"dirty_keys": []string{"ai.base_url", "ai.chat_model"},
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured.APIKey != "persisted-secret" {
		t.Fatalf("captured API key = %q, want stored secret for unchanged authority", captured.APIKey)
	}
}

func TestHandleCheckSettingsConnectionAITranscriptionUsesDedicatedASR(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() {
		newAdminAISettingsCheckClient = originalFactory
	})

	var captured llm.Config
	var request llm.TranscribeRequest
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		captured = cfg
		return &fakeAISettingsCheckClient{
			transcribe: func(_ context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
				request = req
				return &llm.Transcription{}, nil
			},
		}
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"ai.base_url":     "https://chat.example.test",
				"ai.api_key":      "chat-secret",
				"ai.chat_model":   "chat-model",
				"ai.asr_base_url": "https://whisper.example.test",
				"ai.asr_api_key":  "asr-secret",
				"ai.asr_model":    "whisper-model",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/ai_transcription",
		map[string]any{"values": map[string]string{}, "dirty_keys": []string{}},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var response connectionCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if !response.Success {
		t.Fatalf("response.Success = false, message = %q", response.Message)
	}
	if captured.ASRBaseURL != "https://whisper.example.test" ||
		captured.ASRAPIKey != "asr-secret" ||
		captured.ASRModel != "whisper-model" {
		t.Fatalf("captured ASR config = %+v", captured)
	}
	if len(request.Audio) == 0 || request.Filename == "" || request.Timeout <= 0 {
		t.Fatalf("transcription probe request = %+v, want bounded WAV probe", request)
	}
}

func TestHandleCheckSettingsConnectionAITranscriptionDoesNotSendStoredKeysToDraftEndpoint(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() {
		newAdminAISettingsCheckClient = originalFactory
	})

	var captured llm.Config
	newAdminAISettingsCheckClient = func(cfg llm.Config) aiSettingsCheckClient {
		captured = cfg
		return &fakeAISettingsCheckClient{}
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"ai.base_url":     "https://chat.example.test",
				"ai.api_key":      "chat-secret",
				"ai.chat_model":   "chat-model",
				"ai.asr_base_url": "https://stored-whisper.example.test",
				"ai.asr_api_key":  "asr-secret",
				"ai.asr_model":    "whisper-model",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/ai_transcription",
		map[string]any{
			"values": map[string]string{
				"ai.asr_base_url": "https://draft-whisper.example.test",
			},
			"dirty_keys": []string{"ai.asr_base_url"},
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if captured.ASRAPIKey != "" || captured.APIKey != "" {
		t.Fatalf("captured config = %+v, want no stored ASR or inherited chat secret", captured)
	}
}

func TestHandleCheckSettingsConnectionS3UsesPersistedSensitiveValues(t *testing.T) {
	originalFactory := newAdminS3SettingsCheckClient
	t.Cleanup(func() {
		newAdminS3SettingsCheckClient = originalFactory
	})

	var captured s3client.BucketConfig
	newAdminS3SettingsCheckClient = func(cfg s3client.BucketConfig) s3SettingsCheckClient {
		captured = cfg
		return &fakeS3SettingsCheckClient{}
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"s3.public_endpoint":   "https://persisted.example.test",
				"s3.public_bucket":     "silo",
				"s3.public_key_prefix": "persisted/prefix",
				"s3.public_access_key": "persisted-access",
				"s3.public_secret_key": "persisted-secret",
			},
		},
	}

	body := map[string]any{
		"values": map[string]string{
			"s3.public_endpoint":   "https://draft.example.test",
			"s3.public_access_key": "",
			"s3.public_secret_key": "",
		},
		"dirty_keys": []string{"s3.public_endpoint"},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/s3_public",
		body,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var response connectionCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if !response.Success {
		t.Fatalf("response.Success = false, want true (message=%q)", response.Message)
	}
	if captured.Endpoint != "https://draft.example.test" {
		t.Fatalf("captured endpoint = %q, want draft endpoint", captured.Endpoint)
	}
	if captured.KeyPrefix != "persisted/prefix" {
		t.Fatalf("captured key prefix = %q, want persisted/prefix", captured.KeyPrefix)
	}
	if captured.AccessKey != "persisted-access" {
		t.Fatalf("captured access key = %q, want persisted-access", captured.AccessKey)
	}
	if captured.SecretKey != "persisted-secret" {
		t.Fatalf("captured secret key = %q, want persisted-secret", captured.SecretKey)
	}
}

func TestHandleCheckSettingsConnectionMDBListUsesDraftOrSavedKey(t *testing.T) {
	originalFactory := newAdminMDBListSettingsCheckClient
	t.Cleanup(func() { newAdminMDBListSettingsCheckClient = originalFactory })
	var captured []string
	newAdminMDBListSettingsCheckClient = func(apiKey string) mdblistSettingsCheckClient {
		captured = append(captured, apiKey)
		return &fakeMDBListSettingsCheckClient{}
	}
	handler := &AdminHandler{SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
		"mdblist.api_key": "saved-key",
	}}}

	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "saved key when draft is blank and untouched",
			body: map[string]any{"values": map[string]string{"mdblist.api_key": ""}, "dirty_keys": []string{}},
			want: "saved-key",
		},
		{
			name: "unsaved draft key",
			body: map[string]any{"values": map[string]string{"mdblist.api_key": "draft-key"}, "dirty_keys": []string{"mdblist.api_key"}},
			want: "draft-key",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := performSettingsCheckRequest(t, handler, "/admin/settings/check/mdblist", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var response connectionCheckResponse
			if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
				t.Fatal(err)
			}
			if !response.Success {
				t.Fatalf("connection check failed: %s", response.Message)
			}
			if got := captured[len(captured)-1]; got != tc.want {
				t.Fatalf("factory API key = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHandleCheckSettingsConnectionRedisHonorsExplicitClear(t *testing.T) {
	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"redis.url": "redis://persisted:6379",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/redis",
		map[string]any{
			"values": map[string]string{
				"redis.url": "",
			},
			"dirty_keys": []string{"redis.url"},
		},
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var response connectionCheckResponse
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if response.Success {
		t.Fatalf("response.Success = true, want false")
	}
	if !strings.Contains(response.Message, "Redis URL is required") {
		t.Fatalf("message = %q, want Redis URL validation", response.Message)
	}
}

func TestHandleCheckSettingsConnectionRejectsInvalidDraftValues(t *testing.T) {
	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"s3.public_endpoint": "https://persisted.example.test",
				"s3.public_bucket":   "silo",
			},
		},
	}

	rec := performSettingsCheckRequest(
		t,
		handler,
		"/admin/settings/check/s3_public",
		map[string]any{
			"values": map[string]string{
				"s3.public_token_ttl": "not-a-number",
			},
			"dirty_keys": []string{"s3.public_token_ttl"},
		},
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	var response map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("Decode() returned error: %v", err)
	}
	if !strings.Contains(response["message"], "invalid int for") {
		t.Fatalf("message = %q, want parse failure", response["message"])
	}
}

func TestSettingsCheckRouteIsNotShadowedByKeyRoute(t *testing.T) {
	originalFactory := newAdminRedisSettingsCheckClient
	t.Cleanup(func() {
		newAdminRedisSettingsCheckClient = originalFactory
	})

	newAdminRedisSettingsCheckClient = func(cfg config.RedisConfig) (redisSettingsCheckClient, error) {
		return &fakeRedisSettingsCheckClient{}, nil
	}

	handler := &AdminHandler{
		SettingsRepo: &fakeServerSettingsStore{
			values: map[string]string{
				"redis.url": "redis://cache:6379",
			},
		},
	}

	router := chi.NewRouter()
	router.Post("/admin/settings/check/{kind}", handler.HandleCheckSettingsConnection)
	router.Get("/admin/settings/{key}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})

	body, err := json.Marshal(map[string]any{
		"values": map[string]string{
			"redis.url": "redis://cache:6379",
		},
		"dirty_keys": []string{"redis.url"},
	})
	if err != nil {
		t.Fatalf("Marshal() returned error: %v", err)
	}

	req := httptest.NewRequest(
		http.MethodPost,
		"/admin/settings/check/redis",
		bytes.NewReader(body),
	)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAdminGetSettingRedactsSensitiveSetting(t *testing.T) {
	for _, key := range []string{"watchsync.trakt.client_secret", "watchsync.simkl.client_secret"} {
		t.Run(key, func(t *testing.T) {
			const storedSecret = "stored-watch-provider-secret"
			handler := &AdminHandler{
				SettingsRepo: &fakeServerSettingsStore{
					values: map[string]string{
						key: storedSecret,
					},
				},
			}

			req := httptest.NewRequest(http.MethodGet, "/admin/settings/"+key, nil)
			req = withChiParam(req, "key", key)
			rec := httptest.NewRecorder()

			handler.HandleGetSetting(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), storedSecret) {
				t.Fatalf("response leaked sensitive value: %s", rec.Body.String())
			}
		})
	}
}

func TestAdminUpdateSettingRedactsSensitiveSetting(t *testing.T) {
	for _, key := range []string{"watchsync.trakt.client_secret", "watchsync.simkl.client_secret"} {
		t.Run(key, func(t *testing.T) {
			const submittedSecret = "submitted-watch-provider-secret"
			settings := &fakeServerSettingsStore{}
			handler := &AdminHandler{SettingsRepo: settings}

			providerPrefix := strings.TrimSuffix(key, ".client_secret")
			settings.values = map[string]string{providerPrefix + ".client_id": "configured-client-id"}

			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+key,
				strings.NewReader(`{"value":"`+submittedSecret+`"}`),
			)
			req = withChiParam(req, "key", key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values[key] != submittedSecret {
				t.Fatalf("stored value = %q, want submitted secret", settings.values[key])
			}
			if strings.Contains(rec.Body.String(), submittedSecret) {
				t.Fatalf("response leaked sensitive value: %s", rec.Body.String())
			}

			var resp adminSettingResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.Key != key || resp.Value != "" {
				t.Fatalf("response = %+v, want key with empty value", resp)
			}
		})
	}
}

func TestAdminUpdateSettingReportsRestartRequired(t *testing.T) {
	cases := []struct {
		key             string
		value           string
		restartRequired bool
	}{
		// Infrastructure settings are captured at startup.
		{key: "database.max_connections", value: "40", restartRequired: true},
		{key: "s3.public_bucket", value: "assets", restartRequired: true},
		{key: "jellyfin_compat.enabled", value: "false", restartRequired: true},
		// Branding is read live from the settings repo per request.
		{key: "branding.server_name", value: "Casa", restartRequired: false},
		{key: "policy.editor_enabled", value: "true", restartRequired: false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			initial := map[string]string{}
			if tc.key == "s3.public_bucket" {
				initial["s3.public_endpoint"] = "https://s3.example.test"
			}
			settings := &fakeServerSettingsStore{values: initial}
			restartStatus := NewServerRestartStatusTracker()
			handler := &AdminHandler{SettingsRepo: settings, RestartStatus: restartStatus}

			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+tc.key,
				strings.NewReader(`{"value":"`+tc.value+`"}`),
			)
			req = withChiParam(req, "key", tc.key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			var resp adminSettingResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if resp.RestartRequired != tc.restartRequired {
				t.Fatalf("restart_required = %v, want %v", resp.RestartRequired, tc.restartRequired)
			}
			snapshot := restartStatus.Snapshot()
			if snapshot.RestartRequired != tc.restartRequired {
				t.Fatalf("tracker restart required = %v, want %v", snapshot.RestartRequired, tc.restartRequired)
			}
			if !tc.restartRequired && snapshot.RestartRequiredReason != "" {
				t.Fatalf("tracker reason = %q, want empty", snapshot.RestartRequiredReason)
			}
		})
	}
}

func TestAdminUpdateSettingNormalizesAIEndpointAndModelValues(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value string
		want  string
	}{
		{key: "ai.base_url", value: "  https://text.example.test/v1  ", want: "https://text.example.test/v1"},
		{key: "ai.chat_model", value: "  chat-model  ", want: "chat-model"},
		{key: "ai.asr_base_url", value: "  https://speech.example.test  ", want: "https://speech.example.test"},
		{key: "ai.asr_model", value: "  whisper-model  ", want: "whisper-model"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			settings := &fakeServerSettingsStore{}
			handler := &AdminHandler{SettingsRepo: settings}
			body, err := json.Marshal(updateSettingRequest{Value: tc.value})
			if err != nil {
				t.Fatalf("Marshal() returned error: %v", err)
			}
			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+tc.key,
				bytes.NewReader(body),
			)
			req = withChiParam(req, "key", tc.key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if settings.values[tc.key] != tc.want {
				t.Fatalf("stored value = %q, want %q", settings.values[tc.key], tc.want)
			}
		})
	}
}

func TestAdminUpdatePolicyEditorEnabledValidation(t *testing.T) {
	settings := &fakeServerSettingsStore{}
	handler := &AdminHandler{SettingsRepo: settings}
	req := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/policy.editor_enabled",
		strings.NewReader(`{"value":"maybe"}`),
	)
	req = withChiParam(req, "key", "policy.editor_enabled")
	rec := httptest.NewRecorder()

	handler.HandleUpdateSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := settings.values["policy.editor_enabled"]; ok {
		t.Fatalf("invalid policy.editor_enabled was stored: %#v", settings.values)
	}
}

func TestAdminUpdateCatalogSearchSemanticSettingsValidation(t *testing.T) {
	valid := []struct {
		key   string
		value string
		want  string
	}{
		{key: catalog.SearchSettingMeilisearchSemanticEnabled, value: "TRUE", want: "true"},
		{key: catalog.SearchSettingMeilisearchSemanticRatio, value: ".30", want: "0.3"},
		{key: catalog.SearchSettingMeilisearchEmbedder, value: "", want: catalog.DefaultMeilisearchEmbedder},
		{key: catalog.SearchSettingMeilisearchEmbedder, value: "custom_embedder-2", want: "custom_embedder-2"},
	}
	for _, tc := range valid {
		t.Run("valid "+tc.key, func(t *testing.T) {
			settings := &fakeServerSettingsStore{}
			handler := &AdminHandler{SettingsRepo: settings, RestartStatus: NewServerRestartStatusTracker()}
			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+tc.key,
				strings.NewReader(`{"value":"`+tc.value+`"}`),
			)
			req = withChiParam(req, "key", tc.key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if got := config.EffectiveAdminSettings(settings.values)[tc.key]; got != tc.want {
				t.Fatalf("effective value = %q, want %q", got, tc.want)
			}
		})
	}

	invalid := []struct {
		key   string
		value string
	}{
		{key: catalog.SearchSettingMeilisearchSemanticEnabled, value: "sometimes"},
		{key: catalog.SearchSettingMeilisearchSemanticRatio, value: "-0.01"},
		{key: catalog.SearchSettingMeilisearchSemanticRatio, value: "1.01"},
		{key: catalog.SearchSettingMeilisearchSemanticRatio, value: "NaN"},
		{key: catalog.SearchSettingMeilisearchEmbedder, value: "bad.name"},
	}
	for _, tc := range invalid {
		t.Run("invalid "+tc.key, func(t *testing.T) {
			settings := &fakeServerSettingsStore{}
			handler := &AdminHandler{SettingsRepo: settings}
			req := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+tc.key,
				strings.NewReader(`{"value":"`+tc.value+`"}`),
			)
			req = withChiParam(req, "key", tc.key)
			rec := httptest.NewRecorder()

			handler.HandleUpdateSetting(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if _, ok := settings.values[tc.key]; ok {
				t.Fatalf("invalid setting was stored: %#v", settings.values)
			}
		})
	}
}

func performSettingsCheckRequest(
	t *testing.T,
	handler *AdminHandler,
	path string,
	body map[string]any,
) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Marshal() returned error: %v", err)
	}

	router := chi.NewRouter()
	router.Post("/admin/settings/check/{kind}", handler.HandleCheckSettingsConnection)

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func withChiParam(r *http.Request, key, value string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}
