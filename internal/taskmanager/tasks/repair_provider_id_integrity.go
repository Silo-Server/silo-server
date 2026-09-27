package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

const repairProviderIDIntegrityBatchSize = 250

type ProviderIDIntegrityRepairer interface {
	Run(ctx context.Context, batchSize int) (metadata.ProviderIDIntegrityStats, error)
}

type WatchHistoryReconciler interface {
	Run(ctx context.Context) (watchstate.ReconcileStats, error)
}

type RepairProviderIDIntegrityTask struct {
	repairer   ProviderIDIntegrityRepairer
	reconciler WatchHistoryReconciler
}

type repairProviderIDIntegrityResult struct {
	Scanned                      int  `json:"scanned"`
	CleanInserts                 int  `json:"clean_inserts"`
	ProvisionalConflictsRepaired int  `json:"provisional_conflicts_repaired"`
	MatchedCanonicalizations     int  `json:"matched_canonicalizations"`
	SkippedUnresolved            int  `json:"skipped_unresolved"`
	Errors                       int  `json:"errors"`
	RemainingEstimate            int  `json:"remaining_estimate"`
	WatchHistoryReconciled       bool `json:"watch_history_reconciled"`
	WatchHistoryOrphansFound     int  `json:"watch_history_orphans_found"`
	WatchHistoryResolved         int  `json:"watch_history_resolved"`
	WatchHistoryUnresolvable     int  `json:"watch_history_unresolvable"`
	WatchHistoryErrors           int  `json:"watch_history_errors"`
}

func NewRepairProviderIDIntegrityTask(repairer ProviderIDIntegrityRepairer, reconciler WatchHistoryReconciler) *RepairProviderIDIntegrityTask {
	return &RepairProviderIDIntegrityTask{repairer: repairer, reconciler: reconciler}
}

func (t *RepairProviderIDIntegrityTask) Key() string  { return "repair_provider_id_integrity" }
func (t *RepairProviderIDIntegrityTask) Name() string { return "Repair Provider ID Integrity" }
func (t *RepairProviderIDIntegrityTask) Description() string {
	return "Repairs drift between media_items provider ID columns and normalized provider ID rows."
}
func (t *RepairProviderIDIntegrityTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *RepairProviderIDIntegrityTask) IsHidden() bool { return true }

func (t *RepairProviderIDIntegrityTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
}

func (t *RepairProviderIDIntegrityTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.repairer == nil {
		progress.Report(100, "Provider ID repair is not configured")
		return nil
	}
	progress.Report(0, "Scanning provider ID drift")
	stats, err := t.repairer.Run(ctx, repairProviderIDIntegrityBatchSize)
	if err != nil {
		progress.Report(100, fmt.Sprintf("Provider ID repair failed after scanning %d rows", stats.Scanned))
		return err
	}
	result := repairProviderIDIntegrityResult{
		Scanned:                      stats.Scanned,
		CleanInserts:                 stats.CleanInserts,
		ProvisionalConflictsRepaired: stats.ProvisionalConflictsRepaired,
		MatchedCanonicalizations:     stats.MatchedCanonicalizations,
		SkippedUnresolved:            stats.SkippedUnresolved,
		Errors:                       stats.Errors,
		RemainingEstimate:            stats.RemainingEstimate,
	}
	msg := fmt.Sprintf(
		"Provider ID repair scanned %d rows: %d clean inserts, %d provisional repairs, %d canonicalizations, %d unresolved, %d errors, %d still drifted",
		stats.Scanned,
		stats.CleanInserts,
		stats.ProvisionalConflictsRepaired,
		stats.MatchedCanonicalizations,
		stats.SkippedUnresolved,
		stats.Errors,
		stats.RemainingEstimate,
	)
	if t.reconciler != nil && (stats.CleanInserts+stats.ProvisionalConflictsRepaired+stats.MatchedCanonicalizations) > 0 {
		progress.Report(75, msg+"; reconciling watch history")
		reconcileStats, reconcileErr := t.reconciler.Run(ctx)
		result.WatchHistoryReconciled = true
		result.WatchHistoryOrphansFound = reconcileStats.OrphansFound
		result.WatchHistoryResolved = reconcileStats.Resolved
		result.WatchHistoryUnresolvable = reconcileStats.Unresolvable
		result.WatchHistoryErrors = reconcileStats.Errors
		msg += fmt.Sprintf(
			"; watch history reconciled %d/%d orphans (%d unresolved, %d errors)",
			reconcileStats.Resolved,
			reconcileStats.OrphansFound,
			reconcileStats.Unresolvable,
			reconcileStats.Errors,
		)
		if reconcileErr != nil {
			progress.Report(100, msg)
			setRepairProviderIDIntegrityResult(progress, result)
			return reconcileErr
		}
	}
	setRepairProviderIDIntegrityResult(progress, result)
	progress.Report(100, msg)
	return nil
}

func setRepairProviderIDIntegrityResult(progress taskmanager.ProgressReporter, result repairProviderIDIntegrityResult) {
	if progress == nil {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	progress.SetResultData(data)
}
