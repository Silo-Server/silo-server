package worker

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSyncNowSerializesSnapshotCapture guards the SyncNow ordering contract:
// snapshot capture and reconciliation run under one lock, so a request-path
// sync (playback start/stop) can never interleave with the periodic tick and
// commit an older session snapshot after a newer one.
func TestSyncNowSerializesSnapshotCapture(t *testing.T) {
	var inflight atomic.Int32
	var overlapped atomic.Bool
	provider := func() []SessionSync {
		if inflight.Add(1) > 1 {
			overlapped.Store(true)
		}
		time.Sleep(2 * time.Millisecond)
		inflight.Add(-1)
		return nil
	}

	// No pool is needed: an empty snapshot with no node name returns before
	// any database work, keeping the test focused on the locking contract.
	r := NewReconciler(nil, "", provider, uuid.New())

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := r.SyncNow(context.Background()); err != nil {
				t.Errorf("SyncNow: %v", err)
			}
		}()
	}
	wg.Wait()

	if overlapped.Load() {
		t.Fatal("concurrent SyncNow calls captured snapshots concurrently; capture and reconcile must be serialized")
	}
}

// TestSyncNowCoalescesPendingPass guards the follow-up contract: a SyncNow
// call that arrives while a sync is in flight returns immediately, and the
// running owner re-captures a fresh snapshot afterwards — so a stop that lands
// mid-sync is still reflected without waiting for the periodic tick.
func TestSyncNowCoalescesPendingPass(t *testing.T) {
	captures := make(chan struct{}, 16)
	release := make(chan struct{})
	first := true
	provider := func() []SessionSync {
		captures <- struct{}{}
		if first {
			first = false
			<-release // hold the first sync mid-flight
		}
		return nil
	}
	r := NewReconciler(nil, "", provider, uuid.New())

	ownerDone := make(chan error, 1)
	go func() { ownerDone <- r.SyncNow(context.Background()) }()
	<-captures // owner is now blocked inside its snapshot capture

	// A second sync while the first is in flight must not block.
	done := make(chan struct{})
	go func() {
		_ = r.SyncNow(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SyncNow blocked behind an in-flight sync; it must coalesce and return")
	}

	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatalf("owner SyncNow: %v", err)
	}
	select {
	case <-captures: // the owner's follow-up pass with a fresh snapshot
	default:
		t.Fatal("no follow-up snapshot capture ran; the coalesced sync was lost")
	}
}

func TestSessionSnapshotsEqualIncludesSessionGeneration(t *testing.T) {
	left := []SessionSync{{SessionID: "session-1", SessionGeneration: "generation-a"}}
	right := []SessionSync{{SessionID: "session-1", SessionGeneration: "generation-b"}}
	if sessionSnapshotsEqual(left, right) {
		t.Fatal("snapshots with different session generations must not compare equal")
	}
}

func TestNormalizeSessionSyncsTreatsPersistedLegacySentinelAsPublicEmpty(t *testing.T) {
	left := normalizeSessionSyncs("api-1", []SessionSync{{SessionID: "legacy", SessionGeneration: legacySessionGenerationUUID}})
	right := normalizeSessionSyncs("api-1", []SessionSync{{SessionID: "legacy", SessionGeneration: ""}})
	if !sessionSnapshotsEqual(left, right) {
		t.Fatalf("legacy sentinel caused a perpetual sync diff: left=%+v right=%+v", left, right)
	}
}

func workerIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SILO_TEST_DATABASE_URL to run worker database tests")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestReconcileNodeSessionsRecordsFreshEmptyWatermark(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "reconcile-empty-test"
	_, _ = pool.Exec(t.Context(), `DELETE FROM playback_sessions_sync WHERE reporting_node=$1`, node)
	_, _ = pool.Exec(t.Context(), `DELETE FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node)
	reconciler := NewReconciler(pool, node, nil, uuid.New())
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, nil); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	var firstGeneration string
	var count int
	if err := pool.QueryRow(t.Context(), `SELECT reconciliation_generation::text, session_count FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node).Scan(&firstGeneration, &count); err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if firstGeneration == "" || count != 0 {
		t.Fatalf("watermark = (%q, %d), want nonempty generation and zero count", firstGeneration, count)
	}
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, nil); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var secondGeneration string
	if err := pool.QueryRow(t.Context(), `SELECT reconciliation_generation::text FROM playback_session_snapshot_watermarks WHERE reporting_node=$1`, node).Scan(&secondGeneration); err != nil {
		t.Fatalf("read second watermark: %v", err)
	}
	if secondGeneration == firstGeneration {
		t.Fatal("each completed reconciliation must mint a new watermark generation")
	}
}

func TestReconcileNodeSessionsPreservesGenerationAndStartedAtOnUpdate(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "reconcile-identity-test"
	startedAt := time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC)
	session := SessionSync{
		SessionID:         "reconcile-session-identity",
		SessionGeneration: "0aad4ec5-2061-48e7-958c-9ae2294aed2d",
		UserID:            7,
		MediaFileID:       42,
		PlayMethod:        "direct",
		ReportingNode:     node,
		StartedAt:         startedAt,
		UpdatedAt:         startedAt,
	}
	reconciler := NewReconciler(pool, node, nil, uuid.New())
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, []SessionSync{session}); err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	session.PositionSeconds = 30
	session.UpdatedAt = startedAt.Add(time.Minute)
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, []SessionSync{session}); err != nil {
		t.Fatalf("update reconcile: %v", err)
	}
	var generation string
	var gotStartedAt time.Time
	if err := pool.QueryRow(t.Context(), `SELECT session_generation::text, started_at FROM playback_sessions_sync WHERE session_id=$1`, session.SessionID).Scan(&generation, &gotStartedAt); err != nil {
		t.Fatalf("read session identity: %v", err)
	}
	if generation != session.SessionGeneration || !gotStartedAt.Equal(startedAt) {
		t.Fatalf("identity = (%q, %s), want (%q, %s)", generation, gotStartedAt, session.SessionGeneration, startedAt)
	}
}

func TestReconcileLegacySessionGenerationUsesIncompleteSentinel(t *testing.T) {
	pool := workerIntegrationPool(t)
	const node = "reconcile-legacy-generation-test"
	session := SessionSync{
		SessionID:     "reconcile-legacy-session",
		UserID:        7,
		MediaFileID:   42,
		PlayMethod:    "direct",
		ReportingNode: node,
		StartedAt:     time.Now().UTC().Add(-time.Minute),
		UpdatedAt:     time.Now().UTC(),
	}
	reconciler := NewReconciler(pool, node, nil, uuid.New())
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, []SessionSync{session}); err != nil {
		t.Fatalf("legacy reconcile: %v", err)
	}
	var generation string
	if err := pool.QueryRow(t.Context(), `SELECT session_generation::text FROM playback_sessions_sync WHERE session_id=$1`, session.SessionID).Scan(&generation); err != nil {
		t.Fatalf("read legacy generation: %v", err)
	}
	if generation != legacySessionGenerationUUID {
		t.Fatalf("legacy generation = %q, want incomplete sentinel %q", generation, legacySessionGenerationUUID)
	}
	if err := reconciler.ReconcileNodeSessions(t.Context(), node, []SessionSync{session}); err != nil {
		t.Fatalf("legacy update reconcile: %v", err)
	}
}
