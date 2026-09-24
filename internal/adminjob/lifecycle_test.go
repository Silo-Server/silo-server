package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5/pgxpool"
)

func lifecycleRepo(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewRepository(pool)
}
func lifecycleJob(t *testing.T, r *Repository, kind string) *models.AdminJob {
	t.Helper()
	job, err := r.Create(t.Context(), CreateJobInput{JobType: kind, CreatedByUserID: 1, RequestPayload: LibraryRefreshRequest{LibraryID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	return job
}

func lifecycleUser(t *testing.T, r *Repository, label string) int {
	t.Helper()
	username := fmt.Sprintf("%s-%d", label, time.Now().UnixNano())
	var userID int
	if err := r.pool.QueryRow(t.Context(), `
		INSERT INTO users (username, email, password_hash, role, enabled)
		VALUES ($1, $2, 'test', 'admin', true)
		RETURNING id`, username, username+"@example.invalid").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM users WHERE id=$1", userID) })
	return userID
}

func TestStorageTransitionAdmissionIsUniqueAcrossConcurrentCreates(t *testing.T) {
	r := lifecycleRepo(t)
	createdByUserID := lifecycleUser(t, r, "storage-transition-admission")
	type createResult struct {
		job *models.AdminJob
		err error
	}
	start := make(chan struct{})
	results := make(chan createResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			job, err := r.Create(t.Context(), CreateJobInput{
				JobType:         JobTypeStorageTransition,
				CreatedByUserID: createdByUserID,
				RequestPayload:  StorageTransitionRequest{TransitionID: "concurrent-admission", Policy: "migrate_all"},
			})
			results <- createResult{job: job, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	created, conflicts := 0, 0
	for result := range results {
		if result.job != nil {
			created++
			t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", result.job.ID) })
		}
		switch {
		case result.err == nil:
		case errors.Is(result.err, ErrActiveJobConflict):
			conflicts++
		default:
			t.Fatalf("Create error = %v", result.err)
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("created=%d conflicts=%d, want 1 each", created, conflicts)
	}
}
func TestJobClaimRecoveryAndTerminalRace(t *testing.T) {
	r := lifecycleRepo(t)
	for _, kind := range []string{JobTypeLibraryRefresh, JobTypeCatalogExport, JobTypeDeleteLibrary} {
		t.Run(kind, func(t *testing.T) {
			queued := lifecycleJob(t, r, kind)
			old, err := r.ClaimNextQueued(t.Context(), kind)
			if err != nil || old == nil {
				t.Fatalf("claim %v", err)
			}
			stale := r.withClaim(old)
			if _, err = r.RequeueStaleRunning(t.Context(), time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			fresh, err := r.ClaimNextQueued(t.Context(), kind)
			if err != nil || fresh == nil {
				t.Fatalf("reclaim %v", err)
			}
			if err = stale.Complete(t.Context(), queued.ID, CompleteJobInput{}); !errors.Is(err, ErrJobNotFound) {
				t.Fatalf("stale completion %v", err)
			}
			if err = stale.UpdateProgress(t.Context(), queued.ID, 9, 10, "stale"); !errors.Is(err, ErrJobNotFound) {
				t.Fatalf("stale progress %v", err)
			}
			owner := r.withClaim(fresh)
			start := make(chan struct{})
			results := make(chan error, 2)
			var wg sync.WaitGroup
			wg.Go(func() {
				<-start
				results <- owner.Complete(t.Context(), queued.ID, CompleteJobInput{ResultPayload: map[string]int{"done": 1}})
			})
			wg.Go(func() { <-start; results <- owner.Fail(t.Context(), queued.ID, FailJobInput{ErrorMessage: "failure"}) })
			close(start)
			wg.Wait()
			close(results)
			winners := 0
			for err := range results {
				if err == nil {
					winners++
				} else if !errors.Is(err, ErrJobNotFound) {
					t.Fatal(err)
				}
			}
			if winners != 1 {
				t.Fatalf("terminal winners %d", winners)
			}
			terminal, err := r.GetByID(t.Context(), queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			if terminal.ExpiresAt == nil || terminal.CompletedAt == nil || terminal.ExpiresAt.Sub(*terminal.CompletedAt) < 24*time.Hour {
				t.Fatal("retention below 24 hours")
			}
			_ = owner.TouchHeartbeat(t.Context(), queued.ID)
			_ = owner.UpdateProgress(t.Context(), queued.ID, 99, 100, "late")
			_, _ = owner.Cancel(t.Context(), queued.ID, "late", time.Now())
			after, _ := r.GetByID(t.Context(), queued.ID)
			if !reflect.DeepEqual(terminal, after) {
				t.Fatal("terminal representation changed")
			}
		})
	}
}

type waitingRefresh struct{ started chan struct{} }

func (e waitingRefresh) Execute(ctx context.Context, _ LibraryRefreshRequest, _ func(int, int, string)) (*LibraryRefreshResult, error) {
	close(e.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type waitingStorageTransition struct{ started chan struct{} }

func (e waitingStorageTransition) ExecuteStorageTransition(ctx context.Context, _ StorageTransitionRequest, _ func(StorageTransitionProgress)) (any, error) {
	close(e.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type queuedCancellationStorageTransition struct {
	canceled chan StorageTransitionRequest
	err      error
}

type uncertainStorageTransition struct{}

type uncertainStorageTransitionResult struct {
	ManualRestartRequired bool   `json:"manual_restart_required"`
	ClaimGeneration       int64  `json:"claim_generation"`
	Phase                 string `json:"phase"`
}

func (uncertainStorageTransitionResult) StorageTransitionCommitUnknown() bool { return true }
func (r uncertainStorageTransitionResult) WithStorageTransitionRestartReceipt(manual bool, generation int64) any {
	r.ManualRestartRequired = manual
	r.ClaimGeneration = generation
	return r
}

func (uncertainStorageTransition) ExecuteStorageTransition(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
	return uncertainStorageTransitionResult{Phase: "restart_pending"}, nil
}

func (e queuedCancellationStorageTransition) ExecuteStorageTransition(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
	return nil, errors.New("queued transition must not execute")
}

func (e queuedCancellationStorageTransition) CancelStorageTransition(_ context.Context, req StorageTransitionRequest) error {
	e.canceled <- req
	return e.err
}

func TestQueuedStorageTransitionCancellationReleasesStage(t *testing.T) {
	r := lifecycleRepo(t)
	createdByUserID := lifecycleUser(t, r, "queued-storage-cancellation")
	req := StorageTransitionRequest{TransitionID: "queued-stage", Policy: "migrate_all"}
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: createdByUserID, RequestPayload: req})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	if _, err := r.RequestCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	executor := queuedCancellationStorageTransition{canceled: make(chan StorageTransitionRequest, 1)}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(executor)
	worker.runNext()
	select {
	case got := <-executor.canceled:
		if got.TransitionID != req.TransitionID {
			t.Fatalf("canceled transition = %#v", got)
		}
	default:
		t.Fatal("queued cancellation was not forwarded to storage transition state")
	}
	terminal, err := r.GetByID(t.Context(), job.ID)
	if err != nil || terminal.Status != StatusCancelled {
		t.Fatalf("queued cancellation %v %+v", err, terminal)
	}
}

func TestQueuedStorageTransitionCancellationWaitsForStageRelease(t *testing.T) {
	r := lifecycleRepo(t)
	createdByUserID := lifecycleUser(t, r, "queued-storage-release-failure")
	req := StorageTransitionRequest{TransitionID: "queued-stage-release-failure", Policy: "migrate_all"}
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: createdByUserID, RequestPayload: req})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	if _, err := r.RequestCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	executor := queuedCancellationStorageTransition{canceled: make(chan StorageTransitionRequest, 1), err: errors.New("settings unavailable")}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(executor)
	worker.runNext()

	pending, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status == StatusCancelled || !pending.CancelRequested {
		t.Fatalf("queued cancellation after release failure = status %q cancel_requested=%v", pending.Status, pending.CancelRequested)
	}
}

func TestQueuedCancellationAfterStorageCommitWaitsForRestart(t *testing.T) {
	r := lifecycleRepo(t)
	userID := lifecycleUser(t, r, "committed-queued-cancellation")
	job, err := r.Create(t.Context(), CreateJobInput{
		JobType: JobTypeStorageTransition, CreatedByUserID: userID,
		RequestPayload: StorageTransitionRequest{TransitionID: "committed-queued", Policy: "migrate_all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	if _, err := r.RequestCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.heartbeatInterval = 10 * time.Millisecond
	t.Cleanup(worker.Stop)
	worker.SetStorageTransitionExecutor(queuedCancellationStorageTransition{canceled: make(chan StorageTransitionRequest, 1), err: ErrStorageTransitionAlreadyCommitted})
	worker.runNext()
	current, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result StorageTransitionReceipt
	if err := json.Unmarshal(current.ResultPayload, &result); err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusRunning || result.Phase != "restart_pending" || result.ClaimGeneration != current.ClaimGeneration || !result.RestartRequired || !result.ManualRestartRequired || !strings.Contains(current.Message, "restart Silo manually") {
		t.Fatalf("committed cancellation receipt status=%q result=%s message=%q", current.Status, current.ResultPayload, current.Message)
	}
}

func TestStorageTransitionRestartRequestReportsUnavailableHost(t *testing.T) {
	runner := &Runner{}
	if err := runner.requestStorageTransitionRestart("job"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing restart callback error = %v", err)
	}
	runner.storageTransitionCommitted = func(context.Context) error { return errors.New("restart refused") }
	if err := runner.requestStorageTransitionRestart("job"); err == nil || !strings.Contains(err.Error(), "restart refused") {
		t.Fatalf("refused restart error = %v", err)
	}
}

func TestUndeterminedStorageCommitLeavesJobRunningForRestartRecovery(t *testing.T) {
	r := lifecycleRepo(t)
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: 1, RequestPayload: StorageTransitionRequest{TransitionID: "uncertain", Policy: "migrate_all"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.heartbeatInterval = 10 * time.Millisecond
	t.Cleanup(worker.Stop)
	worker.SetStorageTransitionExecutor(uncertainStorageTransition{})
	worker.runNext()
	current, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != StatusRunning || !strings.Contains(current.Message, "restart Silo manually") {
		t.Fatalf("uncertain transition receipt status=%q message=%q", current.Status, current.Message)
	}
	var result uncertainStorageTransitionResult
	if err := json.Unmarshal(current.ResultPayload, &result); err != nil || result.Phase != "restart_pending" || result.ClaimGeneration != current.ClaimGeneration || !result.ManualRestartRequired {
		t.Fatalf("uncertain transition result=%s err=%v", current.ResultPayload, err)
	}
	// The source fence remains held until this process exits. A fresh heartbeat
	// prevents its own runner from reclaiming the uncertain job meanwhile.
	if _, err := r.pool.Exec(t.Context(), `UPDATE admin_jobs SET heartbeat_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err = r.GetByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.HeartbeatAt != nil && current.HeartbeatAt.After(time.Now().Add(-time.Minute)) {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("uncertain transition stopped heartbeating before restart")
		}
	}
	if requeued, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(-time.Minute)); err != nil || requeued != 0 {
		t.Fatalf("live uncertain transition requeued=%d err=%v", requeued, err)
	}
	worker.runNext()
	current, err = r.GetByID(t.Context(), job.ID)
	if err != nil || current.Status != StatusRunning {
		t.Fatalf("uncertain transition after another poll = %+v, err=%v", current, err)
	}
	worker.Stop()
	if _, err := r.pool.Exec(t.Context(), `UPDATE admin_jobs SET heartbeat_at=NOW()-INTERVAL '1 hour' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if requeued, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(-time.Minute)); err != nil || requeued != 1 {
		t.Fatalf("uncertain transition after restart requeued=%d err=%v", requeued, err)
	}
}

func TestCommittedStorageTransitionRecordsManualRestartFlag(t *testing.T) {
	r := lifecycleRepo(t)
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: 1, RequestPayload: StorageTransitionRequest{TransitionID: "committed", Policy: "migrate_all"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
		return restartAwareStorageTransitionResult{}, nil
	}))
	worker.runNext()
	current, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result restartAwareStorageTransitionResult
	if current.Status != StatusCompleted || json.Unmarshal(current.ResultPayload, &result) != nil || result.ClaimGeneration != current.ClaimGeneration || !result.ManualRestartRequired {
		t.Fatalf("committed transition status=%q result=%s", current.Status, current.ResultPayload)
	}
}

func TestFailedStorageTransitionKeepsSafeProgressReceipt(t *testing.T) {
	r := lifecycleRepo(t)
	userID := lifecycleUser(t, r, "storage-transition-progress")
	job, err := r.Create(t.Context(), CreateJobInput{
		JobType: JobTypeStorageTransition, CreatedByUserID: userID,
		RequestPayload: StorageTransitionRequest{TransitionID: "progress-failure", Policy: "migrate_all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(_ context.Context, _ StorageTransitionRequest, report func(StorageTransitionProgress)) (any, error) {
		report(StorageTransitionProgress{Current: 5, Phase: "copying", Message: "verified private/object"})
		return nil, errors.New("private provider endpoint failed")
	}))
	worker.runNext()
	failed, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var receipt StorageTransitionReceipt
	if err := json.Unmarshal(failed.ResultPayload, &receipt); err != nil {
		t.Fatal(err)
	}
	if failed.Status != StatusFailed || receipt.Phase != "failed" || receipt.VerifiedObjects != 5 || receipt.ClaimGeneration != failed.ClaimGeneration || receipt.FailureCategory != "copy_failed" {
		t.Fatalf("failed transition receipt: status=%q result=%s", failed.Status, failed.ResultPayload)
	}
	if failed.ErrorMessage != "private provider endpoint failed" {
		t.Fatalf("internal error was lost: %q", failed.ErrorMessage)
	}
}

func TestCommittedStorageTransitionIgnoresLateCancellation(t *testing.T) {
	r := lifecycleRepo(t)
	userID := lifecycleUser(t, r, "committed-late-cancel")
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: userID, RequestPayload: StorageTransitionRequest{TransitionID: "committed-late-cancel", Policy: "migrate_all"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
		return restartAwareStorageTransitionResult{CopiedObjects: 7}, nil
	}))
	worker.SetStorageTransitionCommitted(func(ctx context.Context) error {
		if _, err := r.RequestCancellation(ctx, job.ID); err != nil {
			return err
		}
		return errors.New("automatic restart unavailable")
	})
	worker.runNext()
	finished, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	var result restartAwareStorageTransitionResult
	if err := json.Unmarshal(finished.ResultPayload, &result); err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusCompleted || finished.CancelRequested || !result.ManualRestartRequired || result.ClaimGeneration != finished.ClaimGeneration || result.CopiedObjects != 7 {
		t.Fatalf("committed transition status=%q cancel_requested=%t result=%s", finished.Status, finished.CancelRequested, finished.ResultPayload)
	}
}

func TestStaleStorageTransitionResumesAfterUnconfirmedCommit(t *testing.T) {
	r := lifecycleRepo(t)
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: 1, RequestPayload: StorageTransitionRequest{TransitionID: "copying-stage", Policy: "migrate_all"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	claimed, err := r.ClaimNextQueued(t.Context(), JobTypeStorageTransition)
	if err != nil || claimed == nil {
		t.Fatalf("claim stale transition: %v", err)
	}
	receipt := StorageTransitionReceipt{Phase: "committing", VerifiedObjects: 5, ClaimGeneration: claimed.ClaimGeneration}
	if err := r.withClaim(claimed).UpdateProgressResult(t.Context(), job.ID, 5, 10, "Committing storage transition", receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	requeued, err := r.GetByID(t.Context(), job.ID)
	if err != nil || requeued.Status != StatusQueued || !strings.Contains(requeued.Message, "unconfirmed commit") {
		t.Fatalf("requeued transition=%+v err=%v", requeued, err)
	}
	var retained StorageTransitionReceipt
	if err := json.Unmarshal(requeued.ResultPayload, &retained); err != nil || retained != receipt || requeued.ProgressCurrent != 5 {
		t.Fatalf("requeue lost recovery receipt: result=%s progress=%d err=%v", requeued.ResultPayload, requeued.ProgressCurrent, err)
	}
	runs := 0
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
		runs++
		return restartAwareStorageTransitionResult{}, nil
	}))
	worker.SetStorageTransitionCommitted(func(context.Context) error { return nil })
	worker.runNext()
	finished, err := r.GetByID(t.Context(), job.ID)
	if err != nil || finished.Status != StatusCompleted || runs != 1 {
		t.Fatalf("resumed transition status=%v runs=%d err=%v", finished, runs, err)
	}
}

type storageTransitionExecutorFunc func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error)

func (f storageTransitionExecutorFunc) ExecuteStorageTransition(ctx context.Context, request StorageTransitionRequest, progress func(StorageTransitionProgress)) (any, error) {
	return f(ctx, request, progress)
}

type restartAwareStorageTransitionResult struct {
	ManualRestartRequired bool  `json:"manual_restart_required"`
	CopiedObjects         int   `json:"copied_objects,omitempty"`
	ClaimGeneration       int64 `json:"claim_generation"`
}

func (r restartAwareStorageTransitionResult) WithStorageTransitionRestartReceipt(manual bool, generation int64) any {
	r.ManualRestartRequired = manual
	r.ClaimGeneration = generation
	return r
}

func TestStorageTransitionDurableCancellation(t *testing.T) {
	r := lifecycleRepo(t)
	job := lifecycleJob(t, r, JobTypeStorageTransition)
	executor := waitingStorageTransition{started: make(chan struct{})}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	worker.SetStorageTransitionExecutor(executor)
	worker.heartbeatInterval = time.Millisecond
	done := make(chan struct{})
	go func() { worker.runNext(); close(done) }()
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("storage transition did not start")
	}
	if _, err := NewRepository(r.pool).RequestCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("storage transition cancellation was not observed")
	}
	terminal, err := r.GetByID(t.Context(), job.ID)
	if err != nil || terminal.Status != StatusCancelled {
		t.Fatalf("storage transition cancellation %v %+v", err, terminal)
	}
}
func TestJobDurableCancellationAcrossWorkersAndRestart(t *testing.T) {
	r := lifecycleRepo(t)
	queued := lifecycleJob(t, r, JobTypeLibraryRefresh)
	apiNode := NewRepository(r.pool)
	pending, err := apiNode.RequestCancellation(t.Context(), queued.ID)
	if err != nil || !pending.CancelRequested {
		t.Fatalf("request %v", err)
	}
	repeat, err := apiNode.RequestCancellation(t.Context(), queued.ID)
	if err != nil || !repeat.UpdatedAt.Equal(pending.UpdatedAt) {
		t.Fatalf("coalescing %v", err)
	}
	restarted := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	restarted.runNext()
	canceled, err := apiNode.RequestCancellation(t.Context(), queued.ID)
	if err != nil || canceled.Status != StatusCancelled {
		t.Fatalf("restart cancellation %v %+v", err, canceled)
	}
	running := lifecycleJob(t, r, JobTypeLibraryRefresh)
	executor := waitingRefresh{started: make(chan struct{})}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, executor, nil, nil, nil, nil)
	worker.heartbeatInterval = time.Millisecond
	done := make(chan struct{})
	go func() { worker.runNext(); close(done) }()
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	if _, err := apiNode.RequestCancellation(t.Context(), running.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("remote cancellation not observed")
	}
	terminal, err := r.GetByID(t.Context(), running.ID)
	if err != nil || terminal.Status != StatusCancelled {
		t.Fatalf("running cancellation %v %+v", err, terminal)
	}
}
func TestLibraryDeletionAcceptanceAtomic(t *testing.T) {
	r := lifecycleRepo(t)
	var folderID int
	if err := r.pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name,enabled) VALUES ('movies','Atomic deletion test',true) RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM media_folders WHERE id=$1", folderID) })
	blocker := lifecycleJob(t, r, JobTypeDeleteLibrary)
	if _, err := r.pool.Exec(t.Context(), `UPDATE admin_jobs SET request_payload=jsonb_build_object('library_id',$2::int) WHERE id=$1`, blocker.ID, folderID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateLibraryDeletion(t.Context(), 1, DeleteLibraryRequest{LibraryID: folderID}); !errors.Is(err, ErrActiveJobConflict) {
		t.Fatalf("expected conflict %v", err)
	}
	var enabled bool
	if err := r.pool.QueryRow(t.Context(), "SELECT enabled FROM media_folders WHERE id=$1", folderID).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("failed acceptance disabled folder: %v", err)
	}
	if _, err := r.CancelQueued(t.Context(), blocker.ID, "test", time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	job, err := r.CreateLibraryDeletion(t.Context(), 1, DeleteLibraryRequest{LibraryID: folderID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	if err := r.pool.QueryRow(t.Context(), "SELECT enabled FROM media_folders WHERE id=$1", folderID).Scan(&enabled); err != nil || enabled {
		t.Fatalf("accepted deletion not prepared: %v", err)
	}
	if _, err := r.GetByID(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestJobCancellationCompletionRace(t *testing.T) {
	r := lifecycleRepo(t)
	for range 8 {
		job := lifecycleJob(t, r, JobTypeLibraryRefresh)
		claimed, err := r.ClaimNextQueued(t.Context(), JobTypeLibraryRefresh)
		if err != nil || claimed == nil {
			t.Fatalf("claim %v", err)
		}
		owner := r.withClaim(claimed)
		start := make(chan struct{})
		result := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			_, err := NewRepository(r.pool).RequestCancellation(t.Context(), job.ID)
			result <- err
		})
		wg.Go(func() {
			<-start
			if err := owner.Complete(t.Context(), job.ID, CompleteJobInput{ResultPayload: LibraryRefreshResult{LibraryID: 1}}); err != nil {
				t.Error(err)
			}
		})
		close(start)
		wg.Wait()
		cancelErr := <-result
		terminal, err := r.GetByID(t.Context(), job.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch terminal.Status {
		case StatusCompleted:
			if !errors.Is(cancelErr, ErrJobNotCancellable) {
				t.Fatalf("completed before cancellation but cancellation accepted: %v", cancelErr)
			}
		case StatusCancelled:
			if cancelErr != nil {
				t.Fatalf("cancellation winner %v", cancelErr)
			}
		default:
			t.Fatalf("nonterminal race outcome %s", terminal.Status)
		}
		if err := owner.Fail(t.Context(), job.ID, FailJobInput{}); !errors.Is(err, ErrJobNotFound) {
			t.Fatalf("late failure %v", err)
		}
	}
}

func TestLibraryDeletionCompetingAcceptanceAndInsertRollback(t *testing.T) {
	r := lifecycleRepo(t)
	createFolder := func() int {
		var id int
		if err := r.pool.QueryRow(t.Context(), `INSERT INTO media_folders(type,name,enabled) VALUES ('movies','Acceptance race',true) RETURNING id`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE job_type=$1 AND request_payload->>'library_id'=$2", JobTypeDeleteLibrary, fmt.Sprint(id))
			_, _ = r.pool.Exec(context.Background(), "DELETE FROM media_folders WHERE id=$1", id)
		})
		return id
	}
	first, second := createFolder(), createFolder()
	// PostgreSQL cannot encode this user ID as its integer column. The failure
	// happens after the target update, so it exercises transaction rollback.
	if _, err := r.CreateLibraryDeletion(t.Context(), math.MaxInt, DeleteLibraryRequest{LibraryID: first}); err == nil {
		t.Fatal("invalid insert accepted")
	}
	var enabled bool
	if err := r.pool.QueryRow(t.Context(), "SELECT enabled FROM media_folders WHERE id=$1", first).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("insertion failure disabled library: %v", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			<-start
			_, err := r.CreateLibraryDeletion(t.Context(), 1, DeleteLibraryRequest{LibraryID: first})
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrActiveJobConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("acceptance winners=%d conflicts=%d", success, conflict)
	}
	if _, err := r.CreateLibraryDeletion(t.Context(), 1, DeleteLibraryRequest{LibraryID: second}); err != nil {
		t.Fatalf("independent library blocked: %v", err)
	}
}

func TestJobOrdinaryCompletionAfterClaim(t *testing.T) {
	r := lifecycleRepo(t)
	for _, kind := range []string{JobTypeCatalogExport, JobTypeCatalogImport, JobTypeDeleteLibrary, JobTypeLibraryRefresh} {
		t.Run(kind, func(t *testing.T) {
			job := lifecycleJob(t, r, kind)
			claimed, err := r.ClaimNextQueued(t.Context(), kind)
			if err != nil || claimed == nil || claimed.ID != job.ID {
				t.Fatalf("claim %v %+v", err, claimed)
			}
			if err := r.withClaim(claimed).Complete(t.Context(), job.ID, CompleteJobInput{ResultPayload: struct {
				Done bool `json:"done"`
			}{Done: true}}); err != nil {
				t.Fatal(err)
			}
			terminal, err := r.GetByID(t.Context(), job.ID)
			if err != nil || terminal.Status != StatusCompleted {
				t.Fatalf("completion %v %+v", err, terminal)
			}
			if string(terminal.ResultPayload) != `{"done": true}` {
				t.Fatalf("result %s", terminal.ResultPayload)
			}
		})
	}
}

// Stale recovery can requeue a transition this process committed or could not
// confirm, for example after a database outage outlasted the stale window. The
// process still holds its source fences, so the claim waits for the restart:
// it neither runs the transition again nor ends up failed or canceled.
func TestRequeuedUncertainCommitIsHeldForRestart(t *testing.T) {
	r := lifecycleRepo(t)
	job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: 1, RequestPayload: StorageTransitionRequest{TransitionID: "held-after-requeue", Policy: "migrate_all"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(worker.Stop)
	runs := 0
	worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
		runs++
		return uncertainStorageTransitionResult{Phase: "restart_pending"}, nil
	}))
	worker.runNext()
	if _, err := NewRepository(r.pool).RequestCancellation(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	worker.runNext()
	held, err := r.GetByID(t.Context(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runs != 1 || held.Status != StatusRunning || held.CancelRequested {
		t.Fatalf("requeued uncertain commit: runs=%d status=%q cancel_requested=%t", runs, held.Status, held.CancelRequested)
	}
	var receipt StorageTransitionReceipt
	if err := json.Unmarshal(held.ResultPayload, &receipt); err != nil || receipt.Phase != "restart_pending" || !receipt.ManualRestartRequired || receipt.ClaimGeneration != held.ClaimGeneration {
		t.Fatalf("held receipt = %s (%v)", held.ResultPayload, err)
	}
}

// A claim that finds its transition committed cannot run it again. It keeps
// the restart receipt, without a pending cancellation, until boot recovery
// completes the job after the restart.
func TestCommittedTransitionClaimWaitsForRestart(t *testing.T) {
	committed := fmt.Errorf("%w; awaiting restart or recovery", ErrStorageTransitionAlreadyCommitted)
	for name, setup := range map[string]func(*testing.T, *Repository, *Runner, string){
		"execute": func(t *testing.T, _ *Repository, worker *Runner, _ string) {
			worker.SetStorageTransitionExecutor(storageTransitionExecutorFunc(func(context.Context, StorageTransitionRequest, func(StorageTransitionProgress)) (any, error) {
				return nil, committed
			}))
		},
		"late cancellation": func(t *testing.T, r *Repository, worker *Runner, id string) {
			if _, err := r.RequestCancellation(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			worker.SetStorageTransitionExecutor(queuedCancellationStorageTransition{canceled: make(chan StorageTransitionRequest, 1), err: ErrStorageTransitionAlreadyCommitted})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := lifecycleRepo(t)
			job, err := r.Create(t.Context(), CreateJobInput{JobType: JobTypeStorageTransition, CreatedByUserID: 1, RequestPayload: StorageTransitionRequest{TransitionID: "committed-before-claim", Policy: "migrate_all"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = r.pool.Exec(context.Background(), "DELETE FROM admin_jobs WHERE id=$1", job.ID) })
			worker := NewRunner(NewRepository(r.pool), nil, nil, nil, nil, nil, nil, nil, nil)
			t.Cleanup(worker.Stop)
			restarts := 0
			worker.SetStorageTransitionCommitted(func(context.Context) error { restarts++; return nil })
			setup(t, r, worker, job.ID)
			worker.runNext()
			held, err := r.GetByID(t.Context(), job.ID)
			if err != nil {
				t.Fatal(err)
			}
			var receipt StorageTransitionReceipt
			if err := json.Unmarshal(held.ResultPayload, &receipt); err != nil {
				t.Fatal(err)
			}
			if held.Status != StatusRunning || held.CancelRequested || receipt.Phase != "restart_pending" || receipt.ManualRestartRequired || restarts != 1 {
				t.Fatalf("committed claim status=%q cancel_requested=%t receipt=%s restarts=%d", held.Status, held.CancelRequested, held.ResultPayload, restarts)
			}
		})
	}
}

type recordingRefresh struct{ reqs chan LibraryRefreshRequest }

func (e recordingRefresh) Execute(_ context.Context, req LibraryRefreshRequest, _ func(int, int, string)) (*LibraryRefreshResult, error) {
	e.reqs <- req
	return &LibraryRefreshResult{LibraryID: req.LibraryID}, nil
}

func TestOnlyARecoveredLibraryRefreshWaitsForTheLibraryLock(t *testing.T) {
	r := lifecycleRepo(t)
	executor := recordingRefresh{reqs: make(chan LibraryRefreshRequest, 1)}
	worker := NewRunner(NewRepository(r.pool), nil, nil, nil, executor, nil, nil, nil, nil)
	executed := func() LibraryRefreshRequest {
		t.Helper()
		worker.runNext()
		select {
		case req := <-executor.reqs:
			return req
		default:
			t.Fatal("runner did not execute the library refresh")
			return LibraryRefreshRequest{}
		}
	}

	lifecycleJob(t, r, JobTypeLibraryRefresh)
	if executed().waitForLibraryLock {
		t.Fatal("a first claim waits for the library lock; want it to fail fast on a held lock")
	}

	recovered := lifecycleJob(t, r, JobTypeLibraryRefresh)
	abandoned, err := r.ClaimNextQueued(t.Context(), JobTypeLibraryRefresh)
	if err != nil || abandoned == nil || abandoned.ID != recovered.ID {
		t.Fatalf("claim job before recovery: job=%+v err=%v", abandoned, err)
	}
	if _, err := r.RequeueStaleRunning(t.Context(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !executed().waitForLibraryLock {
		t.Fatal("a recovered claim fails fast on the library lock its earlier attempt may still hold")
	}
}
