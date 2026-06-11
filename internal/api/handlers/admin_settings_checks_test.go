package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

type fakeServerSettingsStore struct {
	values map[string]string
}

func (f *fakeServerSettingsStore) Get(_ context.Context, key string) (string, error) {
	return f.values[key], nil
}

func (f *fakeServerSettingsStore) Set(_ context.Context, key, value string) error {
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[key] = value
	return nil
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
}

func (f *fakeS3SettingsCheckClient) HeadBucket(ctx context.Context, bucket string) error {
	if f.headBucket != nil {
		return f.headBucket(ctx, bucket)
	}
	return nil
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

func (f *fakeEmbeddingsSettingsCheckClient) Embed(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if f.embed != nil {
		return f.embed(ctx, texts)
	}
	return [][]float32{{0.1, 0.2}}, nil
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
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			settings := &fakeServerSettingsStore{}
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
