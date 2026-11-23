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

// attachmentBitrateBackfillAdvisoryLock spells "SILOATBR".
const attachmentBitrateBackfillAdvisoryLock int64 = 0x53494C4F41544252

// AttachmentBitrateBackfiller corrects the stored bitrate of MKV files scanned
// while their attachments still counted toward it.
type AttachmentBitrateBackfiller interface {
	Run(ctx context.Context, progress func(result scanner.AttachmentBitrateBackfillResult, percent float64)) (scanner.AttachmentBitrateBackfillResult, error)
}

// BackfillAttachmentBitrateTask recomputes the bitrate of MKV files with ASS
// subtitles scanned before Silo left embedded fonts out of it, so font-heavy
// releases stop looking like high-bitrate video and direct play again. Scans
// store the corrected rate themselves; rescans skip unchanged files, which is
// what this task covers.
//
// The pass runs once per deployment and records when it finished. It runs at
// startup and daily so a pass stopped by stalled storage resumes and files it
// could not read are read again; once done, a run is a single query. Every
// API process runs the task manager, so an advisory lock keeps one pass
// reading media across the cluster.
type BackfillAttachmentBitrateTask struct {
	backfiller AttachmentBitrateBackfiller
	lock       clusterLock
}

func NewBackfillAttachmentBitrateTask(pool *pgxpool.Pool, backfiller AttachmentBitrateBackfiller) *BackfillAttachmentBitrateTask {
	task := &BackfillAttachmentBitrateTask{backfiller: backfiller}
	if pool != nil {
		task.lock = advisoryClusterLock{pool: pool, key: attachmentBitrateBackfillAdvisoryLock, name: "attachment bitrate backfill"}
	}
	return task
}

func (t *BackfillAttachmentBitrateTask) Key() string { return "backfill_attachment_bitrate" }
func (t *BackfillAttachmentBitrateTask) Name() string {
	return "Correct Bitrate of Files with Embedded Fonts"
}
func (t *BackfillAttachmentBitrateTask) Description() string {
	return "Re-reads the header of MKV files with ASS subtitles scanned before Silo left embedded fonts out of the bitrate, so they are not transcoded for a bitrate they do not stream."
}
func (t *BackfillAttachmentBitrateTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *BackfillAttachmentBitrateTask) IsHidden() bool { return false }
func (t *BackfillAttachmentBitrateTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *BackfillAttachmentBitrateTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.backfiller == nil {
		progress.Report(100, "Attachment bitrate backfill is not configured")
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("claiming attachment bitrate backfill: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is correcting bitrates")
			return nil
		}
		defer release()
	}

	progress.Report(0, "Reading MKV headers")
	result, err := t.backfiller.Run(ctx, func(r scanner.AttachmentBitrateBackfillResult, percent float64) {
		progress.Report(percent, attachmentBitrateBackfillMessage(r))
	})
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	if err != nil {
		return fmt.Errorf("backfilling attachment bitrate: %w", err)
	}
	progress.Report(100, attachmentBitrateBackfillMessage(result))
	return nil
}

func attachmentBitrateBackfillMessage(r scanner.AttachmentBitrateBackfillResult) string {
	if r.Done && r.Checked == 0 {
		return "Bitrates already corrected"
	}
	if r.Pending > 0 {
		return fmt.Sprintf("Checked %d MKV files, corrected %d; %d could not be read and are read again on the next run", r.Checked, r.Updated, r.Pending)
	}
	return fmt.Sprintf("Checked %d MKV files, corrected %d", r.Checked, r.Updated)
}
