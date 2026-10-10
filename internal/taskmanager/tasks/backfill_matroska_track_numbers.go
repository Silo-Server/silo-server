package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// matroskaTrackBackfillAdvisoryLock spells "SILOMKVT".
const matroskaTrackBackfillAdvisoryLock int64 = 0x53494C4F4D4B5654

// MatroskaTrackBackfiller records Matroska TrackNumbers on subtitle tracks
// probed before the scanner read them.
type MatroskaTrackBackfiller interface {
	Run(ctx context.Context, progress func(result scanner.MatroskaTrackBackfillResult, percent float64)) (scanner.MatroskaTrackBackfillResult, error)
}

// BackfillMatroskaTrackNumbersTask gives embedded MKV subtitle tracks scanned
// before Silo read Matroska track numbers their container track ID, so native
// clients can render them from the stream they direct play. Scans record the
// ID themselves; this task covers files no scan has reprobed since.
//
// It runs at startup. It reads only files with a text subtitle track a client
// could play by its track number, and each file once until the file or its
// stored tracks change, so after the first pass it reads only new or changed
// files. Every API process runs the task manager, so an advisory lock keeps
// one pass reading media across the cluster.
type BackfillMatroskaTrackNumbersTask struct {
	backfiller MatroskaTrackBackfiller
	lock       clusterLock
}

func NewBackfillMatroskaTrackNumbersTask(pool *pgxpool.Pool, backfiller MatroskaTrackBackfiller) *BackfillMatroskaTrackNumbersTask {
	task := &BackfillMatroskaTrackNumbersTask{backfiller: backfiller}
	if pool != nil {
		task.lock = advisoryClusterLock{pool: pool, key: matroskaTrackBackfillAdvisoryLock, name: "Matroska track number backfill"}
	}
	return task
}

func (t *BackfillMatroskaTrackNumbersTask) Key() string { return "backfill_matroska_track_numbers" }
func (t *BackfillMatroskaTrackNumbersTask) Name() string {
	return "Record Matroska Subtitle Track Numbers"
}
func (t *BackfillMatroskaTrackNumbersTask) Description() string {
	return "Reads the track list of MKV files with text subtitles scanned before Silo recorded subtitle track numbers, once per file, so apps can show embedded subtitles without server extraction."
}
func (t *BackfillMatroskaTrackNumbersTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *BackfillMatroskaTrackNumbersTask) IsHidden() bool { return false }
func (t *BackfillMatroskaTrackNumbersTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeStartup}}
}

func (t *BackfillMatroskaTrackNumbersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.backfiller == nil {
		progress.Report(100, "Matroska track number backfill is not configured")
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("claiming Matroska track number backfill: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is recording Matroska track numbers")
			return nil
		}
		defer release()
	}

	progress.Report(0, "Reading MKV track lists")
	result, err := t.backfiller.Run(ctx, func(r scanner.MatroskaTrackBackfillResult, percent float64) {
		progress.Report(percent, fmt.Sprintf("Checked %d MKV files, updated %d", r.Checked, r.Updated))
	})
	if data, marshalErr := json.Marshal(result); marshalErr == nil {
		progress.SetResultData(data)
	}
	if err != nil {
		return fmt.Errorf("backfilling Matroska track numbers: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Checked %d MKV files, updated %d", result.Checked, result.Updated))
	return nil
}
