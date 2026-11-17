package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeTaskHistoryPruner struct {
	result taskmanager.HistoryPruneResult
	err    error
	kept   int
	cutoff time.Time
}

func (f *fakeTaskHistoryPruner) Prune(
	_ context.Context,
	keepPerTask int,
	cutoff time.Time,
	_ int,
	_ int,
) (taskmanager.HistoryPruneResult, error) {
	f.kept = keepPerTask
	f.cutoff = cutoff
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

func TestTaskHistoryCleanupTaskUsesConfiguredRetention(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{
		taskmanager.SettingKeyHistoryRetentionDays: "7",
		taskmanager.SettingKeyHistoryKeepPerTask:   "25",
	}}
	pruner := &fakeTaskHistoryPruner{
		result: taskmanager.HistoryPruneResult{Deleted: 25000, LimitReached: true},
	}
	progress := &taskHistoryCleanupProgress{}

	before := time.Now().UTC().AddDate(0, 0, -7)
	if err := NewTaskHistoryCleanupTask(pruner, store).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -7)
	if pruner.kept != 25 {
		t.Fatalf("keepPerTask = %d, want 25", pruner.kept)
	}
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want a 7 day window between %v and %v", pruner.cutoff, before, after)
	}
	if result := progress.decode(t); result.Deleted != 25000 || !result.LimitReached {
		t.Fatalf("result = %#v, want 25000 deleted with limit reached", result)
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
