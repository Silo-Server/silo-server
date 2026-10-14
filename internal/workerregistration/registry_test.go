package workerregistration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/nodepool"
)

func TestRegistryRetiredOriginRemainsClassified(t *testing.T) {
	r := registrationDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", []byte(public)); err != nil {
		t.Fatal(err)
	}
	if _, err = r.pool.Exec(t.Context(), "DELETE FROM worker_http_nodes WHERE node_id=17"); err != nil {
		t.Fatal(err)
	}
	node, found, err := r.ByURL(t.Context(), "https://worker.example.test")
	if err != nil || !found || node.Enabled {
		t.Fatalf("retired enrolled origin can become legacy: found=%v enabled=%v err=%v", found, node.Enabled, err)
	}
}

func TestRegistryMutationCancellationPreservesMembership(t *testing.T) {
	r := registrationDB(t)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), RegistrySnapshotTTL+100*time.Millisecond)
	defer cancel()
	if _, err = r.pool.Exec(ctx, "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", []byte(public)); err == nil {
		t.Fatal("protocol mutation committed before old classification snapshots could drain")
	}
	var count int
	if err = r.pool.QueryRow(t.Context(), "SELECT count(*) FROM worker_http_nodes").Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled mutation changed membership: count=%d err=%v", count, err)
	}
}

func TestRegistrySnapshotWaitsForFreshWriterCommit(t *testing.T) {
	r := registrationDB(t)
	reader := registryObservedReader(t, r)
	tx, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = nodepool.LockConfiguration(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot RegistrySnapshot
		err      error
	}
	results := make(chan result, 1)
	go func() {
		snapshot, snapshotErr := reader.Snapshot(t.Context())
		results <- result{snapshot, snapshotErr}
	}()
	waitRegistryLock(t, r.pool, reader.pool.Config().ConnConfig.RuntimeParams["application_name"], "ShareLock", false)
	if _, err = tx.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	beforeCommit := time.Now()
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := <-results
	if got.err != nil || len(got.snapshot.Nodes) != 1 || got.snapshot.Nodes[0].ID != 17 || !slices.Contains(got.snapshot.Origins, "https://worker.example.test") {
		t.Fatalf("waiting reader used a pre-lock SQL snapshot: %+v err=%v", got.snapshot, got.err)
	}
	if got.snapshot.ValidUntil.Before(beforeCommit.Add(RegistrySnapshotTTL)) || !time.Now().Before(got.snapshot.ValidUntil) {
		t.Fatalf("writer lock wait consumed the new certificate: expires=%s before_commit=%s", got.snapshot.ValidUntil, beforeCommit)
	}
}

