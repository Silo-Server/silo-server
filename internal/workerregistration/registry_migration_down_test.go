package workerregistration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// The fence is database-wide, including across schemas. Rollback lock-order
// controls need their own database so unrelated package fixtures cannot consume
// the bounded retry budget or appear to retain the rollback connection's lock.
func isolateRegistryRollbackDatabase(t *testing.T) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "registry_down_" + hex.EncodeToString(nonce[:])
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err = admin.Exec(t.Context(), "CREATE DATABASE "+quoted); err != nil {
		admin.Close()
		t.Fatal("rollback contracts require a disposable PostgreSQL role with CREATEDB:", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	privateDSN := dsn + " dbname=" + name
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		parsed.Path = "/" + name
		parsed.RawPath = ""
		query := parsed.Query()
		query.Del("dbname")
		query.Del("database")
		parsed.RawQuery = query.Encode()
		privateDSN = parsed.String()
	}
	t.Setenv("SILO_TEST_DATABASE_URL", privateDSN)
}

func registryRollbackFixture(t *testing.T) (*Repository, *goose.Provider, string) {
	t.Helper()
	isolateRegistryRollbackDatabase(t)
	r := registrationDBWithRegistryFence(t, false)
	cfg := r.pool.Config().ConnConfig.Copy()
	application := "registry-down-" + cfg.RuntimeParams["search_path"]
	cfg.RuntimeParams["application_name"] = application
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	const filename = "20261014164751_worker_registry_classification_fence.sql"
	data, err := os.ReadFile("../../migrations/sql/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fstest.MapFS{filename: &fstest.MapFile{Data: data}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r, provider, application
}

func waitRegistryRollbackState(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		var found bool
		if err := pool.QueryRow(ctx, query, args...).Scan(&found); err != nil {
			t.Fatal("rollback barrier observation failed", err)
		}
		if found {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("rollback barrier was not reached")
		case <-time.After(time.Millisecond):
		}
	}
}

func waitRegistryRollbackStarted(t *testing.T, pool *pgxpool.Pool, application string) {
	t.Helper()
	waitRegistryRollbackState(t, pool, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND state='active' AND query LIKE '%1397312591%' AND wait_event IS NOT NULL)`, application)
}

func TestRegistryMigrationDownAvoidsEnrollmentTableLockDeadlock(t *testing.T) {
	r, provider, application := registryRollbackFixture(t)
	holder, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err = holder.Exec(t.Context(), "SELECT pg_advisory_xact_lock(1397312591,1464816203)"); err != nil {
		t.Fatal(err)
	}
	rollbackDone := make(chan error, 1)
	go func() { _, err := provider.Down(t.Context()); rollbackDone <- err }()
	// The original DOWN queues before enrollment. Once the holder releases,
	// it gets the fence while enrollment already owns its table lock.
	waitRegistryRollbackStarted(t, r.pool, application)
	enrollment, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = enrollment.Rollback(context.Background()) }()
	var pid uint32
	if err = enrollment.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		t.Fatal(err)
	}
	publicKey := registryTestPublicKey(t)
	enrollmentDone := make(chan error, 1)
	go func() {
		_, err := enrollment.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", publicKey)
		if err == nil {
			err = enrollment.Commit(t.Context())
		} else {
			_ = enrollment.Rollback(context.Background())
		}
		enrollmentDone <- err
	}()
	waitRegistryRollbackState(t, r.pool, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='relation' AND relation='worker_http_nodes'::regclass AND mode='RowExclusiveLock' AND granted) AND EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid)
	if err = holder.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	rollbackErr, enrollmentErr := <-rollbackDone, <-enrollmentDone
	if rollbackErr != nil || enrollmentErr != nil {
		t.Fatalf("concurrent rollback/enrollment failed: rollback=%v enrollment=%v", rollbackErr, enrollmentErr)
	}
	if version, err := provider.GetDBVersion(t.Context()); err != nil || version != 0 {
		t.Fatal("successful Goose rollback retained its migration version", version, err)
	}
	var historyRemoved bool
	if err = r.pool.QueryRow(t.Context(), "SELECT to_regclass('worker_http_origin_history') IS NULL").Scan(&historyRemoved); err != nil || !historyRemoved {
		t.Fatal("successful rollback did not remove history", err)
	}
}

func TestRegistryMigrationDownDoesNotHoldTablesWhileAwaitingSnapshotFence(t *testing.T) {
	r, provider, application := registryRollbackFixture(t)
	reader, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Rollback(context.Background()) }()
	if _, err = reader.Exec(t.Context(), "SELECT pg_advisory_xact_lock_shared(1397312591,1464816203)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := provider.Down(t.Context()); done <- err }()
	waitRegistryRollbackStarted(t, r.pool, application)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	var count int
	err = reader.QueryRow(ctx, "SELECT count(*) FROM stream_nodes n LEFT JOIN worker_http_nodes w ON w.node_id=n.id LEFT JOIN worker_http_origin_history h ON h.raw_url=n.url").Scan(&count)
	cancel()
	if err != nil {
		t.Fatal("rollback held a table lock while waiting for a snapshot's shared fence", err)
	}
	if err = reader.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRegistryMigrationDownContentionCancellationReleasesAllLocks(t *testing.T) {
	r, provider, application := registryRollbackFixture(t)
	tableHolder, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tableHolder.Rollback(context.Background()) }()
	if _, err = tableHolder.Exec(t.Context(), "LOCK TABLE worker_http_nodes IN ROW EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := provider.Down(ctx); done <- err }()
	waitRegistryRollbackStarted(t, r.pool, application)
	// A failed NOWAIT table attempt must roll its advisory lock back before
	// sleeping. Observe a shared snapshot admission while DOWN is still active.
	waitRegistryRollbackState(t, r.pool, "SELECT pg_try_advisory_xact_lock_shared(1397312591,1464816203)")
	cancel()
	if err = <-done; err == nil {
		t.Fatal("canceled rollback succeeded")
	}
	if version, err := provider.GetDBVersion(t.Context()); err != nil || version != 20261014164751 {
		t.Fatal("canceled Goose rollback changed its migration version", version, err)
	}
	var intact bool
	if err = r.pool.QueryRow(t.Context(), `SELECT to_regclass('worker_http_origin_history') IS NOT NULL AND EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='worker_http_nodes'::regclass AND tgname='worker_http_node_classification_fence')`).Scan(&intact); err != nil || !intact {
		t.Fatal("canceled Goose rollback partially removed the security fence", err)
	}
	check, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = check.Rollback(context.Background()) }()
	if _, err = check.Exec(t.Context(), "LOCK TABLE stream_nodes,worker_http_origin_history IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
		t.Fatal("canceled rollback retained table locks", err)
	}
	var released bool
	if err = check.QueryRow(t.Context(), `SELECT NOT EXISTS(SELECT 1 FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.application_name=$1 AND l.locktype='advisory')`, application).Scan(&released); err != nil || !released {
		t.Fatal("canceled rollback retained the advisory fence", err)
	}
	if _, err = check.Exec(t.Context(), "DELETE FROM worker_http_origin_history"); err == nil {
		t.Fatal("canceled rollback removed append-only protection")
	} else if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
		t.Fatal("unexpected history security result", err)
	}
}
