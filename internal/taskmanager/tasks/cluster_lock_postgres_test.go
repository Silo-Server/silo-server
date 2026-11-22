package tasks

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A task holding its cluster lock must still reach the pool it was given:
// database.max_connections accepts 1, and the lock pinning that one
// connection would stall the task and every other query on the node.
func TestAdvisoryClusterLockLeavesASingleConnectionPoolFreePostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	lock := advisoryClusterLock{pool: pool, key: 0x53494C4F54535431, name: "test"}
	release, acquired, err := lock.TryAcquire(t.Context())
	if err != nil || !acquired {
		t.Fatalf("TryAcquire = %v, %v", acquired, err)
	}
	// Release before the pool closes, so a failure below ends the test
	// instead of leaving pool.Close waiting on a pinned connection.
	release = sync.OnceFunc(release)
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("pool query while the lock is held: %v", err)
	}
	if _, acquired, err := lock.TryAcquire(t.Context()); err != nil || acquired {
		t.Fatalf("second TryAcquire while held = %v, %v; want skipped", acquired, err)
	}
	release()
	again, acquired, err := lock.TryAcquire(t.Context())
	if err != nil || !acquired {
		t.Fatalf("TryAcquire after release = %v, %v", acquired, err)
	}
	again()
}
