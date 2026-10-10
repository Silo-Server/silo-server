package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanEventPruner struct {
	result     autoscan.EventPruneResult
	err        error
	calls      int
	cutoff     time.Time
	batchSize  int
	maxBatches int
}

func (f *fakeAutoscanEventPruner) PruneEvents(_ context.Context, cutoff time.Time, batchSize, maxBatches int) (autoscan.EventPruneResult, error) {
	f.calls++
	f.cutoff = cutoff
	f.batchSize = batchSize
	f.maxBatches = maxBatches
	return f.result, f.err
}

func decodeAutoscanEventPruneResult(t *testing.T, progress *taskHistoryCleanupProgress) autoscan.EventPruneResult {
	t.Helper()
	var result autoscan.EventPruneResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	return result
}

func TestAutoscanEventCleanupTaskUsesDefaultRetention(t *testing.T) {
	pruner := &fakeAutoscanEventPruner{result: autoscan.EventPruneResult{Deleted: 3891}}
	task := NewAutoscanEventCleanupTask(pruner, &fakeSettingsStore{})
	if task.Key() != "cleanup_autoscan_events" {
		t.Fatalf("Key() = %q, want cleanup_autoscan_events", task.Key())
	}

	before := time.Now().UTC().AddDate(0, 0, -autoscan.DefaultEventsRetentionDays)
	progress := &taskHistoryCleanupProgress{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -autoscan.DefaultEventsRetentionDays)
	if pruner.calls != 1 {
		t.Fatalf("pruner calls = %d, want 1", pruner.calls)
	}
	if pruner.batchSize != autoscanEventCleanupBatchSize || pruner.maxBatches != autoscanEventCleanupMaxBatches {
		t.Fatalf("batch geometry = %d x %d, want %d x %d", pruner.batchSize, pruner.maxBatches, autoscanEventCleanupBatchSize, autoscanEventCleanupMaxBatches)
	}
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want between %v and %v", pruner.cutoff, before, after)
	}
	if got := progress.reports[len(progress.reports)-1]; got != "Pruned 3891 autoscan events" {
		t.Fatalf("last progress report = %q", got)
	}
	if result := decodeAutoscanEventPruneResult(t, progress); result.Deleted != 3891 || result.LimitReached {
		t.Fatalf("result = %#v, want 3891 deleted without limit", result)
	}
}

func TestAutoscanEventCleanupTaskUsesConfiguredRetention(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{autoscan.SettingKeyEventsRetentionDays: " 7 "}}
	pruner := &fakeAutoscanEventPruner{result: autoscan.EventPruneResult{Deleted: 200000, LimitReached: true}}
	progress := &taskHistoryCleanupProgress{}

	before := time.Now().UTC().AddDate(0, 0, -7)
	if err := NewAutoscanEventCleanupTask(pruner, store).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -7)
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want a 7 day window between %v and %v", pruner.cutoff, before, after)
	}
	if got := progress.reports[len(progress.reports)-1]; !strings.Contains(got, "remaining history will be pruned on the next run") {
		t.Fatalf("last progress report = %q", got)
	}
	if result := decodeAutoscanEventPruneResult(t, progress); result.Deleted != 200000 || !result.LimitReached {
		t.Fatalf("result = %#v, want 200000 deleted with limit reached", result)
	}
}

func TestAutoscanEventCleanupTaskRejectsUnusableRetention(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeSettingsStore
	}{
		{"zero", &fakeSettingsStore{values: map[string]string{autoscan.SettingKeyEventsRetentionDays: "0"}}},
		{"negative", &fakeSettingsStore{values: map[string]string{autoscan.SettingKeyEventsRetentionDays: "-5"}}},
		{"garbage", &fakeSettingsStore{values: map[string]string{autoscan.SettingKeyEventsRetentionDays: "soon"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pruner := &fakeAutoscanEventPruner{}
			if err := NewAutoscanEventCleanupTask(pruner, tc.store).Execute(context.Background(), &taskHistoryCleanupProgress{}); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			floor := time.Now().UTC().AddDate(0, 0, -autoscan.DefaultEventsRetentionDays-1)
			ceiling := time.Now().UTC().AddDate(0, 0, -autoscan.DefaultEventsRetentionDays+1)
			if pruner.cutoff.Before(floor) || pruner.cutoff.After(ceiling) {
				t.Fatalf("cutoff = %v, want the default retention window", pruner.cutoff)
			}
		})
	}
}

func TestAutoscanEventCleanupTaskCapsRetentionAboveMaximum(t *testing.T) {
	store := &fakeSettingsStore{values: map[string]string{autoscan.SettingKeyEventsRetentionDays: "200000000"}}
	pruner := &fakeAutoscanEventPruner{}
	before := time.Now().UTC().AddDate(0, 0, -autoscan.MaxEventsRetentionDays)
	if err := NewAutoscanEventCleanupTask(pruner, store).Execute(context.Background(), &taskHistoryCleanupProgress{}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	after := time.Now().UTC().AddDate(0, 0, -autoscan.MaxEventsRetentionDays)
	if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
		t.Fatalf("cutoff = %v, want the %d day maximum", pruner.cutoff, autoscan.MaxEventsRetentionDays)
	}
}

// A failed settings read must not fall back to the default: an admin who
// chose a longer window would lose history that cannot be restored.
func TestAutoscanEventCleanupTaskSkipsWhenRetentionUnreadable(t *testing.T) {
	wantErr := errors.New("settings unavailable")
	pruner := &fakeAutoscanEventPruner{}
	progress := &taskHistoryCleanupProgress{}

	err := NewAutoscanEventCleanupTask(pruner, &fakeSettingsStore{getErr: wantErr}).Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
	if pruner.calls != 0 {
		t.Fatalf("pruner calls = %d, want none", pruner.calls)
	}
	if got := progress.reports[len(progress.reports)-1]; !strings.Contains(got, "skipped") {
		t.Fatalf("last progress report = %q", got)
	}
}

func TestAutoscanEventCleanupTaskReturnsPruneError(t *testing.T) {
	wantErr := errors.New("delete failed")
	pruner := &fakeAutoscanEventPruner{
		result: autoscan.EventPruneResult{Deleted: autoscanEventCleanupBatchSize},
		err:    wantErr,
	}
	progress := &taskHistoryCleanupProgress{}

	err := NewAutoscanEventCleanupTask(pruner, &fakeSettingsStore{}).Execute(context.Background(), progress)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
	if got := progress.reports[len(progress.reports)-1]; !strings.Contains(got, "failed after deleting 1000 events") {
		t.Fatalf("last progress report = %q", got)
	}
	if result := decodeAutoscanEventPruneResult(t, progress); result.Deleted != autoscanEventCleanupBatchSize {
		t.Fatalf("result = %#v, want the rows deleted before the failure", result)
	}
}
