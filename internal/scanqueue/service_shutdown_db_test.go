package scanqueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/models"
)

func openDirectRunTestRepository(t *testing.T) (context.Context, *pgxpool.Pool, *Repository, int) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	name := fmt.Sprintf("scanqueue-direct-run-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled)
		VALUES ('series', $1, true)
		RETURNING id`,
		name,
	).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	return ctx, pool, NewRepository(pool), folderID
}

type overlapTestFolders struct{ folder *models.MediaFolder }

func (f overlapTestFolders) GetByID(context.Context, int) (*models.MediaFolder, error) {
	return f.folder, nil
}

// shutdownTestIngester blocks every ingest until its context ends, as a long
// scan does when the process shuts down. With block unset it returns err, or
// completes when err is nil.
type shutdownTestIngester struct {
	block   bool
	started chan struct{}
	err     error
}

func (i *shutdownTestIngester) ingest(ctx context.Context) (*libraryingest.Result, error) {
	if !i.block {
		if i.err != nil {
			return nil, i.err
		}
		return &libraryingest.Result{}, nil
	}
	close(i.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (i *shutdownTestIngester) IngestFolder(ctx context.Context, _ *models.MediaFolder) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i *shutdownTestIngester) IngestSubtree(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

func (i *shutdownTestIngester) IngestFile(ctx context.Context, _ *models.MediaFolder, _ string) (*libraryingest.Result, error) {
	return i.ingest(ctx)
}

// shutdownTestFolders blocks the library lookup until its context ends.
type shutdownTestFolders struct{ started chan struct{} }

func (f shutdownTestFolders) GetByID(ctx context.Context, _ int) (*models.MediaFolder, error) {
	close(f.started)
	<-ctx.Done()
	return nil, fmt.Errorf("load media folder: %w", ctx.Err())
}

// startShutdownTestRun starts a library scan run and records a second request
// for the same library on it as an owed follow-up.
func startShutdownTestRun(t *testing.T, ctx context.Context, repo *Repository, folderID int) *models.ScanRun {
	t.Helper()
	run, created, err := repo.Create(ctx, CreateInput{LibraryID: folderID, Mode: ModeLibrary, Trigger: "scheduled"})
	if err != nil || !created {
		t.Fatalf("create library run: created=%v err=%v", created, err)
	}
	run, err = repo.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("start library run: %v", err)
	}
	coalesced, created, err := repo.Create(ctx, CreateInput{LibraryID: folderID, Mode: ModeLibrary, Trigger: "autoscan:sonarr"})
	if err != nil || created || coalesced.ID != run.ID || coalesced.FollowupTrigger != "autoscan:sonarr" {
		t.Fatalf("coalesce request into running run: created=%v err=%v run=%#v", created, err, coalesced)
	}
	return coalesced
}

func processUntilStarted(t *testing.T, svc *Service, run *models.ScanRun, started <-chan struct{}) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); svc.process(run) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("scan never started")
	}
	return done
}

func waitProcessed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not stop")
	}
}

// TestShutdownRequeuesRunningScanWithFollowUpDB pins that a scan cut short by
// shutdown goes back to the queue with its owed follow-up, so the next
// process scans it and then serves the request coalesced into it. Before, the
// worker ended the run and both were lost for good.
func TestShutdownRequeuesRunningScanWithFollowUpDB(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	folder := &models.MediaFolder{ID: folderID, Type: "series", Enabled: true}
	appCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	ingester := &shutdownTestIngester{block: true, started: make(chan struct{})}
	svc := NewService(repo, overlapTestFolders{folder: folder}, ingester, nil, appCtx, 1, 1)

	run := startShutdownTestRun(t, ctx, repo, folderID)
	done := processUntilStarted(t, svc, run, ingester.started)
	if _, err := repo.UpdateProgress(ctx, run.ID, run.StartedAt, &evt.ScanRunResult{Message: "Processing files", FilesProcessed: 5, TotalFiles: 10}); err != nil {
		t.Fatalf("record progress: %v", err)
	}
	shutdown()
	waitProcessed(t, done)

	got, err := repo.GetByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got.Status != StatusAccepted || got.StartedAt != nil || got.HeartbeatAt != nil || got.CompletedAt != nil {
		t.Fatalf("run after shutdown = status %s started %v heartbeat %v completed %v, want accepted and unclaimed",
			got.Status, got.StartedAt, got.HeartbeatAt, got.CompletedAt)
	}
	if string(got.ResultPayload) != "{}" {
		t.Fatalf("progress after shutdown = %s, want the interrupted attempt's progress cleared", got.ResultPayload)
	}
	if got.FollowupTrigger != "autoscan:sonarr" {
		t.Fatalf("follow-up trigger after shutdown = %q, want autoscan:sonarr", got.FollowupTrigger)
	}

	// The next process claims the same run, finishes it, and enqueues the
	// follow-up it still owes.
	restarted := NewService(repo, overlapTestFolders{folder: folder}, &shutdownTestIngester{}, nil, context.Background(), 1, 1)
	claimed, err := repo.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("claim requeued run: %v", err)
	}
	restarted.process(claimed)
	if got, err := repo.GetByID(ctx, run.ID); err != nil || got.Status != StatusCompleted {
		t.Fatalf("requeued run after restart = %#v, %v; want completed", got, err)
	}
	followUp, err := repo.GetActiveByScope(ctx, folderID, ModeLibrary, "")
	if err != nil {
		t.Fatalf("load follow-up run: %v", err)
	}
	if followUp.ID == run.ID || followUp.Status != StatusAccepted || followUp.Trigger != "autoscan:sonarr" {
		t.Fatalf("follow-up run = %#v, want a new accepted autoscan:sonarr run", followUp)
	}
}

// TestShutdownDuringLibraryLoadRequeuesScanDB pins that shutdown landing
// before the scan itself starts requeues the run rather than failing it.
func TestShutdownDuringLibraryLoadRequeuesScanDB(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	appCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	folders := shutdownTestFolders{started: make(chan struct{})}
	svc := NewService(repo, folders, &shutdownTestIngester{}, nil, appCtx, 1, 1)

	run := startShutdownTestRun(t, ctx, repo, folderID)
	done := processUntilStarted(t, svc, run, folders.started)
	shutdown()
	waitProcessed(t, done)

	got, err := repo.GetByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got.Status != StatusAccepted || got.ErrorMessage != "" || got.FollowupTrigger != "autoscan:sonarr" {
		t.Fatalf("run after shutdown = status %s error %q follow-up %q, want accepted with its follow-up",
			got.Status, got.ErrorMessage, got.FollowupTrigger)
	}
}

// TestCancelByLibraryStillCancelsRunningScanDB pins that an admin cancel
// still ends a running scan with the cancel status and no follow-up.
func TestCancelByLibraryStillCancelsRunningScanDB(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	folder := &models.MediaFolder{ID: folderID, Type: "series", Enabled: true}
	ingester := &shutdownTestIngester{block: true, started: make(chan struct{})}
	svc := NewService(repo, overlapTestFolders{folder: folder}, ingester, nil, context.Background(), 1, 1)

	run := startShutdownTestRun(t, ctx, repo, folderID)
	done := processUntilStarted(t, svc, run, ingester.started)
	if _, err := svc.CancelByLibrary(ctx, folderID); err != nil {
		t.Fatalf("cancel library scans: %v", err)
	}
	waitProcessed(t, done)

	got, err := repo.GetByID(ctx, run.ID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got.Status != StatusCancelled {
		t.Fatalf("run after cancel = status %s, want %s", got.Status, StatusCancelled)
	}
	if active, err := repo.GetActiveByScope(ctx, folderID, ModeLibrary, ""); !errors.Is(err, ErrScanRunNotFound) {
		t.Fatalf("active run after cancel = %#v, %v; want none", active, err)
	}
}

// TestRequeueLeavesACancelledRunCancelledDB pins that an admin cancel landing
// before the shutdown requeue wins.
func TestRequeueLeavesACancelledRunCancelledDB(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	run := startShutdownTestRun(t, ctx, repo, folderID)
	if _, changed, err := repo.MarkCancelled(ctx, run.ID); err != nil || !changed {
		t.Fatalf("cancel run: changed=%v err=%v", changed, err)
	}

	got, changed, err := repo.Requeue(ctx, run.ID, run.StartedAt)
	if err != nil || changed || got.Status != StatusCancelled {
		t.Fatalf("requeue after cancel = %#v changed=%v err=%v, want the run left %s", got, changed, err, StatusCancelled)
	}
}

// TestRequeueLeavesALaterClaimRunningDB pins that a worker giving up a claim
// cannot requeue the run after another worker has claimed it again.
func TestRequeueLeavesALaterClaimRunningDB(t *testing.T) {
	ctx, _, repo, folderID := openDirectRunTestRepository(t)
	first := startShutdownTestRun(t, ctx, repo, folderID)
	if _, changed, err := repo.Requeue(ctx, first.ID, first.StartedAt); err != nil || !changed {
		t.Fatalf("requeue first claim: changed=%v err=%v", changed, err)
	}
	second, err := repo.Start(ctx, first.ID)
	if err != nil {
		t.Fatalf("claim run again: %v", err)
	}
	if second.StartedAt == nil || second.StartedAt.Equal(*first.StartedAt) {
		t.Fatalf("second claim started at %v, want a start time other than the first claim's %v", second.StartedAt, first.StartedAt)
	}

	got, changed, err := repo.Requeue(ctx, first.ID, first.StartedAt)
	if err != nil || changed || got.Status != StatusRunning || !got.StartedAt.Equal(*second.StartedAt) {
		t.Fatalf("stale requeue = %#v changed=%v err=%v, want the second claim left running", got, changed, err)
	}
}

// claimAgain requeues run as the stale-run requeue would and claims it again,
// as a worker on another replica would, and returns the new claim.
func claimAgain(t *testing.T, ctx context.Context, repo *Repository, run *models.ScanRun) *models.ScanRun {
	t.Helper()
	if _, changed, err := repo.Requeue(ctx, run.ID, run.StartedAt); err != nil || !changed {
		t.Fatalf("requeue run: changed=%v err=%v", changed, err)
	}
	again, err := repo.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("claim run again: %v", err)
	}
	if again.StartedAt == nil || again.StartedAt.Equal(*run.StartedAt) {
		t.Fatalf("second claim started at %v, want a start time other than the first claim's %v", again.StartedAt, run.StartedAt)
	}
	return again
}

// TestRunningScanStopsWhenItsClaimEndsElsewhereDB pins that a worker stops its
// scan at the next heartbeat once its run is canceled on another replica, or
// requeued as stale and claimed again, and leaves the run to that cancel or
// claim. Before, only the replica that took the cancel stopped its own scans,
// and a worker kept heartbeating a later claim, so the scan ran to the end.
func TestRunningScanStopsWhenItsClaimEndsElsewhereDB(t *testing.T) {
	for _, tc := range []struct {
		name string
		// end ends the worker's claim from outside it and returns the status
		// and claim the run must keep.
		end func(t *testing.T, ctx context.Context, repo *Repository, run *models.ScanRun) (string, *time.Time)
	}{
		{
			name: "canceled on another replica",
			end: func(t *testing.T, ctx context.Context, repo *Repository, run *models.ScanRun) (string, *time.Time) {
				other := NewService(repo, nil, nil, nil, context.Background(), 1, 1)
				if _, err := other.CancelByLibrary(ctx, run.MediaFolderID); err != nil {
					t.Fatalf("cancel library scans on another replica: %v", err)
				}
				return StatusCancelled, run.StartedAt
			},
		},
		{
			name: "requeued and claimed again",
			end: func(t *testing.T, ctx context.Context, repo *Repository, run *models.ScanRun) (string, *time.Time) {
				return StatusRunning, claimAgain(t, ctx, repo, run).StartedAt
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, repo, folderID := openDirectRunTestRepository(t)
			folder := &models.MediaFolder{ID: folderID, Type: "series", Enabled: true}
			appCtx, stop := context.WithCancel(context.Background())
			t.Cleanup(stop)
			ingester := &shutdownTestIngester{block: true, started: make(chan struct{})}
			svc := NewService(repo, overlapTestFolders{folder: folder}, ingester, nil, appCtx, 1, 1)
			svc.heartbeatInterval = 20 * time.Millisecond

			run := startShutdownTestRun(t, ctx, repo, folderID)
			done := processUntilStarted(t, svc, run, ingester.started)
			wantStatus, wantClaim := tc.end(t, ctx, repo, run)
			waitProcessed(t, done)

			got, err := repo.GetByID(ctx, run.ID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if got.Status != wantStatus || got.StartedAt == nil || !got.StartedAt.Equal(*wantClaim) || got.FollowupTrigger != "autoscan:sonarr" {
				t.Fatalf("run after the worker stopped = status %s started %v follow-up %q, want %s started %v with its follow-up",
					got.Status, got.StartedAt, got.FollowupTrigger, wantStatus, wantClaim)
			}
		})
	}
}

// TestStaleWorkerLeavesALaterClaimRunningDB pins that a worker whose run was
// requeued as stale and claimed again cannot end the later claim, whichever
// way its own scan ends, even before its heartbeat notices. Before, the stale
// worker completed, failed or canceled the run under the later claim.
func TestStaleWorkerLeavesALaterClaimRunningDB(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "scan completes"},
		{name: "scan fails", err: errors.New("read library: input/output error")},
		{name: "scan is canceled", err: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _, repo, folderID := openDirectRunTestRepository(t)
			folder := &models.MediaFolder{ID: folderID, Type: "series", Enabled: true}
			svc := NewService(repo, overlapTestFolders{folder: folder}, &shutdownTestIngester{err: tc.err}, nil, context.Background(), 1, 1)

			stale := startShutdownTestRun(t, ctx, repo, folderID)
			later := claimAgain(t, ctx, repo, stale)
			svc.process(stale)

			got, err := repo.GetByID(ctx, stale.ID)
			if err != nil {
				t.Fatalf("load run: %v", err)
			}
			if got.Status != StatusRunning || got.StartedAt == nil || !got.StartedAt.Equal(*later.StartedAt) || got.FollowupTrigger != "autoscan:sonarr" {
				t.Fatalf("run after the stale worker finished = status %s started %v follow-up %q, want the later claim (started %v) left running with its follow-up",
					got.Status, got.StartedAt, got.FollowupTrigger, later.StartedAt)
			}
		})
	}
}
