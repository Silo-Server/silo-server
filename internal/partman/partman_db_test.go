package partman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDropExpiredPartitionsGivesUpOnABusyParentDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	table := "qa_drop_wait_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	expired := table + "_p_20000101"
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE public.%s(timestamp timestamptz NOT NULL) PARTITION BY RANGE(timestamp);
 CREATE TABLE public.%s PARTITION OF public.%s FOR VALUES FROM ('2000-01-01 00:00:00+00') TO ('2000-01-02 00:00:00+00');
 CREATE TABLE public.%s PARTITION OF public.%s DEFAULT;`, quoteIdent(table), quoteIdent(expired), quoteIdent(table), quoteIdent(table+"_default"), quoteIdent(table)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), "DROP TABLE public."+quoteIdent(table)+" CASCADE")
	}()
	manager := NewManager(pool, table, Daily, 0)
	manager.dropWait = 100 * time.Millisecond
	cutoff := time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC)

	// A long reader on the parent, such as a slow report or a backup.
	reader, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := reader.Exec(ctx, "LOCK TABLE public."+quoteIdent(table)+" IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	// Without a lock timeout the drop waits for the reader until this deadline
	// cancels it, and every later query on the parent queues behind it.
	dropCtx, stopDrop := context.WithTimeout(ctx, 5*time.Second)
	dropped, err := manager.DropExpiredPartitions(dropCtx, cutoff)
	stopDrop()
	if !errors.Is(err, ErrPartitionBusy) {
		t.Fatalf("drop behind a reader: %v, want ErrPartitionBusy", err)
	}
	if len(dropped) != 0 {
		t.Fatalf("dropped %v behind a reader", dropped)
	}

	// The next run, after the reader finishes, drops the partition.
	if err := reader.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	dropped, err = manager.DropExpiredPartitions(ctx, cutoff)
	if err != nil || len(dropped) != 1 || dropped[0] != expired {
		t.Fatalf("retry dropped %v, %v; want [%s]", dropped, err, expired)
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+expired).Scan(&exists); err != nil || exists {
		t.Fatalf("expired partition still exists: %v, %v", exists, err)
	}
}
