package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
)

func TestAdminServerStatusClearsAfterProcessRestart(t *testing.T) {
	t.Parallel()

	restartStatus := NewServerRestartStatusTracker()
	restartStatus.MarkRequired("settings")

	handler := &AdminHandler{RestartStatus: restartStatus}
	req := httptest.NewRequest(http.MethodGet, "/admin/server/status", nil)
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.RestartRequired {
		t.Fatal("RestartRequired = false, want true")
	}

	restartedHandler := &AdminHandler{RestartStatus: NewServerRestartStatusTracker()}
	rec = httptest.NewRecorder()
	restartedHandler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status after restart = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response after restart: %v", err)
	}
	if resp.RestartRequired {
		t.Fatal("RestartRequired = true after new process tracker, want false")
	}
}

func TestAdminServerStatusDoesNotPromoteLiveJellyfinIdentitySettings(t *testing.T) {
	t.Parallel()

	cfg, err := config.LoadFromDB(map[string]string{
		"jellyfin_compat.enabled":                 "true",
		"jellyfin_compat.listen":                  ":8096",
		"jellyfin_compat.public_url":              "http://127.0.0.1:8096",
		"jellyfin_compat.server_name":             "Silo",
		"jellyfin_compat.emulated_server_version": "10.11.0",
	})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}

	handler := &AdminHandler{
		Config:        cfg,
		RestartStatus: NewServerRestartStatusTracker(),
		SettingsRepo: &fakeServerSettingsStore{values: map[string]string{
			"jellyfin_compat.public_url":              "https://compat.example.test",
			"jellyfin_compat.server_name":             "Silo Compat",
			"jellyfin_compat.emulated_server_version": "10.11.6",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/admin/server/status", nil)
	rec := httptest.NewRecorder()

	handler.HandleGetServerStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminServerStatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.RestartRequired {
		t.Fatal("RestartRequired = true, want false for live Jellyfin identity settings")
	}
}

// A server with neither a pool nor a Redis client must still answer 200 with a
// well-formed health object: the dashboard health strip is the one place an
// admin can see that a dependency is missing.
func TestAdminServerStatusHealthWithoutDependencies(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{RestartStatus: NewServerRestartStatusTracker()}
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/server/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Health struct {
			Postgres map[string]any `json:"postgres"`
			Redis    map[string]any `json:"redis"`
			Errors   int64          `json:"errors_24h"`
			Warnings int64          `json:"warnings_24h"`
		} `json:"health"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	for name, component := range map[string]map[string]any{
		"postgres": body.Health.Postgres,
		"redis":    body.Health.Redis,
	} {
		if component == nil {
			t.Fatalf("%s health missing from the response", name)
		}
		if configured, _ := component["configured"].(bool); configured {
			t.Fatalf("%s configured = true, want false", name)
		}
		if _, present := component["ok"]; present {
			t.Fatalf("%s reports ok while unconfigured; absent and broken must differ", name)
		}
		if _, present := component["latency_ms"]; present {
			t.Fatalf("%s reports a latency it never measured", name)
		}
	}
	if body.Health.Errors != 0 || body.Health.Warnings != 0 {
		t.Fatalf("log counts = %d/%d, want 0/0", body.Health.Errors, body.Health.Warnings)
	}
}

// An unreachable Postgres is reported as ok:false rather than failing the
// route, and the log tallies degrade to zero instead of 500ing.
func TestAdminServerStatusHealthWithUnreachablePostgres(t *testing.T) {
	t.Parallel()

	pool, err := pgxpool.New(context.Background(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)

	handler := &AdminHandler{pool: pool, RestartStatus: NewServerRestartStatusTracker()}
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/server/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Health adminServerHealth `json:"health"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Health.Postgres.Configured {
		t.Fatal("postgres configured = false, want true when a pool exists")
	}
	if body.Health.Postgres.OK == nil || *body.Health.Postgres.OK {
		t.Fatalf("postgres ok = %v, want false", body.Health.Postgres.OK)
	}
	if body.Health.Errors24h != 0 || body.Health.Warnings24h != 0 {
		t.Fatalf("log counts = %d/%d, want 0/0 when the query fails", body.Health.Errors24h, body.Health.Warnings24h)
	}
}

// failingSettingsStore models the settings table during a Postgres outage.
type failingSettingsStore struct {
	fakeServerSettingsStore
}

func (f *failingSettingsStore) GetAll(context.Context) (map[string]string, error) {
	return nil, fmt.Errorf("settings storage is down")
}

// A settings lookup that fails — Postgres being down — must not 500 the status
// endpoint: the health object in this response is where that outage is
// supposed to become visible.
func TestAdminServerStatusAnswersWhenSettingsStorageIsDown(t *testing.T) {
	t.Parallel()

	handler := &AdminHandler{
		RestartStatus: NewServerRestartStatusTracker(),
		SettingsRepo:  &failingSettingsStore{},
	}
	rec := httptest.NewRecorder()
	handler.HandleGetServerStatus(rec, httptest.NewRequest(http.MethodGet, "/admin/server/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Health adminServerHealth `json:"health"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Health.Postgres.Configured {
		t.Fatal("postgres configured = true, want false with no pool")
	}
}

func TestLogLevelCountsAreServedFromCache(t *testing.T) {
	t.Parallel()

	counts := cache.NewTTLCache[adminLogLevelCounts]()
	t.Cleanup(counts.Close)

	pool, err := pgxpool.New(context.Background(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(pool.Close)

	handler := &AdminHandler{pool: pool, logLevelCounts: counts}
	counts.Set(adminLogLevelCountsCacheKey, adminLogLevelCounts{4, 12}, time.Minute)

	// The pool cannot connect, so a cache miss would return zeros.
	if got := handler.logLevelCounts24h(context.Background()); got != (adminLogLevelCounts{4, 12}) {
		t.Fatalf("counts = %v, want [4 12] from cache", got)
	}
}
