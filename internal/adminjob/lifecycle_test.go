package adminjob

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
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
