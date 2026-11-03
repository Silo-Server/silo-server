package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

type fakeProviderIDIntegrityRepairer struct {
	stats metadata.ProviderIDIntegrityStats
	calls int
	err   error
}

func (f *fakeProviderIDIntegrityRepairer) Run(context.Context, int) (metadata.ProviderIDIntegrityStats, error) {
	f.calls++
	return f.stats, f.err
}

type fakeProviderIDHistoryReconciler struct {
	stats watchstate.ReconcileStats
	calls int
	err   error
}

func (f *fakeProviderIDHistoryReconciler) Run(context.Context) (watchstate.ReconcileStats, error) {
	f.calls++
	return f.stats, f.err
}

type providerIDRepairProgressReporter struct {
	reports []string
	result  json.RawMessage
}

func (p *providerIDRepairProgressReporter) Report(_ float64, message string) {
	p.reports = append(p.reports, message)
}

func (p *providerIDRepairProgressReporter) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

func TestRepairProviderIDIntegrityTask(t *testing.T) {
	repairer := &fakeProviderIDIntegrityRepairer{
		stats: metadata.ProviderIDIntegrityStats{
			Scanned:                      5,
			CleanInserts:                 2,
			ProvisionalConflictsRepaired: 1,
			MatchedCanonicalizations:     1,
			SkippedUnresolved:            1,
			RemainingEstimate:            7,
		},
	}
	reconciler := &fakeProviderIDHistoryReconciler{
		stats: watchstate.ReconcileStats{OrphansFound: 3, Resolved: 2, Unresolvable: 1},
	}
	task := NewRepairProviderIDIntegrityTask(repairer, reconciler)

	if task.Key() != "repair_provider_id_integrity" {
		t.Fatalf("Key() = %q, want repair_provider_id_integrity", task.Key())
	}
	if task.Category() != taskmanager.TaskCategorySystem {
		t.Fatalf("Category() = %q, want %q", task.Category(), taskmanager.TaskCategorySystem)
	}
	triggers := task.DefaultTriggers()
	if len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeStartup {
		t.Fatalf("DefaultTriggers() = %#v, want one startup trigger", triggers)
	}

	progress := &providerIDRepairProgressReporter{}
	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if repairer.calls != 1 {
		t.Fatalf("repairer calls = %d, want 1", repairer.calls)
	}
	if reconciler.calls != 1 {
		t.Fatalf("reconciler calls = %d, want 1", reconciler.calls)
	}
	last := progress.reports[len(progress.reports)-1]
	if !strings.Contains(last, "5 rows") || !strings.Contains(last, "7 still drifted") || !strings.Contains(last, "watch history reconciled 2/3") {
		t.Fatalf("last progress report = %q", last)
	}
	var result repairProviderIDIntegrityResult
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("unmarshal result data: %v", err)
	}
	if result.Scanned != 5 || result.CleanInserts != 2 || result.WatchHistoryResolved != 2 || result.RemainingEstimate != 7 {
		t.Fatalf("result data = %#v, want repair, reconcile, and remaining counts", result)
	}
}
