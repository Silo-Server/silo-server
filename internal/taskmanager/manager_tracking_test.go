package taskmanager_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type trackedExecutionRow struct {
	id           int64
	taskKey      string
	ownerID      string
	startedAt    time.Time
	heartbeatAt  time.Time
	completedAt  time.Time
	status       string
	errorMessage string
	durationMs   int64
}

type fakeTrackingHistoryRepo struct {
	mu           sync.Mutex
	nextID       int64
	rows         map[int64]*trackedExecutionRow
	inserts      []taskmanager.ExecutionResult
	heartbeats   int
	hbSignal     chan struct{}
	startErr     error
	finishMiss   bool
	reconcileCnt int
}

func newFakeTrackingHistoryRepo() *fakeTrackingHistoryRepo {
	return &fakeTrackingHistoryRepo{
		nextID:   1,
		rows:     make(map[int64]*trackedExecutionRow),
		hbSignal: make(chan struct{}, 16),
	}
}

func (r *fakeTrackingHistoryRepo) Insert(ctx context.Context, result taskmanager.ExecutionResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserts = append(r.inserts, result)
	return nil
}

func (r *fakeTrackingHistoryRepo) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	return nil, nil
}

func (r *fakeTrackingHistoryRepo) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

func (r *fakeTrackingHistoryRepo) Start(ctx context.Context, taskKey string, startedAt time.Time, ownerID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return 0, r.startErr
	}
	id := r.nextID
	r.nextID++
	r.rows[id] = &trackedExecutionRow{
		id:          id,
		taskKey:     taskKey,
		ownerID:     ownerID,
		startedAt:   startedAt,
		heartbeatAt: startedAt,
		status:      taskmanager.ExecutionStatusRunning,
	}
	return id, nil
}

func (r *fakeTrackingHistoryRepo) Heartbeat(ctx context.Context, id int64, ownerID string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	row := r.rows[id]
	if row != nil && row.ownerID == ownerID && row.status == taskmanager.ExecutionStatusRunning {
		row.heartbeatAt = at
		r.heartbeats++
		select {
		case r.hbSignal <- struct{}{}:
		default:
		}
	}
	return nil
}

func (r *fakeTrackingHistoryRepo) Finish(ctx context.Context, id int64, ownerID string, result taskmanager.ExecutionResult) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finishMiss {
		return false, nil
	}
	row := r.rows[id]
	if row == nil || row.ownerID != ownerID {
		return false, nil
	}
	row.completedAt = result.CompletedAt
	row.status = result.Status
	row.errorMessage = result.ErrorMessage
	row.durationMs = result.DurationMs
	return true, nil
}

func (r *fakeTrackingHistoryRepo) ReconcileStale(ctx context.Context, staleBefore time.Time, completedAt time.Time, reason string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reconcileCnt++
	var count int64
	for _, row := range r.rows {
		if row.status == taskmanager.ExecutionStatusRunning && row.heartbeatAt.Before(staleBefore) {
			row.status = "failed"
			row.completedAt = completedAt
			row.errorMessage = reason
			row.durationMs = max(0, completedAt.Sub(row.startedAt).Milliseconds())
			count++
		}
	}
	return count, nil
}

type blockingTestTask struct {
	key     string
	started chan struct{}
	release chan struct{}
}

func (t *blockingTestTask) Key() string                                  { return t.key }
func (t *blockingTestTask) Name() string                                 { return t.key }
func (t *blockingTestTask) Description() string                          { return "" }
func (t *blockingTestTask) Category() taskmanager.TaskCategory           { return taskmanager.TaskCategorySystem }
func (t *blockingTestTask) IsHidden() bool                               { return false }
func (t *blockingTestTask) DefaultTriggers() []taskmanager.TriggerConfig { return nil }
func (t *blockingTestTask) Execute(ctx context.Context, _ taskmanager.ProgressReporter) error {
	close(t.started)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.release:
		return nil
	}
}

type manualFireTrigger struct {
	cfg taskmanager.TriggerConfig
	ch  chan struct{}
}

func (t *manualFireTrigger) Start(*taskmanager.ExecutionResult) {}
func (t *manualFireTrigger) Stop()                              {}
func (t *manualFireTrigger) C() <-chan struct{}                 { return t.ch }
func (t *manualFireTrigger) NextRunTime() time.Time             { return time.Time{} }
func (t *manualFireTrigger) Config() taskmanager.TriggerConfig  { return t.cfg }

