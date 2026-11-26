package chapterthumbs

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestOriginalsCleanerExclusiveLeavesOneConnectionPoolAvailableDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Before the fix, Exclusive holds the pool's only connection for its lock
	// and the callback waits on that same connection until this deadline.
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	cleaner := &OriginalsCleaner{pool: pool}
	ran, err := cleaner.Exclusive(ctx, func() error {
		var one int
		return pool.QueryRow(ctx, "SELECT 1").Scan(&one)
	})
	if err != nil || !ran {
		t.Fatalf("Exclusive() = %v, %v; want callback run without error", ran, err)
	}
}
