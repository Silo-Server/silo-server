package markers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProviderConfigEnabledForFetchSortsAndFilters(t *testing.T) {
	s := &ProviderConfigStore{cache: map[string]ProviderConfig{
		"alpha":   {Provider: "alpha", FetchEnabled: true, FetchPriority: 200},
		"bravo":   {Provider: "bravo", FetchEnabled: true, FetchPriority: 100},
		"charlie": {Provider: "charlie", FetchEnabled: false, FetchPriority: 50},
	}}

	got := s.EnabledForFetch()
	if len(got) != 2 {
		t.Fatalf("EnabledForFetch returned %d providers, want 2 (charlie disabled)", len(got))
	}
	if got[0].Provider != "bravo" || got[1].Provider != "alpha" {
		t.Errorf("order = [%s %s], want [bravo alpha] by fetch_priority", got[0].Provider, got[1].Provider)
	}
}

// Turning markers.share_by_default off stops sharing on the providers the
// server names and keeps their other settings. A provider seeded while the
// opt-out is being saved starts with sharing off.
func TestProviderConfigDefaultSharingDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	installation := int(time.Now().UnixNano() % 1_000_000_000)
	sharing, other, seeded := PluginProviderID(installation, "markers"), PluginProviderID(installation+1, "markers"), PluginProviderID(installation+2, "markers")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marker_provider_config WHERE provider = ANY($1)`, []string{sharing, other, seeded})
		_, _ = pool.Exec(context.Background(), `DELETE FROM server_settings WHERE key = $1`, SettingShareByDefault)
	})

	store := NewProviderConfigStore(pool)
	on := func(id string) ProviderConfig {
		return ProviderConfig{
			Provider: id, FetchEnabled: true, FetchPriority: 5,
			ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9,
		}
	}
	for _, id := range []string{sharing, other} {
		if err := store.Update(ctx, on(id)); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	// The settings save stops sharing and stores the opt-out in one
	// transaction. A reload seeding a sharing row meanwhile waits for it.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := StopDefaultSharingInTx(ctx, tx, []string{sharing}); err != nil {
		t.Fatalf("StopDefaultSharingInTx: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO server_settings (key, value) VALUES ($1, 'false')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, SettingShareByDefault); err != nil {
		t.Fatalf("store setting: %v", err)
	}
	seedDone := make(chan error, 1)
	go func() { seedDone <- store.EnsureDefaultSharing(ctx, on(seeded)) }()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_locks
			WHERE NOT granted AND relation = 'marker_provider_config'::regclass
			AND database = (SELECT oid FROM pg_database WHERE datname = current_database()))`).Scan(&waiting); err != nil {
			t.Fatalf("read locks: %v", err)
		}
		if waiting {
			break
		}
		select {
		case err := <-seedDone:
			t.Fatalf("seed finished before the opt-out committed: %v", err)
		case <-ctx.Done():
			t.Fatal("seed never waited for the opt-out")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := <-seedDone; err != nil {
		t.Fatalf("EnsureDefaultSharing: %v", err)
	}

	if err := store.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, _ := store.Get(sharing); got.ContributeEnabled || got.ContributeAutoLocal || !got.FetchEnabled || got.ContributeMinConfidence != 0.9 {
		t.Fatalf("stopped provider = %+v, want only contribution off", got)
	}
	if got, _ := store.Get(other); !got.ContributeEnabled || !got.ContributeAutoLocal {
		t.Fatalf("other provider = %+v, want contribution left on", got)
	}
	if got, ok := store.Get(seeded); !ok || got.ContributeEnabled || got.ContributeAutoLocal || got.ContributeMinConfidence != 0.9 {
		t.Fatalf("seeded provider = %+v (exists %v), want a row with sharing off", got, ok)
	}
}
