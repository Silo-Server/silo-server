package nodepool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func configurationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_NODE_CONFIGURATION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_NODE_CONFIGURATION_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConnConfig.Database != "silo_operations_node_configuration_test" {
		t.Fatal("refusing non-owned node configuration database")
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("node_configuration_%d", time.Now().UnixNano())
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanup, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, err = pool.Exec(t.Context(), `CREATE TABLE stream_nodes (
 id serial PRIMARY KEY, name text NOT NULL, type text NOT NULL, url text NOT NULL CONSTRAINT stream_nodes_url_key UNIQUE,
 public_url text, enabled boolean NOT NULL DEFAULT true, healthy boolean NOT NULL DEFAULT false,
 active_jobs integer NOT NULL DEFAULT 0, node_group text, max_jobs integer, max_bandwidth_kbps integer,
 egress_kbps integer NOT NULL DEFAULT 0, last_health_check timestamptz, created_at timestamptz NOT NULL DEFAULT now(),
 capabilities jsonb, capabilities_hash text, capabilities_refreshed_at timestamptz, last_stats jsonb,
 hw_accel_override text, hw_device_override text, capability_drift text, capability_drift_baseline jsonb)`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906205459_node_admin_configuration_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestAdminNodeConfigurationTransactions(t *testing.T) {
	pool := configurationTestPool(t)
	store := NewAdminConfigurationStore(pool)
	bridge := NewRepository(pool)
	ctx := t.Context()
	input := CreateNodeInput{Name: "Synthetic", Type: NodeTypeTranscode, URL: "http://node.invalid", Group: "  group  ", MaxJobs: new(0)}
	node, err := store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	rows, generation, err := store.Snapshot(ctx)
	if err != nil || len(rows) != 1 || rows[0].AdminRevision != node.AdminRevision {
		t.Fatalf("consistent creation: %v %v", rows, err)
	}
	t.Run("natural retry and conflicting configuration", func(t *testing.T) {
		repeated, err := store.Create(ctx, input)
		if err != nil || repeated.ID != node.ID || repeated.AdminRevision != node.AdminRevision {
			t.Fatalf("retry: %v %v", repeated, err)
		}
		other := input
		other.Name = "Competing"
		if _, err = store.Create(ctx, other); !errors.Is(err, ErrNodeConfigurationConflict) {
			t.Fatalf("conflict: %v", err)
		}
		_, g, err := store.Snapshot(ctx)
		if err != nil || g != generation {
			t.Fatalf("retry changed generation: %d %v", g, err)
		}
	})
	t.Run("health sample preserves configuration validator", func(t *testing.T) {
		if err := bridge.UpdateHealth(ctx, node.ID, node.URL, true, 3, 40, nil, nil); err != nil {
			t.Fatal(err)
		}
		rows, g, err := store.Snapshot(ctx)
		if err != nil || rows[0].AdminRevision != node.AdminRevision || !rows[0].Healthy || g != generation {
			t.Fatalf("health changed configuration: %v %d %v", rows, g, err)
		}
	})
	refused := errors.New("guard refused")
	guard := func(want int64) func(int64) error {
		return func(got int64) error {
			if got != want {
				return refused
			}
			return nil
		}
	}
	t.Run("guard failure has no update or delete effects", func(t *testing.T) {
		deny := func(int64) error { return refused }
		if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Name: new("Denied")}, deny); !errors.Is(err, refused) {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, node.ID, deny); !errors.Is(err, refused) {
			t.Fatal(err)
		}
		rows, g, err := store.Snapshot(ctx)
		if err != nil || rows[0].Name != input.Name || g != generation {
			t.Fatalf("refused effects: %v %d %v", rows, g, err)
		}
	})
	t.Run("update returns the locked pre-image", func(t *testing.T) {
		rows, _, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		current := rows[0]
		updated, previous, err := store.Update(ctx, node.ID, UpdateNodeInput{Name: new("Pre-image check")}, guard(current.AdminRevision))
		if err != nil {
			t.Fatal(err)
		}
		if previous.Name != current.Name || previous.AdminRevision != current.AdminRevision || updated.Name != "Pre-image check" || updated.AdminRevision == previous.AdminRevision {
			t.Fatalf("pre-image %+v updated %+v", previous, updated)
		}
		if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Name: new(input.Name)}, guard(updated.AdminRevision)); err != nil {
			t.Fatal(err)
		}
		rows, _, err = store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		node.AdminRevision = rows[0].AdminRevision
	})
	t.Run("concurrent original-version writers have one winner", func(t *testing.T) {
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, name := range []string{"Writer A", "Writer B"} {
			wg.Go(func() {
				<-start
				_, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Name: new(name)}, guard(node.AdminRevision))
				results <- err
			})
		}
		close(start)
		wg.Wait()
		close(results)
		winners, stale := 0, 0
		for err := range results {
			if err == nil {
				winners++
			} else if errors.Is(err, refused) {
				stale++
			} else {
				t.Fatal(err)
			}
		}
		if winners != 1 || stale != 1 {
			t.Fatalf("winners=%d stale=%d", winners, stale)
		}
	})
	t.Run("bridge ABA invalidates old original version", func(t *testing.T) {
		if _, err := bridge.Update(ctx, node.ID, UpdateNodeInput{Name: new(input.Name)}); err != nil {
			t.Fatal(err)
		}
		rows, _, err := store.Snapshot(ctx)
		if err != nil || rows[0].Name != input.Name || rows[0].AdminRevision == node.AdminRevision {
			t.Fatalf("ABA: %v %v", rows, err)
		}
		if _, _, err := store.Update(ctx, node.ID, UpdateNodeInput{Enabled: new(false)}, guard(node.AdminRevision)); !errors.Is(err, refused) {
			t.Fatalf("ABA accepted: %v", err)
		}
	})
	t.Run("failed durable invalidation rolls back deletion", func(t *testing.T) {
		rows, g, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, fmt.Sprintf("ALTER TABLE stream_node_pool_generation ADD CONSTRAINT injected_failure CHECK (generation <= %d)", g))
		if err != nil {
			t.Fatal(err)
		}
		err = store.Delete(ctx, node.ID, guard(rows[0].AdminRevision))
		if err == nil {
			t.Fatal("expected invalidation failure")
		}
		if _, err = bridge.GetByID(ctx, node.ID); err != nil {
			t.Fatalf("deletion escaped rollback: %v", err)
		}
		if _, err = pool.Exec(ctx, "ALTER TABLE stream_node_pool_generation DROP CONSTRAINT injected_failure"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("delete generation survives new store and repeated missing delete", func(t *testing.T) {
		rows, g, err := store.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = store.Delete(ctx, node.ID, guard(rows[0].AdminRevision)); err != nil {
			t.Fatal(err)
		}
		restarted := NewAdminConfigurationStore(pool)
		rows, after, err := restarted.Snapshot(ctx)
		if err != nil || len(rows) != 0 || after <= g {
			t.Fatalf("durable delete: %v %d %v", rows, after, err)
		}
		called := false
		if err = restarted.Delete(ctx, node.ID, func(int64) error { called = true; return nil }); !errors.Is(err, ErrNodeNotFound) || called {
			t.Fatalf("missing receipt: %v guard=%v", err, called)
		}
	})
}
