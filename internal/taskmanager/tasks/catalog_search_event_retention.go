package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	catalogSearchEventRetention = 24 * time.Hour
	catalogSearchEventBatchSize = 5000
)

type catalogSearchEventPruner interface {
	PruneProcessed(ctx context.Context, provider string, cutoff time.Time, afterID int64, limit int) (deleted, lastID int64, err error)
}

// CatalogSearchEventRetentionTask bounds processed search outbox history
// independently of whether new events are waiting to be synced.
type CatalogSearchEventRetentionTask struct {
	pruner catalogSearchEventPruner
	now    func() time.Time
}

func NewCatalogSearchEventRetentionTask(pruner catalogSearchEventPruner) *CatalogSearchEventRetentionTask {
	return &CatalogSearchEventRetentionTask{pruner: pruner, now: time.Now}
}

func (t *CatalogSearchEventRetentionTask) Key() string { return "cleanup_catalog_search_index_events" }
func (t *CatalogSearchEventRetentionTask) Name() string {
	return "Cleanup Catalog Search Index Events"
}
func (t *CatalogSearchEventRetentionTask) Description() string {
	return "Prunes acknowledged catalog search outbox events after the recovery window."
}
func (t *CatalogSearchEventRetentionTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategorySystem
}
func (t *CatalogSearchEventRetentionTask) IsHidden() bool { return false }
func (t *CatalogSearchEventRetentionTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((24 * time.Hour) / time.Millisecond)},
	}
}

func (t *CatalogSearchEventRetentionTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.pruner == nil {
		progress.Report(100, "Catalog search event retention is not configured")
		return nil
	}
	progress.Report(0, "Pruning processed catalog search events")
	cutoff := t.now().Add(-catalogSearchEventRetention)
	var total int64
	var afterID int64
	for {
		deleted, lastID, err := t.pruner.PruneProcessed(ctx, catalog.SearchProviderMeilisearch, cutoff, afterID, catalogSearchEventBatchSize)
		total += deleted
		if err != nil {
			return fmt.Errorf("catalog search event retention (deleted %d rows): %w", total, err)
		}
		afterID = lastID
		if deleted < catalogSearchEventBatchSize {
			// SKIP LOCKED can shorten a batch; any skipped rows remain safe and
			// become eligible again on the next daily run.
			break
		}
		progress.Report(50, fmt.Sprintf("Pruned %d processed catalog search events", total))
	}
	progress.Report(100, fmt.Sprintf("Pruned %d processed catalog search events", total))
	return nil
}
