package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeTaskHistoryPruner struct {
	result     taskmanager.HistoryPruneResult
	err        error
	calls      int
	kept       int
	cutoff     time.Time
	batchSize  int
	maxBatches int
}

func (f *fakeTaskHistoryPruner) Prune(
	_ context.Context,
	keepPerTask int,
	cutoff time.Time,
	batchSize int,
	maxBatches int,
) (taskmanager.HistoryPruneResult, error) {
	f.calls++
	f.kept = keepPerTask
	f.cutoff = cutoff
	f.batchSize = batchSize
	f.maxBatches = maxBatches
	return f.result, f.err
}

type taskHistoryCleanupProgress struct {
	reports []string
	result  json.RawMessage
}

func (p *taskHistoryCleanupProgress) Report(_ float64, message string) {
	p.reports = append(p.reports, message)
}

func (p *taskHistoryCleanupProgress) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

func (p *taskHistoryCleanupProgress) decode(t *testing.T) taskmanager.HistoryPruneResult {
	t.Helper()
	var result taskmanager.HistoryPruneResult
	if err := json.Unmarshal(p.result, &result); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	return result
}

func TestTaskHistoryCleanupTask(t *testing.T) {
	pruner := &fakeTaskHistoryPruner{
		result: taskmanager.HistoryPruneResult{Deleted: 10017},
	}
	task := NewTaskHistoryCleanupTask(pruner, &fakeSettingsStore{})

	if task.Key() != "cleanup_task_history" {
		t.Fatalf("Key() = %q, want cleanup_task_history", task.Key())
	}
	if task.Category() != taskmanager.TaskCategorySystem {
		t.Fatalf("Category() = %q, want %q", task.Category(), taskmanager.TaskCategorySystem)
	}
	if task.IsHidden() {
		t.Fatal("IsHidden() = true, want false")
	}
	wantTriggers := []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
	gotTriggers := task.DefaultTriggers()
	if len(gotTriggers) != len(wantTriggers) || gotTriggers[0] != wantTriggers[0] || gotTriggers[1] != wantTriggers[1] {
		t.Fatalf("DefaultTriggers() = %#v, want %#v", gotTriggers, wantTriggers)
	}

	before := time.Now().UTC().AddDate(0, 0, -taskmanager.DefaultHistoryRetentionDays)
	progress := &taskHistoryCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -taskmanager.DefaultHistoryRetentionDays)
	if pruner.calls != 1 {
		t.Fatalf("pruner calls = %d, want 1", pruner.calls)
	}
	if pruner.kept != taskmanager.DefaultHistoryKeepPerTask {
		t.Fatalf("keepPerTask = %d, want %d", pruner.kept, taskmanager.DefaultHistoryKeepPerTask)
	}
	if pruner.batchSize != taskHistoryCleanupBatchSize {
		t.Fatalf("batchSize = %d, want %d", pruner.batchSize, taskHistoryCleanupBatchSize)
	}
	if pruner.maxBatches != taskHistoryCleanupMaxBatches {
		t.Fatalf("maxBatches = %d, want %d", pruner.maxBatches, taskHistoryCleanupMaxBatches)
	}
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want between %v and %v", pruner.cutoff, before, after)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Pruned 10017 task executions" {
		t.Fatalf("last progress report = %q", got)
	}
	if result := progress.decode(t); result.Deleted != 10017 || result.LimitReached || result.Skipped {
		t.Fatalf("result = %#v, want 10017 deleted without limit", result)
	}
}

func TestTaskHistoryCleanupTaskUsesConfiguredRetention(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		taskmanager.SettingKeyHistoryRetentionDays: "7",
		taskmanager.SettingKeyHistoryKeepPerTask:   "25",
	}}
	pruner := &fakeTaskHistoryPruner{}

	before := time.Now().UTC().AddDate(0, 0, -7)
	if err := NewTaskHistoryCleanupTask(pruner, store).Execute(context.Background(), &taskHistoryCleanupProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -7)
	if pruner.kept != 25 {
		t.Fatalf("keepPerTask = %d, want 25", pruner.kept)
	}
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want a 7 day window between %v and %v", pruner.cutoff, before, after)
	}
}