func TestTaskManagerTracksInFlightHeartbeatsAndFinishesInPlace(t *testing.T) {
	repo := newFakeTrackingHistoryRepo()
	mgr := taskmanager.New(&scheduleStore{}, repo, nil, slog.New(slog.DiscardHandler))
	mgr.SetExecutionLease(10*time.Millisecond, time.Minute, time.Minute)

	task := &blockingTestTask{
		key:     "tracked_task",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr.Register(task)

	if _, err := mgr.StartTask(task.key); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	<-task.started

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for range 2 {
		select {
		case <-repo.hbSignal:
		case <-timer.C:
			t.Fatal("timed out waiting for execution heartbeats")
		}
	}
	repo.mu.Lock()
	row := repo.rows[1]
	inFlightStatus := ""
	if row != nil {
		inFlightStatus = row.status
	}
	repo.mu.Unlock()
	if inFlightStatus != taskmanager.ExecutionStatusRunning {
		t.Fatalf("in-flight status = %q, want %q", inFlightStatus, taskmanager.ExecutionStatusRunning)
	}

	close(task.release)
	mgr.Stop()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	finalRow := repo.rows[1]
	if finalRow == nil || finalRow.status != "completed" {
		t.Fatalf("final tracked row = %#v, want status=completed", finalRow)
	}
	if len(repo.inserts) != 0 {
		t.Fatalf("unexpected fallback inserts = %d, want 0", len(repo.inserts))
	}
}

func TestTaskManagerReconcilesOnlyStaleExecutionsPreservingHealthyPeer(t *testing.T) {
	repo := newFakeTrackingHistoryRepo()
	now := time.Now()
	repo.rows[10] = &trackedExecutionRow{
		id:          10,
		taskKey:     "stale_peer",
		ownerID:     "dead-peer",
		startedAt:   now.Add(-10 * time.Minute),
		heartbeatAt: now.Add(-5 * time.Minute),
		status:      taskmanager.ExecutionStatusRunning,
	}
	repo.rows[11] = &trackedExecutionRow{
		id:          11,
		taskKey:     "healthy_peer",
		ownerID:     "live-peer",
		startedAt:   now.Add(-20 * time.Second),
		heartbeatAt: now.Add(-5 * time.Second),
		status:      taskmanager.ExecutionStatusRunning,
	}

	mgr := taskmanager.New(&scheduleStore{}, repo, nil, slog.New(slog.DiscardHandler))
	mgr.SetExecutionLease(30*time.Second, time.Minute, time.Hour)
	mgr.Start(context.Background())
	mgr.Stop()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	if repo.rows[10].status != "failed" || repo.rows[10].errorMessage == "" {
		t.Fatalf("stale peer row = %#v, want failed with reason", repo.rows[10])
	}
	if repo.rows[11].status != taskmanager.ExecutionStatusRunning {
		t.Fatalf("healthy peer row = %#v, want still running", repo.rows[11])
	}
}

func TestTaskManagerStopDrainsScheduledRunAndPersistsWithDetachedContext(t *testing.T) {
	repo := newFakeTrackingHistoryRepo()
	trig := &manualFireTrigger{
		cfg: taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeStartup},
		ch:  make(chan struct{}, 1),
	}
	store := &scheduleStore{saved: map[string][]taskmanager.TriggerConfig{
		"scheduled_drain": {trig.cfg},
	}}
	mgr := taskmanager.New(store, repo, func(taskmanager.TriggerConfig) taskmanager.Trigger {
		return trig
	}, slog.New(slog.DiscardHandler))
	mgr.SetShutdownDrainTimeout(2 * time.Second)

	task := &blockingTestTask{
		key:     "scheduled_drain",
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	mgr.Register(task)
	mgr.Start(context.Background())

	trig.ch <- struct{}{}
	<-task.started

	// Stop cancels the loop context and the task context, and must still
	// persist the resulting execution row before returning.
	mgr.Stop()

	repo.mu.Lock()
	defer repo.mu.Unlock()
	row := repo.rows[1]
	if row == nil {
		t.Fatal("scheduled run lost its execution row on Stop")
	}
	if row.status != "failed" && row.status != "cancelled" { //nolint:misspell // Existing task state vocabulary.
		t.Fatalf("scheduled run status after Stop = %q, want terminal failure or cancel", row.status)
	}
	if row.completedAt.IsZero() {
		t.Fatal("scheduled run completedAt was not written on Stop")
	}
}

func TestTaskManagerFallsBackToInsertWhenStartOrFinishFails(t *testing.T) {
	t.Run("start error falls back to Insert", func(t *testing.T) {
		repo := newFakeTrackingHistoryRepo()
		repo.startErr = errors.New("transient start failure")
		mgr := taskmanager.New(&scheduleStore{}, repo, nil, slog.New(slog.DiscardHandler))
		mgr.Register(scheduleTask{})
		if err := mgr.RunTask(context.Background(), "schedule_test"); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.inserts) != 1 || repo.inserts[0].Status != "completed" {
			t.Fatalf("fallback inserts = %#v, want 1 completed", repo.inserts)
		}
	})

	t.Run("finish missing row falls back to Insert", func(t *testing.T) {
		repo := newFakeTrackingHistoryRepo()
		repo.finishMiss = true
		mgr := taskmanager.New(&scheduleStore{}, repo, nil, slog.New(slog.DiscardHandler))
		mgr.Register(scheduleTask{})
		if err := mgr.RunTask(context.Background(), "schedule_test"); err != nil {
			t.Fatalf("RunTask: %v", err)
		}
		repo.mu.Lock()
		defer repo.mu.Unlock()
		if len(repo.inserts) != 1 || repo.inserts[0].Status != "completed" {
			t.Fatalf("fallback inserts = %#v, want 1 completed", repo.inserts)
		}
	})
}
