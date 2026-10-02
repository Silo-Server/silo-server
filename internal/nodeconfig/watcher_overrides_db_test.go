package nodeconfig

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// newOverrideTestPool follows the repository-wide convention for tests that
// need a real database: skip unless one is configured, and skip again if it
// predates the migration under test rather than failing on a missing column.
func newOverrideTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	var columns int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'stream_nodes' AND column_name = 'hw_accel_override'`).Scan(&columns); err != nil || columns < 1 {
		t.Skip("test database has not applied the stream_nodes override migration")
	}
	return pool
}

func insertOverrideNode(t *testing.T, pool *pgxpool.Pool, url string, accel *string) int {
	t.Helper()
	return insertOverrideNodeNamed(t, pool, fmt.Sprintf("override-%d", time.Now().UnixNano()), url, accel)
}

// insertOverrideNodeNamed is insertOverrideNode with an explicit registered
// name, for the name-fallback tests below where the name (not the
// auto-generated one) is the thing under test.
func insertOverrideNodeNamed(t *testing.T, pool *pgxpool.Pool, name, url string, accel *string) int {
	t.Helper()
	ctx := context.Background()
	var id int
	if err := pool.QueryRow(ctx,
		`INSERT INTO stream_nodes (name, type, url, hw_accel_override)
		 VALUES ($1, 'transcode', $2, $3) RETURNING id`,
		name, url, accel).Scan(&id); err != nil {
		t.Fatalf("insert node %q: %v", url, err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM stream_nodes WHERE id = $1`, id); err != nil {
			t.Errorf("cleanup node %d: %v", id, err)
		}
	})
	return id
}

// stream_nodes.url is unique on the exact string, so a node registered twice —
// once with a trailing slash — is two legal rows that the lookup's rtrim
// tolerance collapses into one key. The winner has to be the same on every
// reload: without an explicit order the seq scan returns whichever row it
// reaches first, and the 30-second health sweep rewriting those rows would
// silently flip the node between two acceleration policies mid-deployment.
func TestQueryNodeHWOverridesPicksTheSameRowAcrossReloads(t *testing.T) {
	pool := newOverrideTestPool(t)
	ctx := context.Background()
	base := fmt.Sprintf("http://dup-node-%d:8082", time.Now().UnixNano())

	pinned := "none"
	firstID := insertOverrideNode(t, pool, base, &pinned)
	insertOverrideNode(t, pool, base+"/", nil)

	w := &Watcher{pool: pool}
	assertPinned := func(stage string) {
		t.Helper()
		overrides, found, err := w.queryNodeHWOverrides(ctx, base, "")
		if err != nil {
			t.Fatalf("%s: lookup: %v", stage, err)
		}
		if !found {
			t.Fatalf("%s: node row not found", stage)
		}
		if overrides.HWAccel == nil || *overrides.HWAccel != pinned {
			t.Fatalf("%s: hw_accel override = %v, want the lowest-id row's %q", stage, overrides.HWAccel, pinned)
		}
	}

	assertPinned("initial load")

	// What the health sweep does every 30 seconds. It moves the tuple, and with
	// it the physical order an unordered scan would have followed.
	if _, err := pool.Exec(ctx,
		`UPDATE stream_nodes SET healthy = NOT healthy, last_health_check = NOW() WHERE id = $1`, firstID); err != nil {
		t.Fatalf("health sweep update: %v", err)
	}

	assertPinned("after a health sweep rewrote the row")
}

// A split-horizon node — public url registered, internal NODE_URL on the box
// — has no url match at all, so the lookup falls back to the registered name.
func TestQueryNodeHWOverridesFallsBackToUniqueName(t *testing.T) {
	pool := newOverrideTestPool(t)
	ctx := context.Background()
	name := fmt.Sprintf("silo-name-%d", time.Now().UnixNano())
	registeredURL := fmt.Sprintf("https://public-%d.example.com", time.Now().UnixNano())
	pinned := "vaapi"
	insertOverrideNodeNamed(t, pool, name, registeredURL, &pinned)

	w := &Watcher{pool: pool}
	overrides, found, err := w.queryNodeHWOverrides(ctx, "http://10.0.4.7:8082", name)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !found {
		t.Fatal("name-matched row not found")
	}
	if overrides.HWAccel == nil || *overrides.HWAccel != pinned {
		t.Fatalf("hw_accel override = %v, want the name-matched row's %q", overrides.HWAccel, pinned)
	}
}

// Two rows sharing the fallback name identify nothing: names carry no unique
// constraint, so an ambiguous match must not silently adopt either row's
// overrides.
func TestQueryNodeHWOverridesAmbiguousNameIsNotFound(t *testing.T) {
	pool := newOverrideTestPool(t)
	ctx := context.Background()
	name := fmt.Sprintf("silo-name-%d", time.Now().UnixNano())
	pinned := "none"
	insertOverrideNodeNamed(t, pool, name, fmt.Sprintf("https://a-%d.example.com", time.Now().UnixNano()), &pinned)
	insertOverrideNodeNamed(t, pool, name, fmt.Sprintf("https://b-%d.example.com", time.Now().UnixNano()), nil)

	w := &Watcher{pool: pool}
	overrides, found, err := w.queryNodeHWOverrides(ctx, "http://10.0.4.7:8082", name)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if found {
		t.Fatalf("ambiguous name matched, overrides = %+v", overrides)
	}
}

// The url match wins even when the name also identifies a different row: url
// is the primary identity, and name is only consulted when the url misses.
func TestQueryNodeHWOverridesURLWinsOverNameMatch(t *testing.T) {
	pool := newOverrideTestPool(t)
	ctx := context.Background()
	url := fmt.Sprintf("http://url-match-%d:8082", time.Now().UnixNano())
	name := fmt.Sprintf("silo-name-%d", time.Now().UnixNano())
	byURL := "qsv"
	byName := "nvenc"
	insertOverrideNodeNamed(t, pool, fmt.Sprintf("other-%d", time.Now().UnixNano()), url, &byURL)
	insertOverrideNodeNamed(t, pool, name, fmt.Sprintf("https://different-%d.example.com", time.Now().UnixNano()), &byName)

	w := &Watcher{pool: pool}
	overrides, found, err := w.queryNodeHWOverrides(ctx, url, name)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !found {
		t.Fatal("url-matched row not found")
	}
	if overrides.HWAccel == nil || *overrides.HWAccel != byURL {
		t.Fatalf("hw_accel override = %v, want the url-matched %q, not the name-matched row", overrides.HWAccel, byURL)
	}
}
