package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ChapterThumbnailOriginalsCleanupKey is the machine-managed cleanup
// checkpoint, scoped to the storage identity: after a storage move the new
// location may hold copied originals, so the cleanup runs again there.
const ChapterThumbnailOriginalsCleanupKey = config.ChapterThumbnailOriginalsCleanupKey

// chapterOriginalsGrace is how long the cleanup waits after it is first armed
// on a storage location before it walks storage. Until a rolling upgrade
// finishes, a node on an earlier build still writes an original beside each new
// thumbnail; the migration's trigger keeps chapter rows off those originals, so
// deleting them is always safe, but a pass that ran before those nodes were
// gone could miss the last ones and record itself done. The wait is recorded in
// the checkpoint, so restarts do not reset it.
const chapterOriginalsGrace = time.Hour

type chapterOriginalsCheckpoint struct {
	Identity string    `json:"identity"`
	ArmedAt  time.Time `json:"armed_at"`
	Token    string    `json:"token,omitempty"`
	// Deferred records that the current pass left originals behind (too new
	// to delete yet, or a failed delete), so reaching the end starts another
	// pass instead of finishing.
	Deferred bool `json:"deferred,omitempty"`
	Done     bool `json:"done,omitempty"`
}

// ChapterThumbnailOriginalsCleaner is the cleanup surface. Satisfied by
// *chapterthumbs.OriginalsCleaner; nil when storage is not configured.
type ChapterThumbnailOriginalsCleaner interface {
	Exclusive(ctx context.Context, fn func() error) (bool, error)
	Page(ctx context.Context, token string) (chapterthumbs.OriginalsCleanupStats, string, error)
}

// CleanupChapterThumbnailOriginalsTask deletes the full-size chapter thumbnail
// originals earlier builds stored beside the 300px image clients load. Its
// first run on a storage location only arms it; runs from an hour later on
// delete, until one full pass over chapter-images/ leaves nothing behind. It
// then records that and stops running on its schedule.
type CleanupChapterThumbnailOriginalsTask struct {
	cleaner  ChapterThumbnailOriginalsCleaner
	settings ArtworkReconcileSettingsStore
	identity string
	now      func() time.Time
}

func NewCleanupChapterThumbnailOriginalsTask(cleaner ChapterThumbnailOriginalsCleaner, settings ArtworkReconcileSettingsStore, identity string) *CleanupChapterThumbnailOriginalsTask {
	return &CleanupChapterThumbnailOriginalsTask{cleaner: cleaner, settings: settings, identity: identity, now: time.Now}
}

func (t *CleanupChapterThumbnailOriginalsTask) Key() string {
	return "cleanup_chapter_thumbnail_originals"
}
func (t *CleanupChapterThumbnailOriginalsTask) Name() string {
	return "Clean Up Full-Size Chapter Thumbnails"
}
func (t *CleanupChapterThumbnailOriginalsTask) Description() string {
	return "Deletes full-size chapter thumbnail images stored by earlier versions. Clients only load the 300px thumbnail; the task stops once storage holds none."
}
func (t *CleanupChapterThumbnailOriginalsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *CleanupChapterThumbnailOriginalsTask) IsHidden() bool { return false }

func (t *CleanupChapterThumbnailOriginalsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Startup arms the cleanup on the first boot after the upgrade and, on a
	// later boot, deletes. The daily interval covers servers that stay up and
	// finishes what the age floor held back.
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

// ShouldRun skips scheduled runs once this storage location is clean. A manual
// run still starts a fresh pass.
func (t *CleanupChapterThumbnailOriginalsTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.cleaner == nil || t.settings == nil {
		return false, nil
	}
	return !t.readCheckpoint(ctx).Done, nil
}

func (t *CleanupChapterThumbnailOriginalsTask) readCheckpoint(ctx context.Context) chapterOriginalsCheckpoint {
	start := chapterOriginalsCheckpoint{Identity: t.identity}
	raw, err := t.settings.Get(ctx, ChapterThumbnailOriginalsCleanupKey)
	if err != nil || raw == "" {
		return start
	}
	var saved chapterOriginalsCheckpoint
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.Identity != t.identity {
		return start
	}
	return saved
}

func (t *CleanupChapterThumbnailOriginalsTask) saveCheckpoint(ctx context.Context, cp chapterOriginalsCheckpoint) {
	encoded, err := json.Marshal(cp)
	if err != nil {
		return
	}
	if err := t.settings.Set(ctx, ChapterThumbnailOriginalsCleanupKey, string(encoded)); err != nil {
		// Losing the cursor costs re-listing, never correctness: deletion is
		// idempotent.
		slog.WarnContext(ctx, "chapter thumbnail cleanup: saving checkpoint failed",
			"component", "taskmanager", "error", err)
	}
}

func (t *CleanupChapterThumbnailOriginalsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.cleaner == nil || t.settings == nil {
		progress.Report(100, "Chapter thumbnail cleanup is not configured")
		return nil
	}

	var total chapterthumbs.OriginalsCleanupStats
	var summary string
	// The checkpoint is read and written only while holding the lock, so two
	// nodes never resume from, or overwrite, each other's position.
	acquired, err := t.cleaner.Exclusive(ctx, func() error {
		cp := t.readCheckpoint(ctx)
		if cp.Done {
			cp = chapterOriginalsCheckpoint{Identity: t.identity, ArmedAt: cp.ArmedAt}
		}
		now := t.now()
		if cp.ArmedAt.IsZero() {
			cp.ArmedAt = now
			t.saveCheckpoint(ctx, cp)
		}
		if startAt := cp.ArmedAt.Add(chapterOriginalsGrace); now.Before(startAt) {
			summary = fmt.Sprintf("Full-size chapter thumbnails will be deleted from %s, once servers on earlier versions have been upgraded",
				startAt.UTC().Format(time.RFC3339))
			return nil
		}

		progress.Report(0, "Deleting full-size chapter thumbnails")
		for {
			stats, next, err := t.cleaner.Page(ctx, cp.Token)
			total.Add(stats)
			cp.Deferred = cp.Deferred || stats.TooNew > 0 || stats.DeleteFailed > 0
			if err != nil {
				t.saveCheckpoint(ctx, cp)
				return err
			}
			cp.Token = next
			if next == "" {
				done := !cp.Deferred
				cp = chapterOriginalsCheckpoint{Identity: t.identity, ArmedAt: cp.ArmedAt, Done: done}
				t.saveCheckpoint(ctx, cp)
				break
			}
			t.saveCheckpoint(ctx, cp)
			progress.Report(0, fmt.Sprintf("Deleted %d of %d full-size chapter thumbnails so far", total.Deleted, total.Originals))
		}
		if left := total.TooNew + total.DeleteFailed; left > 0 {
			summary = fmt.Sprintf("Deleted %d full-size chapter thumbnails; %d are left for a later run", total.Deleted, left)
		} else {
			summary = fmt.Sprintf("Deleted %d full-size chapter thumbnails; none are left to delete", total.Deleted)
		}
		if total.Referenced > 0 {
			summary += fmt.Sprintf(" (%d kept: a chapter still references them)", total.Referenced)
		}
		return nil
	})
	t.setResult(progress, total)
	if err != nil {
		return fmt.Errorf("cleaning up chapter thumbnail originals: %w", err)
	}
	if !acquired {
		progress.Report(100, "Another server is already cleaning up chapter thumbnails")
		return nil
	}
	progress.Report(100, summary)
	return nil
}

func (t *CleanupChapterThumbnailOriginalsTask) setResult(progress taskmanager.ProgressReporter, total chapterthumbs.OriginalsCleanupStats) {
	if data, err := json.Marshal(total); err == nil {
		progress.SetResultData(data)
	}
}
