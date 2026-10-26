package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/partman"
	"github.com/Silo-Server/silo-server/migrations"
)

// Migration 028 creates the first log partitions and partman extends them
// later in UTC. On a server whose TimeZone is not UTC the two must still agree:
// otherwise partman's next partition overlaps or leaves a gap beside one 028
// made, and writes fall into the default partition. Each zone gets its own
// database because 028 runs once; the test role needs CREATEDB.
func TestLogPartitionsMatchPartmanOnNonUTCServer(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	// Zones on both sides of UTC: a negative offset makes partman's partition
	// overlap 028's, a positive one can leave a gap before it.
	for _, zone := range []string{"America/New_York", "Asia/Tokyo"} {
		t.Run(zone, func(t *testing.T) {
			name := fmt.Sprintf("silo_log_partition_tz_%d", time.Now().UnixNano())
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
					t.Error(err)
				}
			}()
			cfg, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.Database = name
			cfg.ConnConfig.RuntimeParams["timezone"] = zone
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			provider, err := newMigrationProvider(pool, migrations.FS, "sql")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = provider.Close() }()
			if _, err := provider.UpTo(ctx, 28); err != nil {
				t.Fatal(err)
			}

			// Reach past the periods 028 created, so partman has to add
			// partitions next to them.
			for table, granularity := range map[string]partman.Granularity{
				"operational_logs": partman.Daily,
				"activity_log":     partman.Weekly,
			} {
				if err := partman.NewManager(pool, table, granularity, 8).EnsureFuturePartitions(ctx); err != nil {
					t.Errorf("%s: %v", table, err)
				}
			}

			if _, err := pool.Exec(ctx, `
INSERT INTO operational_logs ("timestamp", level, component, message)
SELECT ts, 'info', 'test', 'partition coverage'
FROM generate_series(now(), now() + interval '8 days', interval '1 hour') ts;
INSERT INTO activity_log ("timestamp", client_ip, method, path)
SELECT ts, '127.0.0.1', 'GET', '/'
FROM generate_series(now(), now() + interval '8 weeks', interval '1 hour') ts;`); err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"operational_logs_default", "activity_log_default"} {
				var n int
				if err := pool.QueryRow(ctx, "SELECT count(*) FROM public."+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("%s holds %d rows inside the partitioned window", table, n)
				}
			}
		})
	}
}
