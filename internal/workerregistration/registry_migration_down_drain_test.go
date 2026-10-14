package workerregistration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Observe the actual three-second drain, rather than a retry's short sleep.
// Both lock modes are accepted here so this regression also exercises the old
// SELECT-blocking rollback without changing the migration under test.
func waitRegistryRollbackDrain(t *testing.T, r *Repository, application string) {
	t.Helper()
	waitRegistryRollbackState(t, r.pool, `SELECT EXISTS(
        SELECT 1 FROM pg_stat_activity a
        WHERE a.application_name=$1 AND a.wait_event='PgSleep'
        AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.granted)
        AND (SELECT count(DISTINCT relation) FROM pg_locks l
            WHERE l.pid=a.pid AND l.granted
            AND l.mode IN ('ShareRowExclusiveLock','AccessExclusiveLock')
            AND l.relation IN ('stream_nodes'::regclass,'worker_http_nodes'::regclass,'worker_http_origin_history'::regclass))=3
    )`, application)
}

func TestRegistryMigrationDownAllowsAuthenticationDuringDrain(t *testing.T) {
	r, provider, application := registryRollbackFixture(t)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := provider.Down(ctx); done <- err }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err == nil {
			t.Error("rollback finished before the observed drain was canceled")
		}
	})
	waitRegistryRollbackDrain(t, r, application)
	// Worker snapshot refreshes have a 1.5-second request budget. Authentication
	// must remain available while the configuration fence drains older snapshots.
	authCtx, authCancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer authCancel()
	started := time.Now()
	node, found, err := r.ByID(authCtx, 17)
	if err != nil || !found || node.ID != 17 {
		t.Fatalf("worker authentication blocked during rollback drain after %s: found=%v err=%v", time.Since(started), found, err)
	}
	t.Logf("worker authentication completed during observed rollback drain in %s", time.Since(started))
	// Ensure the lookup completed during the drain, rather than after DDL.
	waitRegistryRollbackDrain(t, r, application)
}

func TestRegistryMigrationDownRetriesPromotionAfterReleasingSnapshotFence(t *testing.T) {
	for _, name := range []string{"complete", "cancel"} {
		t.Run(name, func(t *testing.T) {
			cancelAfterPromotion := name == "cancel"
			r, provider, application := registryRollbackFixture(t)
			reader := registryObservedReader(t, r)
			// The last table allows the first two ACCESS EXCLUSIVE promotions to
			// succeed before NOWAIT fails. The entire attempt must then release them.
			selectTx, err := r.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = selectTx.Rollback(context.Background()) }()
			var count int
			if err = selectTx.QueryRow(t.Context(), "SELECT count(*) FROM worker_http_origin_history").Scan(&count); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { _, err := provider.Down(ctx); done <- err }()
			defer cancel()
			finished := false
			t.Cleanup(func() {
				cancel()
				if !finished {
					<-done
				}
			})
			waitRegistryRollbackDrain(t, r, application)
			type result struct {
				snapshot RegistrySnapshot
				err      error
			}
			readDone := make(chan result, 1)
			go func() {
				snapshot, snapshotErr := reader.Snapshot(ctx)
				readDone <- result{snapshot, snapshotErr}
			}()
			waitRegistryLock(t, r.pool, reader.pool.Config().ConnConfig.RuntimeParams["application_name"], "ShareLock", false)
			var got result
			select {
			case got = <-readDone:
			case <-time.After(6 * time.Second):
				t.Fatal("failed promotion retained its advisory or partial table locks")
			}
			if got.err != nil || got.snapshot.ValidUntil.IsZero() {
				t.Fatal("snapshot could not finish after failed promotion", got.err)
			}
			// A new snapshot was issued after the failed attempt. Its transaction-local
			// drained marker must have rolled back too, so the retry needs a full drain.
			waitRegistryRollbackDrain(t, r, application)
			if cancelAfterPromotion {
				cancel()
				err = <-done
				finished = true
				if err == nil {
					t.Fatal("canceled promotion retry succeeded")
				}
				if err = selectTx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if version, versionErr := provider.GetDBVersion(t.Context()); versionErr != nil || version != 20261014164751 {
					t.Fatal("canceled promotion changed migration version", version, versionErr)
				}
				check, checkErr := r.pool.Begin(t.Context())
				if checkErr != nil {
					t.Fatal(checkErr)
				}
				defer func() { _ = check.Rollback(context.Background()) }()
				if _, err = check.Exec(t.Context(), "LOCK TABLE stream_nodes,worker_http_nodes,worker_http_origin_history IN ACCESS EXCLUSIVE MODE NOWAIT"); err != nil {
					t.Fatal("canceled promotion retained table locks or removed history", err)
				}
				var released bool
				if err = check.QueryRow(t.Context(), "SELECT pg_try_advisory_xact_lock(1397312591,1464816203)").Scan(&released); err != nil || !released {
					t.Fatal("canceled promotion retained its advisory fence", err)
				}
				if _, err = check.Exec(t.Context(), "DELETE FROM worker_http_origin_history"); err == nil {
					t.Fatal("canceled promotion removed append-only protection")
				} else if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "42501" {
					t.Fatal("unexpected append-only protection result", err)
				}
				return
			}
			if err = selectTx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				finished = true
			case <-time.After(6 * time.Second):
				t.Fatal("rollback did not finish after the SELECT released its table lock")
			}
			if err != nil {
				t.Fatal("rollback failed after retrying promotion", err)
			}
			if time.Now().Before(got.snapshot.ValidUntil) {
				t.Fatal("rollback skipped the fresh snapshot's drain after a failed promotion")
			}
			if version, versionErr := provider.GetDBVersion(t.Context()); versionErr != nil || version != 0 {
				t.Fatal("successful rollback retained its migration version", version, versionErr)
			}
		})
	}
}
