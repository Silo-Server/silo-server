package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const orphanedMediaCleanupBatchSize = 1000

type OrphanedMediaItemCleaner interface {
	Cleanup(ctx context.Context, batchSize int) (catalog.OrphanedProvisionalCleanupStats, error)
}

type CleanupOrphanedMediaItemsTask struct {
	cleaner OrphanedMediaItemCleaner
}

type cleanupOrphanedMediaItemsResult struct {
	Candidates int `json:"candidates"`
	Deleted    int `json:"deleted"`
}

func NewCleanupOrphanedMediaItemsTask(cleaner OrphanedMediaItemCleaner) *CleanupOrphanedMediaItemsTask {
	return &CleanupOrphanedMediaItemsTask{cleaner: cleaner}
}

func (t *CleanupOrphanedMediaItemsTask) Key() string { return "cleanup_orphaned_media_items" }
func (t *CleanupOrphanedMediaItemsTask) Name() string {
	return "Clean Orphaned Media Items"
}
func (t *CleanupOrphanedMediaItemsTask) Description() string {
	return "Deletes detached provisional media items that have no files, library memberships, or durable user/sync references."
}
func (t *CleanupOrphanedMediaItemsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *CleanupOrphanedMediaItemsTask) IsHidden() bool { return false }

func (t *CleanupOrphanedMediaItemsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return nil
}

func (t *CleanupOrphanedMediaItemsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.cleaner == nil {
		progress.Report(100, "Orphaned media cleanup is not configured")
		return nil
	}

	progress.Report(0, "Checking for orphaned provisional media items")
	stats, err := t.cleaner.Cleanup(ctx, orphanedMediaCleanupBatchSize)
	setCleanupOrphanedMediaItemsResult(progress, cleanupOrphanedMediaItemsResult{
		Candidates: stats.Candidates,
		Deleted:    stats.Deleted,
	})
	if err != nil {
		progress.Report(100, fmt.Sprintf("Orphaned media cleanup failed after deleting %d of %d candidates", stats.Deleted, stats.Candidates))
		return err
	}

	if stats.Candidates == 0 {
		progress.Report(100, "No orphaned provisional media items found")
		return nil
	}
	progress.Report(100, fmt.Sprintf("Deleted %d orphaned provisional media items (%d candidates)", stats.Deleted, stats.Candidates))
	return nil
}

func setCleanupOrphanedMediaItemsResult(progress taskmanager.ProgressReporter, result cleanupOrphanedMediaItemsResult) {
	if progress == nil {
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	progress.SetResultData(data)
}
