package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
)

// failingSettingsStore models the settings table during a Postgres outage.
type failingSettingsStore struct {
	fakeServerSettingsStore
}

func (f *failingSettingsStore) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("settings storage is down")
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

// Live Jellyfin identity settings take effect without a restart, so a stored
// value that differs from the running config does not ask for one.
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
	if handler.ReadAdminServerStatus(t.Context()).RestartRequired {
		t.Fatal("RestartRequired = true, want false for live Jellyfin identity settings")
	}
}

// The status read answers whatever its dependencies do: the health object is
// where a missing or broken one becomes visible.
func TestAdminServerStatusHealthDegradesWithoutFailing(t *testing.T) {
	t.Parallel()
	unreachable, err := pgxpool.New(context.Background(), "postgres://silo:silo@127.0.0.1:1/silo?connect_timeout=1")
	if err != nil {
		t.Fatalf("create unreachable pool: %v", err)
	}
	t.Cleanup(unreachable.Close)

	t.Run("no dependencies", func(t *testing.T) {
		health := (&AdminHandler{RestartStatus: NewServerRestartStatusTracker()}).ReadAdminServerStatus(t.Context()).Health
		for name, component := range map[string]adminHealthComponent{"postgres": health.Postgres, "redis": health.Redis} {
			// Absent and broken must differ: an unconfigured component
			// reports neither ok nor a latency it never measured.
			if component.Configured || component.OK != nil || component.LatencyMS != nil {
				t.Fatalf("%s = %+v, want unconfigured", name, component)
			}
		}
		if health.Errors24h != 0 || health.Warnings24h != 0 {
			t.Fatalf("log counts = %d/%d, want 0/0", health.Errors24h, health.Warnings24h)
		}
	})

	t.Run("unreachable Postgres", func(t *testing.T) {
		health := (&AdminHandler{pool: unreachable, RestartStatus: NewServerRestartStatusTracker()}).ReadAdminServerStatus(t.Context()).Health
		if !health.Postgres.Configured || health.Postgres.OK == nil || *health.Postgres.OK {
			t.Fatalf("postgres = %+v, want configured and not ok", health.Postgres)
		}
		if health.Errors24h != 0 || health.Warnings24h != 0 {
			t.Fatalf("log counts = %d/%d, want 0/0 when the query fails", health.Errors24h, health.Warnings24h)
		}
	})

	t.Run("settings storage down", func(t *testing.T) {
		status := (&AdminHandler{RestartStatus: NewServerRestartStatusTracker(), SettingsRepo: &failingSettingsStore{}}).ReadAdminServerStatus(t.Context())
		if status.Health.Postgres.Configured {
			t.Fatal("postgres configured = true, want false with no pool")
		}
		// A false lock flag is not a confirmed editable location.
		if status.ArtworkStorage.StatusKnown {
			t.Fatal("storage lock status known despite failed settings read")
		}
	})
}

func TestAdminServerStatusReportsArtworkStorageLock(t *testing.T) {
	t.Parallel()
	read := func(backend string, stored map[string]string) adminArtworkStorageStatus {
		h := &AdminHandler{RestartStatus: NewServerRestartStatusTracker(), ArtworkBackend: backend, SettingsRepo: &fakeServerSettingsStore{values: stored}}
		return h.ReadAdminServerStatus(t.Context()).ArtworkStorage
	}
	if got := read("local", map[string]string{}); got.Locked || got.PrivateLocked || !got.StatusKnown || got.Backend != "local" {
		t.Fatalf("fresh install: %+v", got)
	}
	if got := read("s3", map[string]string{blobstore.IdentitySettingKey: "s3|https://s3.example|artwork|"}); !got.Locked || !got.PrivateLocked || !got.StatusKnown || got.Backend != "s3" {
		t.Fatalf("recorded artwork did not lock both locations: %+v", got)
	}
	// A private bucket that holds data locks only the private location; the
	// artwork location is still free until the first artwork write.
	if got := read("local", map[string]string{blobstore.OperationalIdentitySettingKey: "s3|https://private.example|private|"}); got.Locked || !got.PrivateLocked {
		t.Fatalf("private-only recording: %+v", got)
	}
}
