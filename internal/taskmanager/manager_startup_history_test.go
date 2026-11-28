package taskmanager_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// pausedHistory answers GetLatest with an older run, but only after the test
// releases the read, so a run can complete while startup is reading history.
type pausedHistory struct {
	older   taskmanager.ExecutionResult
	reading chan struct{}
	release chan struct{}
	once    sync.Once

	mu      sync.Mutex
	inserts []taskmanager.ExecutionResult
}

func (h *pausedHistory) Insert(_ context.Context, result taskmanager.ExecutionResult) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.inserts = append(h.inserts, result)
	return nil
}

func (h *pausedHistory) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	h.once.Do(func() { close(h.reading) })
	<-h.release
	older := h.older
	return &older, nil
}

func (h *pausedHistory) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

// A run that completes while startup reads the task's history is newer than
// what the read returns. Startup must keep it as the task's last run and
// schedule the next run an interval after it, not after the older one (#4586).
func TestStartupHistoryReadKeepsARunThatCompletedMeanwhile(t *testing.T) {
	const taskKey = "refresh_metadata"
	triggerRepo := &fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{
		taskKey: {{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)}},
	}}
	olderCompleted := time.Now().Add(-10 * time.Minute)
	history := &pausedHistory{
		older: taskmanager.ExecutionResult{
			TaskKey: taskKey, Status: "completed",
			StartedAt: olderCompleted.Add(-time.Minute), CompletedAt: olderCompleted,
		},
		reading: make(chan struct{}),
		release: make(chan struct{}),
	}
	manager := taskmanager.New(triggerRepo, history, newFakeTrigger, slog.New(slog.DiscardHandler))
	manager.Register(stubTask{key: taskKey})

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()
	started := make(chan struct{})
	go func() {
		defer close(started)
		manager.Start(ctx)
	}()

	<-history.reading
	if err := manager.RunTask(ctx, taskKey); err != nil {
		t.Fatalf("RunTask() error = %v", err)
	}
	newer := manager.GetTaskInfo(taskKey).LastExecution
	if newer == nil || !newer.CompletedAt.After(olderCompleted) {
		t.Fatalf("run during startup recorded %+v, want a completion after %s", newer, olderCompleted)
	}
	close(history.release)
	<-started

	info := manager.GetTaskInfo(taskKey)
	if info.LastExecution == nil || !info.LastExecution.CompletedAt.Equal(newer.CompletedAt) {
		t.Fatalf("last execution = %+v, want the run that completed during startup (%s)", info.LastExecution, newer.CompletedAt)
	}
	if info.NextRunAt == nil || info.NextRunAt.Sub(newer.CompletedAt) < 59*time.Minute {
		t.Fatalf("next run = %v, want an hour after the newer completion %s", info.NextRunAt, newer.CompletedAt)
	}
}

type olderHistory struct {
	older taskmanager.ExecutionResult
}

func (h *olderHistory) Insert(context.Context, taskmanager.ExecutionResult) error { return nil }
func (h *olderHistory) GetLatest(context.Context, string) (*taskmanager.ExecutionResult, error) {
	older := h.older
	return &older, nil
}
func (h *olderHistory) List(context.Context, string, int) ([]taskmanager.ExecutionResult, error) {
	return nil, nil
}

type pausedTriggerRepository struct {
	fakeTriggerRepository
	reading chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *pausedTriggerRepository) GetTriggers(ctx context.Context, key string) ([]taskmanager.TriggerConfig, error) {
	r.once.Do(func() { close(r.reading) })
	<-r.release
	return r.fakeTriggerRepository.GetTriggers(ctx, key)
}

type blockingTask struct {
	stubTask
	running chan struct{}
	finish  chan struct{}
}

func (t blockingTask) Execute(context.Context, taskmanager.ProgressReporter) error {
	close(t.running)
	<-t.finish
	return nil
}

// A run that is still going when startup reads history, and completes while
// startup reads the saved schedule, arms no triggers itself. Startup must arm
// them from it, not from the older run history returned.
func TestStartupKeepsARunThatCompletesWhileItsScheduleIsRead(t *testing.T) {
	const taskKey = "refresh_metadata"
	repo := &pausedTriggerRepository{
		fakeTriggerRepository: fakeTriggerRepository{triggers: map[string][]taskmanager.TriggerConfig{
			taskKey: {{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)}},
		}},
		reading: make(chan struct{}),
		release: make(chan struct{}),
	}
	olderCompleted := time.Now().Add(-10 * time.Minute)
	history := &olderHistory{older: taskmanager.ExecutionResult{
		TaskKey: taskKey, Status: "completed",
		StartedAt: olderCompleted.Add(-time.Minute), CompletedAt: olderCompleted,
	}}
	task := blockingTask{stubTask: stubTask{key: taskKey}, running: make(chan struct{}), finish: make(chan struct{})}
	manager := taskmanager.New(repo, history, newFakeTrigger, slog.New(slog.DiscardHandler))
	manager.Register(task)

	ctx, cancel := context.WithCancel(context.Background())
	defer manager.Stop()
	defer cancel()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_ = manager.RunTask(ctx, taskKey)
	}()
	<-task.running

	started := make(chan struct{})
	go func() {
		defer close(started)
		manager.Start(ctx)
	}()
	<-repo.reading // history already read; run still going
	close(task.finish)
	<-runDone
	newer := manager.GetTaskInfo(taskKey).LastExecution
	close(repo.release)
	<-started

	info := manager.GetTaskInfo(taskKey)
	if info.LastExecution == nil || !info.LastExecution.CompletedAt.Equal(newer.CompletedAt) {
		t.Errorf("last execution = %v, want %v", info.LastExecution, newer.CompletedAt)
	}
	if info.NextRunAt == nil || info.NextRunAt.Sub(newer.CompletedAt) < 59*time.Minute {
		t.Errorf("next run = %v, want an hour after %s (older completion %s)", info.NextRunAt, newer.CompletedAt, olderCompleted)
	}
}
