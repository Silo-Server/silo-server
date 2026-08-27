package scanqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestStaleScanClaimCannotMutateSuccessor(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)

	tests := []struct {
		name   string
		mutate func(context.Context, *Repository, *models.ScanRun) error
	}{
		{
			name: "heartbeat",
			mutate: func(ctx context.Context, repo *Repository, claim *models.ScanRun) error {
				return repo.TouchHeartbeat(ctx, claim.ID, claim.ClaimToken)
			},
		},
		{
			name: "progress",
			mutate: func(ctx context.Context, repo *Repository, claim *models.ScanRun) error {
				_, err := repo.UpdateProgress(ctx, claim.ID, claim.ClaimToken, &evt.ScanRunResult{FilesProcessed: 1})
				return err
			},
		},
		{
			name: "success",
			mutate: func(ctx context.Context, repo *Repository, claim *models.ScanRun) error {
				_, err := repo.Complete(ctx, claim.ID, claim.ClaimToken, &evt.ScanRunResult{FilesProcessed: 1})
				return err
			},
		},
		{
			name: "failure",
			mutate: func(ctx context.Context, repo *Repository, claim *models.ScanRun) error {
				_, err := repo.Fail(ctx, claim.ID, claim.ClaimToken, "stale worker")
				return err
			},
		},
		{
			name: "cancellation",
			mutate: func(ctx context.Context, repo *Repository, claim *models.ScanRun) error {
				_, err := repo.CancelClaim(ctx, claim.ID, claim.ClaimToken)
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			first := createAndClaimScanRun(t, pool, repo)
			successor := expireRequeueAndReclaim(t, pool, repo, first)

			if err := tt.mutate(ctx, repo, first); !errors.Is(err, ErrScanRunClaimLost) {
				t.Fatalf("stale %s error = %v, want %v", tt.name, err, ErrScanRunClaimLost)
			}

			current, err := repo.GetByID(ctx, successor.ID)
			if err != nil {
				t.Fatalf("load successor after stale %s: %v", tt.name, err)
			}
			if current.Status != StatusRunning || current.ClaimToken != successor.ClaimToken {
				t.Fatalf("successor after stale %s = status:%q token:%q, want running token %q", tt.name, current.Status, current.ClaimToken, successor.ClaimToken)
			}

			completed, err := repo.Complete(ctx, successor.ID, successor.ClaimToken, &evt.ScanRunResult{FilesProcessed: 2})
			if err != nil {
				t.Fatalf("complete legitimate successor after stale %s: %v", tt.name, err)
			}
			if completed.Status != StatusCompleted {
				t.Fatalf("legitimate successor status = %q, want %q", completed.Status, StatusCompleted)
			}
		})
	}
}