func TestRegistrySnapshotHoldsSharedFenceUntilReadCompletes(t *testing.T) {
	r := registrationDB(t)
	reader := registryObservedReader(t, r)
	blockedTable, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blockedTable.Rollback(context.Background()) }()
	if _, err = blockedTable.Exec(t.Context(), "LOCK TABLE stream_nodes IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot RegistrySnapshot
		err      error
	}
	readDone := make(chan result, 1)
	go func() {
		snapshot, snapshotErr := reader.Snapshot(t.Context())
		readDone <- result{snapshot, snapshotErr}
	}()
	appName := reader.pool.Config().ConnConfig.RuntimeParams["application_name"]
	waitRegistryLock(t, r.pool, appName, "ShareLock", true)
	writer := registryObservedReader(t, r)
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := writer.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", make([]byte, ed25519.PublicKeySize))
		writeDone <- writeErr
	}()
	waitRegistryLock(t, r.pool, writer.pool.Config().ConnConfig.RuntimeParams["application_name"], "ExclusiveLock", false)
	// The first data SELECT is blocked while the shared fence remains held.
	// Let the actual certificate lifetime expire, then prove completion does
	// not restart it. Waiting for expiry is the behavior under test.
	expiry, cancelExpiry := context.WithTimeout(t.Context(), RegistrySnapshotTTL+100*time.Millisecond)
	<-expiry.Done()
	cancelExpiry()
	if err = blockedTable.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	read := <-readDone
	if read.err != nil {
		t.Fatal(read.err)
	}
	if time.Now().Before(read.snapshot.ValidUntil) || read.snapshot.ValidUntil.IsZero() {
		t.Fatal("slow first data read restarted the certificate lifetime:", read.snapshot.ValidUntil)
	}
	if err = <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestRegistrySnapshotCancellationDoesNotPublishPartialState(t *testing.T) {
	r := registrationDB(t)
	tx, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = nodepool.LockConfiguration(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	snapshot, err := r.Snapshot(ctx)
	if err == nil || len(snapshot.Nodes) != 0 || len(snapshot.Origins) != 0 {
		t.Fatalf("canceled snapshot published state: %+v err=%v", snapshot, err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Snapshot(t.Context()); err != nil {
		t.Fatal("canceled read retained its database lock:", err)
	}
}

func TestRegistryHistorySurvivesMovesTypeChangesAndParentDeletion(t *testing.T) {
	r := registrationDB(t)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	rolledBack := "https://rolled-back.example.test/prefix"
	tx, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", rolledBack); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, found, lookupErr := r.ByURL(t.Context(), "https://rolled-back.example.test"); lookupErr != nil || found {
		t.Fatalf("rollback retained an uncommitted origin: found=%v err=%v", found, lookupErr)
	}
	tx, err = r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	first := "HTTPS://MOVED.example.test:0443/prefix?changed=1#fragment"
	last := "https://retired.example.test:443/?changed=2"
	if _, err = tx.Exec(t.Context(), "UPDATE stream_nodes SET url=$1,type='stream' WHERE id=17", first); err != nil {
		t.Fatal(err)
	}
	// Only the first actual change in a transaction drains. A timeout shorter
	// than the drain proves later row triggers do not repeat the three seconds.
	short, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err = tx.Exec(short, "UPDATE stream_nodes SET url=$1,type='transcode' WHERE id=17", last); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(short, "DELETE FROM stream_nodes WHERE id=17"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://worker.example.test", "https://moved.example.test", "https://retired.example.test:0443"} {
		node, found, lookupErr := r.ByURL(t.Context(), origin)
		if lookupErr != nil || !found || node.Enabled {
			t.Fatalf("old or new origin became legacy after delete: %s found=%v enabled=%v err=%v", origin, found, node.Enabled, lookupErr)
		}
	}
	snapshot, err := r.Snapshot(t.Context())
	if err != nil || len(snapshot.Nodes) != 0 || len(snapshot.Origins) != 3 || !slices.Contains(snapshot.Origins, first) || !slices.Contains(snapshot.Origins, last) {
		t.Fatalf("raw origin history changed or cascaded: %+v err=%v", snapshot, err)
	}
}

func TestRegistryNoopAndHealthWritesDoNotDrain(t *testing.T) {
	r := registrationDB(t)
	registryAddAdminConfigurationSchema(t, r)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	// Acquire the database-wide fence before measuring each operation. Other
	// packages can mutate their own schemas concurrently; their lock queue must
	// not consume the one-second budget which detects an unexpected drain.
	// This fresh transaction has not drained, so a faulty row trigger still
	// exceeds that budget even though it already owns the exclusive fence.
	tx, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()
	if err = nodepool.LockConfiguration(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	healthCtx, cancelHealth := context.WithTimeout(t.Context(), time.Second)
	err = nodepool.NewRepository(r.pool).UpdateHealth(healthCtx, 17, "https://worker.example.test", true, 1, 40, nil, nil)
	cancelHealth()
	if err != nil {
		t.Fatal("ordinary health sample drained classification snapshots:", err)
	}
	for _, query := range []string{
		"UPDATE stream_nodes SET enabled=NOT enabled WHERE id=17",
		"UPDATE stream_nodes SET url=url,type=type WHERE id=17",
		"UPDATE worker_http_nodes SET public_key=public_key,allow_http=allow_http,node_id=node_id WHERE node_id=17",
	} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		_, err := tx.Exec(ctx, query)
		cancel()
		if err != nil {
			t.Fatalf("non-protocol write drained snapshots: %s: %v", query, err)
		}
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryHistoryInsertCannotBypassDrain(t *testing.T) {
	r := registrationDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), RegistrySnapshotTTL+100*time.Millisecond)
	defer cancel()
	if _, err := r.pool.Exec(ctx, "INSERT INTO worker_http_origin_history(raw_url) VALUES('https://reserved.example.test')"); err == nil {
		t.Fatal("direct history insertion committed before live legacy snapshots drained")
	}
	if _, found, err := r.ByURL(t.Context(), "https://reserved.example.test"); err != nil || found {
		t.Fatalf("canceled insertion changed history: found=%v err=%v", found, err)
	}
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_origin_history(raw_url) VALUES('https://reserved.example.test')"); err != nil {
		t.Fatal(err)
	}
	if node, found, err := r.ByURL(t.Context(), "https://reserved.example.test"); err != nil || !found || node.Enabled {
		t.Fatalf("direct fenced history failed to deny legacy transport: %+v found=%v err=%v", node, found, err)
	}
}

func TestRegistryTypeChangeCannotRestoreLegacyTransport(t *testing.T) {
	r := registrationDB(t)
	key := registryTestPublicKey(t)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1),(18,$1)", key); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(t.Context(), "UPDATE stream_nodes SET type=CASE WHEN id=17 THEN 'proxy' ELSE 'invalid-type' END WHERE id IN (17,18)"); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"https://worker.example.test", "https://other-worker.example.test"} {
		if node, found, err := r.ByURL(t.Context(), origin); err != nil || !found || node.Enabled {
			t.Fatalf("non-transcode type restored legacy transport: %s %+v found=%v err=%v", origin, node, found, err)
		}
	}
	if snapshot, err := r.Snapshot(t.Context()); err != nil || len(snapshot.Nodes) != 0 || len(snapshot.Origins) != 2 {
		t.Fatalf("snapshot lost classification after type change: %+v err=%v", snapshot, err)
	}
}

