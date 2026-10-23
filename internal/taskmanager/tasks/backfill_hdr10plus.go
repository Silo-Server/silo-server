package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// hdr10PlusBackfillAdvisoryLock spells "SILOHDRP".
const hdr10PlusBackfillAdvisoryLock int64 = 0x53494C4F48445250

// HDR10PlusBackfiller records HDR10+ status on video probed before the
// scanner read it from a decoded frame.
type HDR10PlusBackfiller interface {
	Run(ctx context.Context, progress func(result scanner.HDR10PlusBackfillResult, percent float64)) (scanner.HDR10PlusBackfillResult, error)
}

// BackfillHDR10PlusTask records HDR10+ status on PQ video scanned before Silo
// read it from a decoded frame, so HDR10+ files stop showing as HDR10. Scans
// record it themselves; this task covers files no scan has reprobed since,
// and files whose frame read failed on storage or was skipped by playback's
// probe repair.
//
// It runs at startup and then daily. It reads only PQ files without a
// recorded status, once each, so after the first pass it reads only the few
// new ones. Every API process runs the task manager, so an advisory lock
// keeps one pass reading media across the cluster.
type BackfillHDR10PlusTask struct {
	backfiller HDR10PlusBackfiller
	lock       clusterLock
}

func NewBackfillHDR10PlusTask(pool *pgxpool.Pool, backfiller HDR10PlusBackfiller) *BackfillHDR10PlusTask {
	task := &BackfillHDR10PlusTask{backfiller: backfiller}
	if pool != nil {
		task.lock = advisoryClusterLock{pool: pool, key: hdr10PlusBackfillAdvisoryLock, name: "HDR10+ backfill"}
	}
	return task
}

func (t *BackfillHDR10PlusTask) Key() string  { return "backfill_hdr10_plus" }
func (t *BackfillHDR10PlusTask) Name() string { return "Detect HDR10+ Video" }
func (t *BackfillHDR10PlusTask) Description() string {
	return "Reads the first frame of HDR video scanned before Silo detected HDR10+, once per file, so HDR10+ files are labeled HDR10+ instead of HDR10."
}
func (t *BackfillHDR10PlusTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *BackfillHDR10PlusTask) IsHidden() bool { return false }
func (t *BackfillHDR10PlusTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *BackfillHDR10PlusTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.backfiller == nil {
		progress.Report(100, "HDR10+ backfill is not configured")
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("claiming HDR10+ backfill: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is detecting HDR10+")
			return nil
		}
		defer release()
	}

	progress.Report(0, "Reading HDR video frames")
	result, err := t.backfiller.Run(ctx, func(r scanner.HDR10PlusBackfillResult, percent float64) {
		progress.Report(percent, hdr10PlusBackfillMessage(r))
	})
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	if err != nil {
		return fmt.Errorf("backfilling HDR10+: %w", err)
	}
	progress.Report(100, hdr10PlusBackfillMessage(result))
	return nil
}

func hdr10PlusBackfillMessage(r scanner.HDR10PlusBackfillResult) string {
	return fmt.Sprintf("Checked %d HDR files, found %d HDR10+", r.Checked, r.HDR10Plus)
}