func TestScanServiceCancelsIngestionWhenClaimIsLost(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	first := createAndClaimScanRun(t, pool, repo)
	ingester := &blockingScanIngester{
		started:   make(chan struct{}),
		canceled:  make(chan struct{}),
		committed: make(chan struct{}),
	}
	service := NewService(
		repo,
		fixedFolderLoader{folder: &models.MediaFolder{ID: first.MediaFolderID, Enabled: true}},
		ingester,
		nil,
		t.Context(),
		1,
		1,
	)
	service.heartbeatInterval = 20 * time.Millisecond
	service.staleAfter = 10 * time.Second

	processed := make(chan struct{})
	go func() {
		service.process(first)
		close(processed)
	}()
	waitForScanSignal(t, ingester.started, "ingestion to start")

	successor := expireRequeueAndReclaim(t, pool, repo, first)
	waitForScanSignal(t, ingester.canceled, "stale ingestion to observe claim loss")
	waitForScanSignal(t, processed, "stale scan process to stop")
	select {
	case <-ingester.committed:
		t.Fatal("stale ingestion ran a post-commit callback")
	default:
	}

	current, err := repo.GetByID(t.Context(), successor.ID)
	if err != nil {
		t.Fatalf("load successor after stale ingestion stopped: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != successor.ClaimToken {
		t.Fatalf("successor after stale ingestion stopped = status:%q token:%q, want running token %q", current.Status, current.ClaimToken, successor.ClaimToken)
	}

	successorCommitted := make(chan struct{})
	successorService := NewService(
		repo,
		fixedFolderLoader{folder: &models.MediaFolder{ID: successor.MediaFolderID, Enabled: true}},
		successfulCommitIngester{committed: successorCommitted},
		nil,
		t.Context(),
		1,
		1,
	)
	successorService.heartbeatInterval = 20 * time.Millisecond
	successorService.staleAfter = 10 * time.Second
	successorService.process(successor)
	waitForScanSignal(t, successorCommitted, "successor post-commit callback")
	completed, err := repo.GetByID(t.Context(), successor.ID)
	if err != nil {
		t.Fatalf("load completed successor: %v", err)
	}
	if completed.Status != StatusCompleted {
		t.Fatalf("successor status = %q, want %q", completed.Status, StatusCompleted)
	}
}

func TestScanServiceLocalLeaseExpiryCancelsIngestion(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	ingester := &blockingScanIngester{
		started:   make(chan struct{}),
		canceled:  make(chan struct{}),
		committed: make(chan struct{}),
	}
	service := NewService(
		repo,
		fixedFolderLoader{folder: &models.MediaFolder{ID: claim.MediaFolderID, Enabled: true}},
		ingester,
		nil,
		t.Context(),
		1,
		1,
	)
	service.heartbeatInterval = time.Hour
	service.staleAfter = 50 * time.Millisecond

	processed := make(chan struct{})
	go func() {
		service.process(claim)
		close(processed)
	}()
	waitForScanSignal(t, ingester.started, "ingestion to start")
	waitForScanSignal(t, ingester.canceled, "local lease expiry to cancel ingestion")
	waitForScanSignal(t, processed, "lease-expired scan process to stop")

	select {
	case <-ingester.committed:
		t.Fatal("lease-expired ingestion ran a post-commit callback")
	default:
	}
}

func TestScanServiceRejectsReclaimedClaimBeforeIngestionStarts(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	first := createAndClaimScanRun(t, pool, repo)
	successor := expireRequeueAndReclaim(t, pool, repo, first)
	ingester := &recordingScanIngester{}
	service := NewService(
		repo,
		fixedFolderLoader{folder: &models.MediaFolder{ID: first.MediaFolderID, Enabled: true}},
		ingester,
		nil,
		t.Context(),
		1,
		1,
	)

	service.process(first)
	if calls := ingester.calls.Load(); calls != 0 {
		t.Fatalf("stale claim started ingestion %d times, want 0", calls)
	}
	current, err := repo.GetByID(t.Context(), successor.ID)
	if err != nil {
		t.Fatalf("load successor after stale process resumed: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != successor.ClaimToken {
		t.Fatalf("successor after stale process resumed = status:%q token:%q", current.Status, current.ClaimToken)
	}
}

func TestScanServiceRegistersCancellationBeforeInitialHeartbeat(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	ingester := &recordingScanIngester{}
	service := NewService(
		repo,
		fixedFolderLoader{folder: &models.MediaFolder{ID: claim.MediaFolderID, Enabled: true}},
		ingester,
		nil,
		t.Context(),
		1,
		1,
	)

	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin heartbeat blocker: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lockedID string
	if err := tx.QueryRow(t.Context(), `SELECT id FROM scan_runs WHERE id = $1 FOR UPDATE`, claim.ID).Scan(&lockedID); err != nil {
		t.Fatalf("lock claimed row: %v", err)
	}

	processed := make(chan struct{})
	go func() {
		service.process(claim)
		close(processed)
	}()
	cancel := waitForTrackedScanCancel(t, service, claim)
	cancel()
	waitForScanSignal(t, processed, "startup heartbeat to stop after local cancellation")
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("release heartbeat blocker: %v", err)
	}
	if calls := ingester.calls.Load(); calls != 0 {
		t.Fatalf("canceled startup began ingestion %d times, want 0", calls)
	}

	canceled, err := repo.CancelActiveByLibrary(t.Context(), claim.MediaFolderID)
	if err != nil {
		t.Fatalf("durably cancel startup claim: %v", err)
	}
	if len(canceled) != 1 || canceled[0].Status != StatusCancelled {
		t.Fatalf("durably canceled runs = %#v, want one canceled run", canceled)
	}
}

func TestScanServiceLeaseExpiryAtCompletionBoundaryLeavesRunForRetry(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	service := NewService(repo, nil, nil, nil, t.Context(), 1, 1)
	parent, cancel := context.WithCancelCause(t.Context())
	ctx, lease := newClaimLeaseContext(parent, cancel, time.Minute, time.Now())
	lease.deadline.Store(time.Now().Add(-time.Second).UnixNano())

	if service.finishRun(ctx, lease, claim, &libraryingest.Result{}, nil) {
		t.Fatal("expired claim completed successfully")
	}
	current, err := repo.GetByID(t.Context(), claim.ID)
	if err != nil {
		t.Fatalf("load expired claim after completion boundary: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != claim.ClaimToken {
		t.Fatalf("expired claim = status:%q token:%q, want running for stale requeue", current.Status, current.ClaimToken)
	}
}

func TestLegacyStaleWorkerCannotMutateDistinctSuccessor(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	first := createAndClaimScanRun(t, pool, repo)
	successor := expireRequeueAndReclaim(t, pool, repo, first)

	legacyWrites := []struct {
		name string
		sql  string
	}{
		{
			name: "heartbeat",
			sql:  `UPDATE scan_runs SET heartbeat_at = NOW(), updated_at = NOW() WHERE id = $1 AND status = 'running'`,
		},
		{
			name: "progress",
			sql:  `UPDATE scan_runs SET result_payload = '{}'::jsonb, updated_at = NOW() WHERE id = $1 AND status = 'running'`,
		},
		{
			name: "success",
			sql:  `UPDATE scan_runs SET status = 'completed', completed_at = NOW(), updated_at = NOW() WHERE id = $1 AND status = 'running'`,
		},
		{
			name: "failure",
			sql:  `UPDATE scan_runs SET status = 'failed', completed_at = NOW(), updated_at = NOW() WHERE id = $1 AND status = 'running'`,
		},
	}
	for _, write := range legacyWrites {
		t.Run(write.name, func(t *testing.T) {
			tag, err := pool.Exec(t.Context(), write.sql, first.ID)
			if err != nil {
				t.Fatalf("legacy stale %s: %v", write.name, err)
			}
			if tag.RowsAffected() != 0 {
				t.Fatalf("legacy stale %s affected %d rows, want 0", write.name, tag.RowsAffected())
			}
		})
	}

	current, err := repo.GetByID(t.Context(), successor.ID)
	if err != nil {
		t.Fatalf("load successor after legacy writes: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != successor.ClaimToken {
		t.Fatalf("successor after legacy writes = status:%q token:%q, want running token %q", current.Status, current.ClaimToken, successor.ClaimToken)
	}
}

func TestLegacyTokenlessClaimIsNotAutomaticallyRequeued(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	if _, err := pool.Exec(t.Context(), `
		UPDATE scan_runs
		SET claim_token = NULL, heartbeat_at = '1900-01-01'
		WHERE id = $1`, claim.ID); err != nil {
		t.Fatalf("convert claim to legacy tokenless row: %v", err)
	}

	requeued, err := repo.RequeueStaleRunning(t.Context(), time.Now())
	if err != nil {
		t.Fatalf("requeue stale claims: %v", err)
	}
	if len(requeued) != 0 {
		t.Fatalf("requeued %d legacy tokenless claims, want 0", len(requeued))
	}
	current, err := repo.GetByID(t.Context(), claim.ID)
	if err != nil {
		t.Fatalf("load legacy claim: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != "" {
		t.Fatalf("legacy claim = status:%q token:%q, want tokenless running", current.Status, current.ClaimToken)
	}
}

func TestDatabaseRejectsLegacyClaimAndInPlaceRequeue(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)

	if _, err := pool.Exec(t.Context(), `
		UPDATE scan_runs
		SET status = 'accepted', started_at = NULL, heartbeat_at = NULL,
			completed_at = NULL, error_message = '', updated_at = NOW()
		WHERE id = $1 AND status = 'running'`, claim.ID); err == nil {
		t.Fatal("legacy in-place requeue succeeded, want database trigger rejection")
	}
	current, err := repo.GetByID(t.Context(), claim.ID)
	if err != nil {
		t.Fatalf("load claim after rejected legacy requeue: %v", err)
	}
	if current.Status != StatusRunning || current.ClaimToken != claim.ClaimToken {
		t.Fatalf("claim after rejected legacy requeue = status:%q token:%q", current.Status, current.ClaimToken)
	}
	if _, err := repo.Complete(t.Context(), claim.ID, claim.ClaimToken, &evt.ScanRunResult{}); err != nil {
		t.Fatalf("complete token-bearing claim: %v", err)
	}

	accepted, inserted, err := repo.Create(t.Context(), CreateInput{
		LibraryID: claim.MediaFolderID,
		Mode:      claim.Mode,
		Path:      claim.Path,
		Trigger:   claim.Trigger,
	})
	if err != nil || !inserted {
		t.Fatalf("create accepted run: inserted=%v err=%v", inserted, err)
	}
	if _, err := pool.Exec(t.Context(), `
		UPDATE scan_runs
		SET status = 'running', started_at = NOW(), heartbeat_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND status = 'accepted'`, accepted.ID); err == nil {
		t.Fatal("legacy tokenless claim succeeded, want database trigger rejection")
	}
	current, err = repo.GetByID(t.Context(), accepted.ID)
	if err != nil {
		t.Fatalf("load accepted run after rejected legacy claim: %v", err)
	}
	if current.Status != StatusAccepted || current.ClaimToken != "" {
		t.Fatalf("accepted run after rejected legacy claim = status:%q token:%q", current.Status, current.ClaimToken)
	}
}

func TestCancelByLibrarySerializesWithStaleRequeue(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	if _, err := pool.Exec(t.Context(), `UPDATE scan_runs SET heartbeat_at = '1900-01-01' WHERE id = $1`, claim.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_, err := repo.RequeueStaleRunning(t.Context(), time.Now())
		errs <- err
	})
	wg.Go(func() {
		<-start
		_, err := repo.CancelActiveByLibrary(t.Context(), claim.MediaFolderID)
		errs <- err
	})
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent cancellation/requeue: %v", err)
		}
	}

	active, err := repo.ListActive(t.Context())
	if err != nil {
		t.Fatalf("list active claims: %v", err)
	}
	for _, run := range active {
		if run.MediaFolderID == claim.MediaFolderID {
			t.Fatalf("active claim survived cancellation/requeue race: %#v", run)
		}
	}
}