func TestRegistryHistoryAndTruncateCannotRemoveClassification(t *testing.T) {
	r := registrationDB(t)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"UPDATE worker_http_origin_history SET raw_url='https://changed.example.test'",
		"DELETE FROM worker_http_origin_history",
		"TRUNCATE worker_http_origin_history",
		"TRUNCATE worker_http_nodes",
		"TRUNCATE stream_nodes CASCADE",
	} {
		_, err := r.pool.Exec(t.Context(), query)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("unsafe registry removal was not explicitly rejected: %s: %v", query, err)
		}
	}
	node, found, err := r.ByURL(t.Context(), "https://worker.example.test")
	if err != nil || !found || !node.Enabled {
		t.Fatalf("failed history mutation changed membership: %+v found=%v err=%v", node, found, err)
	}
}

func TestRegistryCanceledRedeemPreservesTicketAndBinding(t *testing.T) {
	r := registrationDB(t)
	ticket, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	key := ed25519.PublicKey(registryTestPublicKey(t))
	if err = r.Redeem(ctx, ticket, key); err == nil {
		t.Fatal("redemption committed without draining live snapshots")
	}
	var consumed bool
	digest := enrollmentDigest(ticket)
	if err = r.pool.QueryRow(t.Context(), "SELECT consumed_at IS NOT NULL FROM worker_http_enrollments WHERE ticket_hash=$1", digest[:]).Scan(&consumed); err != nil || consumed {
		t.Fatalf("canceled drain consumed ticket: consumed=%v err=%v", consumed, err)
	}
	if err = r.Redeem(t.Context(), ticket, key); err != nil {
		t.Fatal("canceled redemption prevented valid retry:", err)
	}
	node, found, err := r.ByID(t.Context(), 17)
	if err != nil || !found || !slices.Equal(node.PublicKey, key) {
		t.Fatalf("redemption lost original binding: %+v found=%v err=%v", node, found, err)
	}
}

func TestRegistryMigrationBackfillsExistingEnrollmentOrigins(t *testing.T) {
	r := registrationDBWithRegistryFence(t, false)
	rawURL := "HTTPS://Existing.example.test:0443/invalid-prefix"
	if _, err := r.pool.Exec(t.Context(), "UPDATE stream_nodes SET url=$1 WHERE id=17", rawURL); err != nil {
		t.Fatal(err)
	}
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20261014164751_worker_registry_classification_fence.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = r.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.Snapshot(t.Context())
	if err != nil || !slices.Contains(snapshot.Origins, rawURL) {
		t.Fatalf("migration omitted preexisting origin: %+v err=%v", snapshot, err)
	}
	if _, err = r.pool.Exec(t.Context(), "DELETE FROM worker_http_nodes WHERE node_id=17"); err != nil {
		t.Fatal(err)
	}
	if _, found, lookupErr := r.ByURL(t.Context(), "https://existing.example.test"); lookupErr != nil || !found {
		t.Fatalf("backfilled malformed origin became legacy: found=%v err=%v", found, lookupErr)
	}
}