func TestTaskHistoryCleanupTaskRejectsUnusableRetention(t *testing.T) {
	for _, tc := range []struct {
		name          string
		retentionDays string
		keepPerTask   string
	}{
		{"zero", "0", "0"},
		{"negative", "-5", "-1"},
		{"garbage", "soon", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSettingsStore{values: map[string]string{
				taskmanager.SettingKeyHistoryRetentionDays: tc.retentionDays,
				taskmanager.SettingKeyHistoryKeepPerTask:   tc.keepPerTask,
			}}
			pruner := &fakeTaskHistoryPruner{}
			if err := NewTaskHistoryCleanupTask(pruner, store).Execute(context.Background(), &taskHistoryCleanupProgress{}); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if pruner.kept != taskmanager.DefaultHistoryKeepPerTask {
				t.Fatalf("keepPerTask = %d, want the default %d", pruner.kept, taskmanager.DefaultHistoryKeepPerTask)
			}
			floor := time.Now().UTC().AddDate(0, 0, -taskmanager.DefaultHistoryRetentionDays-1)
			if pruner.cutoff.Before(floor) || pruner.cutoff.After(time.Now().UTC()) {
				t.Fatalf("cutoff = %v, want the default retention window", pruner.cutoff)
			}
		})
	}
}

func TestSeedHistoryRetentionDefaults(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		taskmanager.SettingKeyHistoryKeepPerTask: "5",
	}}
	if err := taskmanager.SeedHistoryRetentionDefaults(context.Background(), store); err != nil {
		t.Fatalf("SeedHistoryRetentionDefaults: %v", err)
	}
	if got := store.values[taskmanager.SettingKeyHistoryKeepPerTask]; got != "5" {
		t.Fatalf("keep per task = %q, want the existing 5", got)
	}
	want := strconv.Itoa(taskmanager.DefaultHistoryRetentionDays)
	if got := store.values[taskmanager.SettingKeyHistoryRetentionDays]; got != want {
		t.Fatalf("retention days = %q, want %q", got, want)
	}
}

func TestTaskHistoryCleanupTaskReturnsPruneError(t *testing.T) {
	wantErr := errors.New("delete failed")
	pruner := &fakeTaskHistoryPruner{
		result: taskmanager.HistoryPruneResult{Deleted: taskHistoryCleanupBatchSize},
		err:    wantErr,
	}
	progress := &taskHistoryCleanupProgress{}

	err := NewTaskHistoryCleanupTask(pruner, &fakeSettingsStore{}).Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
	if got := progress.reports[len(progress.reports)-1]; !strings.Contains(got, "failed after deleting 10000 executions") {
		t.Fatalf("last progress report = %q", got)
	}
}

func TestTaskHistoryCleanupTaskCapsWorkPerRun(t *testing.T) {
	wantDeleted := int64(taskHistoryCleanupBatchSize * taskHistoryCleanupMaxBatches)
	pruner := &fakeTaskHistoryPruner{
		result: taskmanager.HistoryPruneResult{
			Deleted:      wantDeleted,
			LimitReached: true,
		},
	}
	progress := &taskHistoryCleanupProgress{}

	if err := NewTaskHistoryCleanupTask(pruner, &fakeSettingsStore{}).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if pruner.calls != 1 {
		t.Fatalf("pruner calls = %d, want 1", pruner.calls)
	}
	result := progress.decode(t)
	if result.Deleted != wantDeleted || !result.LimitReached {
		t.Fatalf("result = %#v, want %d deleted with limit reached", result, wantDeleted)
	}
}

func TestTaskHistoryCleanupTaskReportsSkippedLock(t *testing.T) {
	pruner := &fakeTaskHistoryPruner{
		result: taskmanager.HistoryPruneResult{Skipped: true},
	}
	progress := &taskHistoryCleanupProgress{}

	if err := NewTaskHistoryCleanupTask(pruner, &fakeSettingsStore{}).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Task history cleanup is already running on another node" {
		t.Fatalf("last progress report = %q", got)
	}
	if result := progress.decode(t); !result.Skipped || result.Deleted != 0 {
		t.Fatalf("result = %#v, want skipped", result)
	}
}
