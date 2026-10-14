package workerregistration

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRegistrySavepointRollbackDoesNotReuseDrain(t *testing.T) {
	r := registrationDB(t)
	tx, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	sp, err := tx.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sp.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	if err = sp.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	var marker, xid string
	if err = tx.QueryRow(t.Context(), "SELECT coalesce(current_setting('silo.worker_registry_drained_xid',TRUE),''),pg_current_xact_id()::text").Scan(&marker, &xid); err != nil {
		t.Fatal(err)
	}
	if marker == xid {
		t.Fatal("rolled-back savepoint retained the successful drain marker")
	}
	// The savepoint released its fence; a different reader can now issue a
	// fresh certificate which the next mutation must drain again.
	snapshot, err := r.Snapshot(t.Context())
	if err != nil || len(snapshot.Nodes) != 0 || !time.Now().Before(snapshot.ValidUntil) {
		t.Fatalf("fresh reader after savepoint rollback: snapshot=%+v error=%v", snapshot, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), RegistrySnapshotTTL+100*time.Millisecond)
	defer cancel()
	if _, err = tx.Exec(ctx, "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err == nil {
		t.Fatal("second mutation reused a drain rolled back to a savepoint")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("second mutation failed without waiting for the drain:", err)
	}
	_ = tx.Rollback(context.Background())
	if _, found, lookupErr := r.ByURL(t.Context(), "https://worker.example.test"); lookupErr != nil || found {
		t.Fatalf("rolled-back mutations became committed classifications: found=%v error=%v", found, lookupErr)
	}
}