func TestRegistryMigrationDownDrainsBeforeRemovingFence(t *testing.T) {
	r := registrationDB(t)
	migration, err := os.ReadFile("../../migrations/sql/20261014164751_worker_registry_classification_fence.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, down, found := strings.Cut(string(migration), "-- +goose Down")
	if !found {
		t.Fatal("migration has no rollback")
	}
	ctx, cancel := context.WithTimeout(t.Context(), RegistrySnapshotTTL+100*time.Millisecond)
	defer cancel()
	if _, err = r.pool.Exec(ctx, down); err == nil {
		t.Fatal("migration removed the mutation fence while old certificates could remain valid")
	}
	if _, err = r.Snapshot(t.Context()); err != nil {
		t.Fatal("canceled rollback partially removed the registry fence:", err)
	}
	if _, err = r.pool.Exec(t.Context(), down); err != nil {
		t.Fatal(err)
	}
	var removed bool
	if err = r.pool.QueryRow(t.Context(), "SELECT to_regclass('worker_http_origin_history') IS NULL").Scan(&removed); err != nil || !removed {
		t.Fatalf("successful rollback retained fence tables: removed=%v err=%v", removed, err)
	}
}

func TestRegistryAdminAndEnrollmentAcquireFenceBeforeRows(t *testing.T) {
	r := registrationDB(t)
	registryAddAdminConfigurationSchema(t, r)
	ticket, err := r.Issue(t.Context(), 17, "https://core.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if err = nodepool.LockConfiguration(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	admin := registryObservedReader(t, r)
	enrollment := registryObservedReader(t, r)
	adminDone := make(chan error, 1)
	go func() {
		_, _, updateErr := nodepool.NewAdminConfigurationStore(admin.pool).Update(t.Context(), 17,
			nodepool.UpdateNodeInput{Name: new("Updated concurrently")}, func(int64) error { return nil })
		adminDone <- updateErr
	}()
	waitRegistryLock(t, r.pool, admin.pool.Config().ConnConfig.RuntimeParams["application_name"], "ExclusiveLock", false)
	enrollmentDone := make(chan error, 1)
	key := ed25519.PublicKey(registryTestPublicKey(t))
	go func() { enrollmentDone <- enrollment.Redeem(t.Context(), ticket, key) }()
	waitRegistryLock(t, r.pool, enrollment.pool.Config().ConnConfig.RuntimeParams["application_name"], "ExclusiveLock", false)
	// Both real callers are waiting for the global lock. Neither may already
	// hold the parent or one-use ticket row, which would reverse trigger order.
	probe, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Rollback(context.Background()) }()
	var id int
	if err = probe.QueryRow(t.Context(), "SELECT id FROM stream_nodes WHERE id=17 FOR UPDATE NOWAIT").Scan(&id); err != nil {
		t.Fatal("administrator or enrollment locked parent before global fence:", err)
	}
	digest := enrollmentDigest(ticket)
	if err = probe.QueryRow(t.Context(), "SELECT node_id FROM worker_http_enrollments WHERE ticket_hash=$1 FOR UPDATE NOWAIT", digest[:]).Scan(&id); err != nil {
		t.Fatal("enrollment locked ticket before global fence:", err)
	}
	if err = probe.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-adminDone; err != nil {
		t.Fatal("normal administrator writer deadlocked with enrollment:", err)
	}
	if err = <-enrollmentDone; err != nil {
		t.Fatal("normal enrollment deadlocked with administrator:", err)
	}
	node, err := nodepool.NewRepository(r.pool).GetByID(t.Context(), 17)
	if err != nil || node.Name != "Updated concurrently" {
		t.Fatalf("serialized administrator update lost: %+v err=%v", node, err)
	}
	registered, found, err := r.ByID(t.Context(), 17)
	if err != nil || !found || !slices.Equal(registered.PublicKey, key) {
		t.Fatalf("serialized enrollment lost: %+v found=%v err=%v", registered, found, err)
	}
}

func TestRegistryAdminDeleteAcquiresFenceBeforeParentRow(t *testing.T) {
	r := registrationDB(t)
	registryAddAdminConfigurationSchema(t, r)
	if _, err := r.pool.Exec(t.Context(), "INSERT INTO worker_http_nodes(node_id,public_key) VALUES(17,$1)", registryTestPublicKey(t)); err != nil {
		t.Fatal(err)
	}
	blocker, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if err = nodepool.LockConfiguration(t.Context(), blocker); err != nil {
		t.Fatal(err)
	}
	admin := registryObservedReader(t, r)
	done := make(chan error, 1)
	go func() {
		done <- nodepool.NewAdminConfigurationStore(admin.pool).Delete(t.Context(), 17, func(int64) error { return nil })
	}()
	waitRegistryLock(t, r.pool, admin.pool.Config().ConnConfig.RuntimeParams["application_name"], "ExclusiveLock", false)
	probe, err := r.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Rollback(context.Background()) }()
	var id int
	if err = probe.QueryRow(t.Context(), "SELECT id FROM stream_nodes WHERE id=17 FOR UPDATE NOWAIT").Scan(&id); err != nil {
		t.Fatal("administrator deletion locked parent before global fence:", err)
	}
	if err = probe.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal("normal administrator deletion failed:", err)
	}
	if _, found, lookupErr := r.ByID(t.Context(), 17); lookupErr != nil || found {
		t.Fatalf("deleted node survived: found=%v err=%v", found, lookupErr)
	}
	if _, found, lookupErr := r.ByURL(t.Context(), "https://worker.example.test"); lookupErr != nil || !found {
		t.Fatalf("administrator deletion restored legacy classification: found=%v err=%v", found, lookupErr)
	}
}

