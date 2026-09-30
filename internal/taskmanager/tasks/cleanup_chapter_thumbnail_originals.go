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

// chapterOriginalsPagesPerCall bounds one cleaner call, so the checkpoint
// advances every chapterOriginalsPagesPerCall listing pages.
const chapterOriginalsPagesPerCall = 50

type chapterOriginalsCheckpoint struct {
	Identity string `json:"identity"`
	Token    string `json:"token,omitempty"`
	// Deferred records that the current pass left originals behind (too new
	// to delete yet, or a failed delete), so reaching the end starts another
	// pass instead of finishing.
	Deferred bool `json:"deferred,omitempty"`
	Done     bool `json:"done,omitempty"`
}

// ChapterThumbnailOriginalsCleaner is the cleanup surface. Satisfied by
// *chapterthumbs.OriginalsCleaner; nil when storage is not configured.
type ChapterThumbnailOriginalsCleaner interface {
	Run(ctx context.Context, token string, maxPages int) (chapterthumbs.OriginalsCleanupStats, error)
}

// CleanupChapterThumbnailOriginalsTask deletes the full-size chapter thumbnail
// originals earlier builds stored beside the 300px image clients load. It runs
// until one full pass over chapter-images/ leaves nothing behind, then records
// that and stops running on its schedule.
type CleanupChapterThumbnailOriginalsTask struct {
	cleaner  ChapterThumbnailOriginalsCleaner
	settings ArtworkReconcileSettingsStore
	identity string
}

func NewCleanupChapterThumbnailOriginalsTask(cleaner ChapterThumbnailOriginalsCleaner, settings ArtworkReconcileSettingsStore, identity string) *CleanupChapterThumbnailOriginalsTask {
	return &CleanupChapterThumbnailOriginalsTask{cleaner: cleaner, settings: settings, identity: identity}
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
	// Startup reclaims the space on the first boot after the upgrade. The
	// daily interval finishes what the age floor held back.
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

	cp := t.readCheckpoint(ctx)
	if cp.Done {
		cp = chapterOriginalsCheckpoint{Identity: t.identity}
	}
	var total chapterthumbs.OriginalsCleanupStats
	progress.Report(0, "Deleting full-size chapter thumbnails")
	for {
		stats, err := t.cleaner.Run(ctx, cp.Token, chapterOriginalsPagesPerCall)
		if stats.Skipped {
			// Another node holds the lock and is mid-pass; writing this
			// node's cursor back would drag its progress backwards.
			progress.Report(100, "Another server is already cleaning up chapter thumbnails")
			return nil
		}
		total.Scanned += stats.Scanned
		total.Originals += stats.Originals
		total.Deleted += stats.Deleted
		total.Referenced += stats.Referenced
		total.TooNew += stats.TooNew
		total.DeleteFailed += stats.DeleteFailed
		total.Pages += stats.Pages

		cp.Token = stats.NextToken
		cp.Deferred = cp.Deferred || stats.TooNew > 0 || stats.DeleteFailed > 0
		if err != nil {
			t.saveCheckpoint(ctx, cp)
			t.setResult(progress, total)
			return fmt.Errorf("cleaning up chapter thumbnail originals: %w", err)
		}
		if stats.Done {
			if cp.Deferred {
				cp = chapterOriginalsCheckpoint{Identity: t.identity}
			} else {
				cp = chapterOriginalsCheckpoint{Identity: t.identity, Done: true}
			}
			t.saveCheckpoint(ctx, cp)
			break
		}
		t.saveCheckpoint(ctx, cp)
		progress.Report(0, fmt.Sprintf("Deleted %d of %d full-size chapter thumbnails so far", total.Deleted, total.Originals))
	}

	t.setResult(progress, total)
	left := total.TooNew + total.DeleteFailed
	if left > 0 {
		progress.Report(100, fmt.Sprintf(
			"Deleted %d full-size chapter thumbnails; %d are left for the next run", total.Deleted, left))
		return nil
	}
	progress.Report(100, fmt.Sprintf("Deleted %d full-size chapter thumbnails; none are left to delete", total.Deleted))
	return nil
}

func (t *CleanupChapterThumbnailOriginalsTask) setResult(progress taskmanager.ProgressReporter, total chapterthumbs.OriginalsCleanupStats) {
	if data, err := json.Marshal(total); err == nil {
		progress.SetResultData(data)
	}
}