func TestRequeuePublishesRetiredAndSuccessorLifecycleEvents(t *testing.T) {
	pool := newScanQueueTestPool(t)
	repo := NewRepository(pool)
	claim := createAndClaimScanRun(t, pool, repo)
	if _, err := pool.Exec(t.Context(), `UPDATE scan_runs SET heartbeat_at = '1900-01-01' WHERE id = $1`, claim.ID); err != nil {
		t.Fatalf("expire claim: %v", err)
	}

	hub := evt.NewHub("scan-claim-test", nil)
	events, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	service := NewService(repo, nil, nil, hub, t.Context(), 1, 1)
	service.requeueStale()

	failed := waitForScanEvent(t, events, "scan.failed")
	accepted := waitForScanEvent(t, events, "scan.accepted")
	if failed == accepted {
		t.Fatalf("retired and successor event IDs both %q, want distinct runs", failed)
	}
}

func TestClaimLeaseRejectsLateRenewalAndExpiresSynchronously(t *testing.T) {
	parent, cancel := context.WithCancelCause(t.Context())
	ctx, lease := newClaimLeaseContext(parent, cancel, time.Minute, time.Now())
	deadline := time.Now().Add(-time.Second)
	lease.deadline.Store(deadline.UnixNano())

	if lease.renew(deadline.Add(time.Second)) {
		t.Fatal("late heartbeat renewed an expired lease")
	}
	if err := ctx.Err(); !errors.Is(err, context.Canceled) {
		t.Fatalf("lease context error = %v, want %v", err, context.Canceled)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, ErrScanRunClaimLost) {
		t.Fatalf("lease context cause = %v, want %v", cause, ErrScanRunClaimLost)
	}
}

func newScanQueueTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func createAndClaimScanRun(t *testing.T, pool *pgxpool.Pool, repo *Repository) *models.ScanRun {
	t.Helper()
	ctx := t.Context()
	fixture := fmt.Sprintf("scan-claim-fence-%d", time.Now().UnixNano())

	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('movies', $1, TRUE)
		RETURNING id
	`, fixture).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM scan_runs WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	created, inserted, err := repo.Create(ctx, CreateInput{
		LibraryID: folderID,
		Mode:      ModeFile,
		Path:      "/synthetic/claim-fence.mkv",
		Trigger:   "claim-fencing-test",
	})
	if err != nil || !inserted {
		t.Fatalf("create scan run: inserted=%v err=%v", inserted, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE scan_runs SET requested_at = '1900-01-01' WHERE id = $1`, created.ID); err != nil {
		t.Fatalf("age accepted scan run: %v", err)
	}

	claim, err := repo.ClaimNextAccepted(ctx, 1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("claim first worker: %v", err)
	}
	if claim == nil || claim.ID != created.ID || claim.ClaimToken == "" {
		t.Fatalf("first claim = %#v, want token-bearing run %q", claim, created.ID)
	}
	return claim
}

func expireRequeueAndReclaim(t *testing.T, pool *pgxpool.Pool, repo *Repository, first *models.ScanRun) *models.ScanRun {
	t.Helper()
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `UPDATE scan_runs SET heartbeat_at = '1900-01-01' WHERE id = $1`, first.ID); err != nil {
		t.Fatalf("expire first claim: %v", err)
	}
	requeued, err := repo.RequeueStaleRunning(ctx, time.Now())
	if err != nil || len(requeued) != 1 {
		t.Fatalf("requeue first claim: count=%d err=%v", len(requeued), err)
	}

	successor, err := repo.ClaimNextAccepted(ctx, 1_000_000, 1_000_000)
	if err != nil {
		t.Fatalf("claim successor worker: %v", err)
	}
	if successor == nil || successor.ID == first.ID || successor.ClaimToken == "" || successor.ClaimToken == first.ClaimToken {
		t.Fatalf("successor claim = %#v, want a distinct run with a fresh token", successor)
	}
	retired, err := repo.GetByID(ctx, first.ID)
	if err != nil {
		t.Fatalf("load retired claim: %v", err)
	}
	if retired.Status != StatusFailed || retired.ClaimToken != "" {
		t.Fatalf("retired claim = status:%q token:%q, want failed without token", retired.Status, retired.ClaimToken)
	}
	if requeued[0].Retired.ID != retired.ID || requeued[0].Successor.ID != successor.ID {
		t.Fatalf("requeue result = %#v, want retired %q successor %q", requeued[0], retired.ID, successor.ID)
	}
	if successor.MediaFolderID != first.MediaFolderID || successor.Mode != first.Mode || successor.Path != first.Path || successor.Trigger != first.Trigger || !successor.RequestedAt.Equal(first.RequestedAt) {
		t.Fatalf("successor did not preserve claim scope: first=%#v successor=%#v", first, successor)
	}
	return successor
}

func waitForScanSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForTrackedScanCancel(t *testing.T, service *Service, run *models.ScanRun) context.CancelFunc {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	key := scanClaimKey{runID: run.ID, claimToken: run.ClaimToken}
	for {
		service.runningMu.Lock()
		tracked, ok := service.runningCancels[key]
		service.runningMu.Unlock()
		if ok {
			return tracked.cancel
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for startup claim cancellation registration")
			return nil
		case <-poll.C:
		}
	}
}

func waitForScanEvent(t *testing.T, events <-chan evt.Envelope, eventName string) string {
	t.Helper()
	select {
	case event := <-events:
		if event.Event != eventName {
			t.Fatalf("event = %q, want %q", event.Event, eventName)
		}
		var run evt.ScanRun
		if err := json.Unmarshal(event.Data, &run); err != nil {
			t.Fatalf("decode %s event: %v", eventName, err)
		}
		return run.ID
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", eventName)
		return ""
	}
}

type fixedFolderLoader struct {
	folder *models.MediaFolder
}

func (l fixedFolderLoader) GetByID(context.Context, int) (*models.MediaFolder, error) {
	return l.folder, nil
}

type blockingScanIngester struct {
	started   chan struct{}
	canceled  chan struct{}
	committed chan struct{}
}

func (i *blockingScanIngester) IngestFolder(ctx context.Context, _ *models.MediaFolder) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i *blockingScanIngester) IngestSubtree(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i *blockingScanIngester) IngestFile(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i *blockingScanIngester) ingest(ctx context.Context) (*libraryingest.Result, error) {
	close(i.started)
	if !libraryingest.DeferUntilCommitted(ctx, func(context.Context) { close(i.committed) }) {
		return nil, errors.New("scan queue did not install an after-commit registrar")
	}
	<-ctx.Done()
	close(i.canceled)
	return nil, ctx.Err()
}

type successfulCommitIngester struct {
	committed chan struct{}
}

type recordingScanIngester struct {
	calls atomic.Int32
}

func (i *recordingScanIngester) IngestFolder(context.Context, *models.MediaFolder) (*libraryingest.Result, error) {
	i.calls.Add(1)
	return &libraryingest.Result{}, nil
}

func (i *recordingScanIngester) IngestSubtree(context.Context, *models.MediaFolder, string) (*libraryingest.Result, error) {
	i.calls.Add(1)
	return &libraryingest.Result{}, nil
}

func (i *recordingScanIngester) IngestFile(context.Context, *models.MediaFolder, string) (*libraryingest.Result, error) {
	i.calls.Add(1)
	return &libraryingest.Result{}, nil
}

func (i successfulCommitIngester) IngestFolder(ctx context.Context, _ *models.MediaFolder) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i successfulCommitIngester) IngestSubtree(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i successfulCommitIngester) IngestFile(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i successfulCommitIngester) ingest(ctx context.Context) (*libraryingest.Result, error) {
	if !libraryingest.DeferUntilCommitted(ctx, func(afterCommitCtx context.Context) {
		if afterCommitCtx.Err() != nil {
			return
		}
		close(i.committed)
	}) {
		return nil, errors.New("scan queue did not install an after-commit registrar")
	}
	return &libraryingest.Result{}, nil
}