func registryAddAdminConfigurationSchema(t *testing.T, r *Repository) {
	t.Helper()
	_, err := r.pool.Exec(t.Context(), `ALTER TABLE stream_nodes
ADD COLUMN name TEXT NOT NULL DEFAULT 'Synthetic', ADD COLUMN public_url TEXT,
ADD COLUMN healthy BOOLEAN NOT NULL DEFAULT FALSE, ADD COLUMN active_jobs INTEGER NOT NULL DEFAULT 0,
ADD COLUMN node_group TEXT, ADD COLUMN max_jobs INTEGER, ADD COLUMN max_bandwidth_kbps INTEGER,
ADD COLUMN egress_kbps INTEGER NOT NULL DEFAULT 0, ADD COLUMN last_health_check TIMESTAMPTZ,
ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now(), ADD COLUMN capabilities JSONB,
ADD COLUMN capabilities_hash TEXT, ADD COLUMN capabilities_refreshed_at TIMESTAMPTZ,
ADD COLUMN last_stats JSONB, ADD COLUMN hw_accel_override TEXT, ADD COLUMN hw_device_override TEXT,
ADD COLUMN capability_drift TEXT, ADD COLUMN capability_drift_baseline JSONB,
ADD COLUMN network_access JSONB NOT NULL DEFAULT '{}', ADD COLUMN download_artifact_dir_override TEXT,
ADD COLUMN download_artifact_max_bytes_override BIGINT, ADD COLUMN drain_requested BOOLEAN NOT NULL DEFAULT FALSE,
ADD COLUMN drain_state TEXT NOT NULL DEFAULT 'none'`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906205459_node_admin_configuration_revisions.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	if _, err = r.pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
}

func registryTestPublicKey(t *testing.T) []byte {
	t.Helper()
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func registryObservedReader(t *testing.T, r *Repository) *Repository {
	t.Helper()
	cfg := r.pool.Config()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "registry_" + hex.EncodeToString(nonce[:])
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return New(pool, r.cipher)
}

func waitRegistryLock(t *testing.T, pool *pgxpool.Pool, application, mode string, granted bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid
WHERE l.locktype='advisory' AND l.classid=$1 AND l.objid=$2 AND l.objsubid=2
AND a.application_name=$3 AND l.mode=$4 AND l.granted=$5`, nodepool.ConfigurationLockNamespace, nodepool.WorkerRegistryLockKey, application, mode, granted).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("registry operation never reached its observable advisory lock", mode, granted)
		case <-ticker.C:
		}
	}
}
