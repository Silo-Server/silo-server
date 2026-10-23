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

// Turning markers.share_by_default off must stop sharing on the providers the
// server names, keep their other settings, and leave the rest alone.
func TestProviderConfigStopSharingDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	installation := int(time.Now().UnixNano() % 1_000_000_000)
	sharing, other := PluginProviderID(installation, "markers"), PluginProviderID(installation+1, "markers")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM marker_provider_config WHERE provider = ANY($1)`, []string{sharing, other})
	})

	store := NewProviderConfigStore(pool)
	for _, id := range []string{sharing, other} {
		if err := store.Update(ctx, ProviderConfig{
			Provider: id, FetchEnabled: true, FetchPriority: 5,
			ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	for _, wantChanged := range []bool{true, false} {
		changed, err := store.StopSharing(ctx, []string{sharing})
		if err != nil {
			t.Fatalf("StopSharing: %v", err)
		}
		if changed != wantChanged {
			t.Fatalf("changed = %v, want %v", changed, wantChanged)
		}
	}
	if got, _ := store.Get(sharing); got.ContributeEnabled || got.ContributeAutoLocal || !got.FetchEnabled || got.ContributeMinConfidence != 0.9 {
		t.Fatalf("stopped provider = %+v, want only contribution off", got)
	}
	if got, _ := store.Get(other); !got.ContributeEnabled || !got.ContributeAutoLocal {
		t.Fatalf("other provider = %+v, want contribution left on", got)
	}
}
