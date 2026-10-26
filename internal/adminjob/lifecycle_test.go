package adminjob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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

type queuedCancellationStorageTransition struct {
	canceled chan StorageTransitionRequest
	err      error
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
