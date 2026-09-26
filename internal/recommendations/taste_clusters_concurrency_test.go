package recommendations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pause the first replacement after its INSERT but before commit. Without
// serialisation the second DELETE misses those uncommitted rows, and its
// INSERT fails with 23505 once the first replacement commits.
func TestUpsertTasteClustersConcurrentPostgres(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			f := newTasteClusterFixture(t)
			if existing {
				f.replace(t, f.observer, 1, "owner", tasteClusterSet("old", 3))
			}
			release, firstDone := f.pauseFirst(t)
			secondDone := make(chan error, 1)
			go func() {
				secondDone <- NewRepo(f.second).UpsertTasteClusters(f.ctx, 1, "owner", tasteClusterSet("second", 1))
			}()
			f.waitBlocked(t, f.secondPID, secondDone)
			release()
			f.await(t, firstDone)
			f.await(t, secondDone)
			f.assertClusters(t, 1, "owner", "second", 1)
		})
	}
}

func TestUpsertTasteClustersProfileIsolationAndCancellationPostgres(t *testing.T) {
	f := newTasteClusterFixture(t)
	release, firstDone := f.pauseFirst(t)

	// A different profile or account must remain writable while this profile
	// is blocked. These calls complete before the gate is released.
	f.replace(t, f.second, 1, "other", tasteClusterSet("other-profile", 1))
	f.replace(t, f.second, 2, "owner", tasteClusterSet("other-account", 1))

	waitCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- NewRepo(f.second).UpsertTasteClusters(waitCtx, 1, "owner", tasteClusterSet("cancelled", 1))
	}()
	f.waitBlocked(t, f.secondPID, waitDone)
	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled replacement = %v, want context.Canceled", err)
		}
	case <-f.ctx.Done():
		t.Fatal("cancelled replacement did not return")
	}
	release()
	f.await(t, firstDone)
	f.assertClusters(t, 1, "owner", "first", 2)
	f.assertClusters(t, 1, "other", "other-profile", 1)
	f.assertClusters(t, 2, "owner", "other-account", 1)
	// A cancelled waiter must not retain the profile's lock.
	f.replace(t, f.second, 1, "owner", tasteClusterSet("after-cancel", 1))
	f.assertClusters(t, 1, "owner", "after-cancel", 1)
}

func TestUpsertTasteClustersRollbackAndClearPostgres(t *testing.T) {
	f := newTasteClusterFixture(t)
	f.replace(t, f.observer, 1, "owner", tasteClusterSet("original", 2))
	invalid := tasteClusterSet("invalid", 2)
	invalid[1].Embedding = []float32{1, 0} // Fail after DELETE and the first INSERT.
	if err := NewRepo(f.first).UpsertTasteClusters(f.ctx, 1, "owner", invalid); err == nil {
		t.Fatal("replacement with invalid vector dimensions succeeded")
	}
	f.assertClusters(t, 1, "owner", "original", 2)
	// Rollback must release the lock for another database connection, and an
	// empty replacement must remove all clusters without inserting a sentinel.
	f.replace(t, f.second, 1, "owner", nil)
	f.assertClusters(t, 1, "owner", "", 0)
	f.replace(t, f.first, 1, "owner", tasteClusterSet("after-clear", 1))
	f.assertClusters(t, 1, "owner", "after-clear", 1)
}

type tasteClusterFixture struct {
	ctx                 context.Context
	observer            *pgxpool.Pool
	first, second       *pgxpool.Pool
	firstPID, secondPID uint32
	schema              string
}

func newTasteClusterFixture(t *testing.T) *tasteClusterFixture {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS vector`); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_taste_clusters_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop taste cluster fixture: %v", err)
		}
	})
	newPool := func(name string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.MaxConns = 2
		cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
		cfg.ConnConfig.RuntimeParams["application_name"] = name
		if name != "observer" {
			cfg.MaxConns = 1
		}
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	f := &tasteClusterFixture{
		ctx: ctx, schema: schema,
		observer: newPool("observer"), first: newPool("first"), second: newPool("second"),
	}
	if _, err := f.observer.Exec(ctx, `CREATE TABLE user_taste_clusters (
		user_id integer NOT NULL, profile_id text NOT NULL, cluster_idx integer NOT NULL,
		embedding vector(3) NOT NULL, dominant_genres jsonb, label text,
		member_count integer, total_weight double precision, updated_at timestamptz NOT NULL,
		PRIMARY KEY (user_id, profile_id, cluster_idx)
	)`); err != nil {
		t.Fatal(err)
	}
	if err := f.first.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&f.firstPID); err != nil {
		t.Fatal(err)
	}
	if err := f.second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&f.secondPID); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *tasteClusterFixture) pauseFirst(t *testing.T) (func(), <-chan error) {
	t.Helper()
	gate, err := f.observer.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gate.Rollback(context.Background()) })
	gateKey := "test:taste-cluster-gate:" + f.schema
	if _, err := gate.Exec(f.ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, gateKey); err != nil {
		t.Fatal(err)
	}
	// This trigger is confined to the disposable schema. It gates an actual
	// INSERT, independently of the production locking implementation.
	trigger := fmt.Sprintf(`CREATE FUNCTION pause_first() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
		IF current_setting('application_name') = 'first' THEN
			PERFORM pg_advisory_xact_lock(hashtextextended('%s', 0));
		END IF;
		RETURN NEW;
	END $$;
	CREATE TRIGGER pause_first AFTER INSERT ON user_taste_clusters
	FOR EACH ROW EXECUTE FUNCTION pause_first();`, gateKey)
	if _, err := f.observer.Exec(f.ctx, trigger); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- NewRepo(f.first).UpsertTasteClusters(f.ctx, 1, "owner", tasteClusterSet("first", 2))
	}()
	f.waitBlocked(t, f.firstPID, done)
	return func() {
		if err := gate.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
	}, done
}

func (f *tasteClusterFixture) waitBlocked(t *testing.T, pid uint32, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := f.observer.QueryRow(f.ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE pid = $1 AND wait_event_type = 'Lock'
		)`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("replacement returned before blocking: %v", err)
		case <-f.ctx.Done():
			t.Fatal("replacement did not reach the database lock")
		case <-ticker.C:
		}
	}
}

func (f *tasteClusterFixture) await(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("replacement failed: %v", err)
		}
	case <-f.ctx.Done():
		t.Fatal("replacement did not finish")
	}
}

func (f *tasteClusterFixture) replace(t *testing.T, pool *pgxpool.Pool, userID int, profileID string, clusters []TasteCluster) {
	t.Helper()
	if err := NewRepo(pool).UpsertTasteClusters(f.ctx, userID, profileID, clusters); err != nil {
		t.Fatal(err)
	}
}

func (f *tasteClusterFixture) assertClusters(t *testing.T, userID int, profileID, label string, count int) {
	t.Helper()
	clusters, err := NewRepo(f.observer).GetTasteClusters(f.ctx, userID, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != count {
		t.Fatalf("cluster count = %d, want %d", len(clusters), count)
	}
	for i, c := range clusters {
		if c.ClusterIdx != i || c.Label != label {
			t.Fatalf("cluster %d = (index %d, label %q), want label %q", i, c.ClusterIdx, c.Label, label)
		}
	}
}

func tasteClusterSet(label string, count int) []TasteCluster {
	clusters := make([]TasteCluster, count)
	for i := range clusters {
		clusters[i] = TasteCluster{
			ClusterIdx: i, Embedding: []float32{1, 0, 0}, DominantGenres: []string{"test"},
			Label: label, MemberCount: 1, TotalWeight: 1,
		}
	}
	return clusters
}
