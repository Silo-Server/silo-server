package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type CatalogSearchIndexWorker interface {
	ShouldSyncRun(ctx context.Context) (bool, error)
	SyncOutbox(ctx context.Context, progress catalog.SearchIndexProgressReporter) (catalog.CatalogSearchIndexSyncStats, error)
	Rebuild(ctx context.Context, progress catalog.SearchIndexProgressReporter) (catalog.CatalogSearchIndexRebuildStats, error)
}

type SyncCatalogSearchIndexTask struct {
	worker CatalogSearchIndexWorker
}

func NewSyncCatalogSearchIndexTask(worker CatalogSearchIndexWorker) *SyncCatalogSearchIndexTask {
	return &SyncCatalogSearchIndexTask{worker: worker}
}

func (t *SyncCatalogSearchIndexTask) Key() string  { return "sync_catalog_search_index" }
func (t *SyncCatalogSearchIndexTask) Name() string { return "Sync Catalog Search Index" }
func (t *SyncCatalogSearchIndexTask) Description() string {
	return "Drains catalog search outbox events into the configured Meilisearch index."
}
func (t *SyncCatalogSearchIndexTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *SyncCatalogSearchIndexTask) IsHidden() bool { return true }
func (t *SyncCatalogSearchIndexTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 1000},
	}
}

func (t *SyncCatalogSearchIndexTask) ShouldRun(ctx context.Context) (bool, error) {
	if t == nil || t.worker == nil {
		return false, nil
	}
	return t.worker.ShouldSyncRun(ctx)
}

func (t *SyncCatalogSearchIndexTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.worker == nil {
		progress.Report(100, "Catalog search sync is not configured")
		return nil
	}
	stats, err := t.worker.SyncOutbox(ctx, progress)
	if err != nil {
		progress.Report(100, fmt.Sprintf("Catalog search sync failed after %d events", stats.Events))
		return err
	}
	return nil
}

type RebuildCatalogSearchIndexTask struct {
	worker CatalogSearchIndexWorker
}

func NewRebuildCatalogSearchIndexTask(worker CatalogSearchIndexWorker) *RebuildCatalogSearchIndexTask {
	return &RebuildCatalogSearchIndexTask{worker: worker}
}

func (t *RebuildCatalogSearchIndexTask) Key() string  { return "rebuild_catalog_search_index" }
func (t *RebuildCatalogSearchIndexTask) Name() string { return "Rebuild Catalog Search Index" }
func (t *RebuildCatalogSearchIndexTask) Description() string {
	return "Rebuilds the configured Meilisearch catalog index from PostgreSQL."
}
func (t *RebuildCatalogSearchIndexTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *RebuildCatalogSearchIndexTask) IsHidden() bool { return false }
func (t *RebuildCatalogSearchIndexTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return nil
}

func (t *RebuildCatalogSearchIndexTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.worker == nil {
		progress.Report(100, "Catalog search rebuild is not configured")
		return nil
	}
	stats, err := t.worker.Rebuild(ctx, progress)
	if err != nil {
		progress.Report(100, fmt.Sprintf("Catalog search rebuild failed after %d documents", stats.DocumentCount))
		return err
	}
	return nil
}
