package trickplay

import (
	"context"
	"testing"
	"time"
)

func TestStaleReconcileCannotDowngradeConcurrentUpgradeDB(t *testing.T) {
	f := newFixture(t)
	file := f.file(t, f.library(t, "movies", true), "rolling-reconcile")
	f.reconcile(t)
	f.generate(t, file, "server")
	conn, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `UPDATE media_file_trickplay SET state='pending', recipe_version=$2 WHERE media_file_id=$1`, file, AlgorithmVersion+1); err != nil {
		t.Fatal(err)
	}
	worker, err := f.pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Release()
	pid := worker.Conn().PgConn().PID()
	done := make(chan error, 1)
	go func() {
		_, err := worker.Exec(t.Context(), staleSQL, AlgorithmVersion, "different recipe", testStore, 100)
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("reconcile did not reach row lock")
		case <-tick.C:
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	row, _ := f.row(t, file)
	if row.version != AlgorithmVersion+1 || row.state != statePending {
		t.Fatalf("upgraded row overwritten: %+v", row)
	}
}
