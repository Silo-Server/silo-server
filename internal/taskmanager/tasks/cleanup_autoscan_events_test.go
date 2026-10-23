package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/autoscan"
)

type fakeAutoscanEventPruner struct {
	result autoscan.EventPruneResult
	err    error
	calls  int
	cutoff time.Time
}

func (f *fakeAutoscanEventPruner) PruneEvents(_ context.Context, cutoff time.Time, _, _ int) (autoscan.EventPruneResult, error) {
	f.calls++
	f.cutoff = cutoff
	return f.result, f.err
}

// The retention window decides what is deleted for good, so an unusable
// stored value must never widen the deletion past the default.
func TestAutoscanEventCleanupTaskRetentionWindow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   map[string]string
		wantDays int
	}{
		{"unset", nil, autoscan.DefaultEventsRetentionDays},
		{"configured", map[string]string{autoscan.SettingKeyEventsRetentionDays: " 7 "}, 7},
		{"zero", map[string]string{autoscan.SettingKeyEventsRetentionDays: "0"}, autoscan.DefaultEventsRetentionDays},
		{"negative", map[string]string{autoscan.SettingKeyEventsRetentionDays: "-5"}, autoscan.DefaultEventsRetentionDays},
		{"garbage", map[string]string{autoscan.SettingKeyEventsRetentionDays: "soon"}, autoscan.DefaultEventsRetentionDays},
		{"above maximum", map[string]string{autoscan.SettingKeyEventsRetentionDays: "200000000"}, autoscan.MaxEventsRetentionDays},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pruner := &fakeAutoscanEventPruner{}
			before := time.Now().UTC().AddDate(0, 0, -tc.wantDays)
			task := NewAutoscanEventCleanupTask(pruner, &fakeSettingsStore{values: tc.values})
			if err := task.Execute(context.Background(), &taskHistoryCleanupProgress{}); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			after := time.Now().UTC().AddDate(0, 0, -tc.wantDays)
			if pruner.cutoff.Before(before) || pruner.cutoff.After(after) {
				t.Fatalf("cutoff = %v, want a %d day window", pruner.cutoff, tc.wantDays)
			}
		})
	}
}

// A failed settings read must not fall back to the default: an admin who
// chose a longer window would lose history that cannot be restored.
func TestAutoscanEventCleanupTaskSkipsWhenRetentionUnreadable(t *testing.T) {
	wantErr := errors.New("settings unavailable")
	pruner := &fakeAutoscanEventPruner{}

	err := NewAutoscanEventCleanupTask(pruner, &fakeSettingsStore{getErr: wantErr}).Execute(context.Background(), &taskHistoryCleanupProgress{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute error = %v, want %v", err, wantErr)
	}
	if pruner.calls != 0 {
		t.Fatalf("pruner calls = %d, want none", pruner.calls)
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
	var result autoscan.EventPruneResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if result.Deleted != autoscanEventCleanupBatchSize {
		t.Fatalf("result = %#v, want the rows deleted before the failure", result)
	}
}
