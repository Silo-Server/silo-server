package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/ai/llm"
	"github.com/Silo-Server/silo-server/internal/config"
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
		"server.log_level":         "debug",
		"tmdb.api_key":             "never-return-this",
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
	if got := values["clientip.trusted_proxies"]; got != "192.0.2.0/24, 2001:db8::/32" {
		t.Fatalf("clientip.trusted_proxies = %q, want active environment value", got)
	}
	if _, leaked := values["redis.url"]; leaked {
		t.Fatal("effective settings response leaked environment-managed redis.url")
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

func TestAdminSettingsValidationIncludesEnvironmentManagedValues(t *testing.T) {
	newHandler := func() (*AdminHandler, *fakeServerSettingsStore) {
		settings := &fakeServerSettingsStore{values: map[string]string{
			"watchsync.simkl.client_id": "configured-client-id",
		}}
		return &AdminHandler{
			SettingsRepo: settings,
			BootstrapSensitiveConfigured: map[string]bool{
				"watchsync.simkl.client_secret": true,
			},
			BootstrapSensitiveValues: map[string]string{
				"watchsync.simkl.client_secret": "clawrouter-e2e-secret",
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

func TestAdminUpdateSettingValidatesRoutingAgainstStoredPeer(t *testing.T) {
	for _, test := range []struct {
		name      string
		storedKey string
		stored    string
		key       string
		value     string
	}{
		{
			name: "remux execution against proxy-only egress", storedKey: config.PlaybackRoutingRemuxEgressSettingKey,
			stored: string(config.PlaybackEgressProxyOnly), key: config.PlaybackRoutingRemuxExecutionSettingKey,
			value: string(config.PlaybackExecutionAPIOnly),
		},
		{
			name: "remux egress against API-only execution", storedKey: config.PlaybackRoutingRemuxExecutionSettingKey,
			stored: string(config.PlaybackExecutionAPIOnly), key: config.PlaybackRoutingRemuxEgressSettingKey,
			value: string(config.PlaybackEgressProxyOnly),
		},
		{
			name: "video execution against proxy-only egress", storedKey: config.PlaybackRoutingVideoTranscodeEgressSettingKey,
			stored: string(config.PlaybackEgressProxyOnly), key: config.PlaybackRoutingVideoTranscodeExecutionSettingKey,
			value: string(config.PlaybackExecutionAPIOnly),
		},
		{
			name: "video egress against API-only execution", storedKey: config.PlaybackRoutingVideoTranscodeExecutionSettingKey,
			stored: string(config.PlaybackExecutionAPIOnly), key: config.PlaybackRoutingVideoTranscodeEgressSettingKey,
			value: string(config.PlaybackEgressProxyOnly),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := &fakeServerSettingsStore{values: map[string]string{test.storedKey: test.stored}}
			handler := &AdminHandler{SettingsRepo: settings}
			request := httptest.NewRequest(
				http.MethodPut,
				"/admin/settings/"+test.key,
				strings.NewReader(`{"value":"`+test.value+`"}`),
			)
			request = withChiParam(request, "key", test.key)
			recorder := httptest.NewRecorder()

			handler.HandleUpdateSetting(recorder, request)

			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"error":"invalid_settings"`) {
				t.Fatalf("response = %d %s, want invalid-settings rejection", recorder.Code, recorder.Body.String())
			}
			if settings.atomicCalls != 1 || settings.setManyCalls != 0 {
				t.Fatalf("atomic calls=%d writes=%d, want 1 and 0", settings.atomicCalls, settings.setManyCalls)
			}
			if _, exists := settings.values[test.key]; exists {
				t.Fatalf("invalid routing setting was persisted: %#v", settings.values)
			}
			if _, err := config.LoadFromDB(settings.values); err != nil {
				t.Fatalf("rejected write left restart-invalid settings: %v", err)
			}
		})
	}
}

func TestAdminUpdateSettingCanRepairLegacyInvalidRoutingPair(t *testing.T) {
	settings := &fakeServerSettingsStore{values: map[string]string{
		config.PlaybackRoutingRemuxExecutionSettingKey: string(config.PlaybackExecutionAPIOnly),
		config.PlaybackRoutingRemuxEgressSettingKey:    string(config.PlaybackEgressProxyOnly),
	}}
	handler := &AdminHandler{SettingsRepo: settings}
	request := httptest.NewRequest(
		http.MethodPut,
		"/admin/settings/"+config.PlaybackRoutingRemuxExecutionSettingKey,
		strings.NewReader(`{"value":"worker_only"}`),
	)
	request = withChiParam(request, "key", config.PlaybackRoutingRemuxExecutionSettingKey)
	recorder := httptest.NewRecorder()

	handler.HandleUpdateSetting(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("response = %d %s, want repair accepted", recorder.Code, recorder.Body.String())
	}
	if _, err := config.LoadFromDB(settings.values); err != nil {
		t.Fatalf("repaired settings are restart-invalid: %v", err)
	}
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

	t.Run("bootstrap Redis URL", func(t *testing.T) {
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
				"watchsync.simkl.client_id":     "configured-client-id",
				"watchsync.simkl.client_secret": "clawrouter-e2e-secret",
			},
			key:   "watchsync.simkl.client_id",
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
	cleanupCalled := false
	client := &fakeS3SettingsCheckClient{
		getObject: func(context.Context, string, string) ([]byte, error) {
			cancel()
			return nil, context.Canceled
		},
		delete: func(ctx context.Context, _, _ string) error {
			cleanupCalled = true
			cleanupContextErr = ctx.Err()
			return nil
		},
	}

	err := checkS3ObjectPermissions(ctx, client, "silo")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if !cleanupCalled || cleanupContextErr != nil {
		t.Fatalf("cleanup called = %v, context error = %v; want cleanup on a live context", cleanupCalled, cleanupContextErr)
	}
}

type fakeEmbeddingsSettingsCheckClient struct {
	embed func(ctx context.Context, texts []string) ([][]float32, error)
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
	return &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Subtitle test."}}}, nil
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
			transcribe: func(ctx context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
				request = req
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) < req.Timeout || time.Until(deadline) > 80*time.Second {
					t.Fatal("outer check deadline does not cover the transcription request")
				}
				return &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Subtitle test."}}}, nil
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
	if len(request.Audio) == 0 || request.Filename == "" || request.Timeout <= 60*time.Second || request.Timeout > 75*time.Second {
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

func TestAdminGetSettingRedactsSensitiveSetting(t *testing.T) {
	for _, key := range []string{"watchsync.simkl.client_secret"} {
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
	for _, key := range []string{"watchsync.simkl.client_secret"} {
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

func TestAdminBatchAllowsOpenRouterASR(t *testing.T) {
	settings := &fakeServerSettingsStore{}
	handler := &AdminHandler{SettingsRepo: settings}
	rec := httptest.NewRecorder()
	handler.HandleUpdateSettings(rec, httptest.NewRequest(http.MethodPut, "/admin/settings", strings.NewReader(`{"values":{"ai.asr_base_url":"https://openrouter.ai/api/v1","ai.asr_model":"openai/whisper-large-v3","subtitle_ai.transcribe_enabled":"true"}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body = %s", rec.Code, rec.Body.String())
	}
	if settings.values["ai.asr_base_url"] != "https://openrouter.ai/api/v1" || settings.values["subtitle_ai.transcribe_enabled"] != "true" {
		t.Fatalf("settings not saved: %+v", settings.values)
	}
}

func TestAITranscriptionConnectionRequiresTimestampedSpeech(t *testing.T) {
	originalFactory := newAdminAISettingsCheckClient
	t.Cleanup(func() { newAdminAISettingsCheckClient = originalFactory })
	for _, tc := range []struct {
		name   string
		result *llm.Transcription
		want   bool
	}{
		{name: "nil response"},
		{name: "empty segments", result: &llm.Transcription{}},
		{name: "blank text", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: " "}}}},
		{name: "no timestamps", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Text: "Test"}}}},
		{name: "timestamped speech", result: &llm.Transcription{Segments: []llm.TranscriptionSegment{{Start: 0, End: 1, Text: "Test"}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newAdminAISettingsCheckClient = func(llm.Config) aiSettingsCheckClient {
				return &fakeAISettingsCheckClient{transcribe: func(_ context.Context, req llm.TranscribeRequest) (*llm.Transcription, error) {
					if !bytes.Equal(req.Audio[:4], []byte("RIFF")) || len(bytes.Trim(req.Audio[44:], "\x00")) == 0 {
						t.Fatal("connection probe must contain audio, not silence")
					}
					return tc.result, nil
				}}
			}
			cfg := &config.Config{}
			cfg.AI.BaseURL = "https://openrouter.ai/api/v1"
			cfg.AI.ASRModel = "openai/whisper-large-v3"
			if got := checkAITranscriptionConnection(t.Context(), cfg); got.Success != tc.want {
				t.Fatalf("connection result = %+v, want success = %v", got, tc.want)
			}
		})
	}
}
