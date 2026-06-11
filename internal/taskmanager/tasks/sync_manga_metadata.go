package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type mangaMetadataEnricher interface {
	Run(ctx context.Context) (int, error)
}

// SyncMangaMetadataTask runs the periodic manga enrichment sweep.
// It calls manga.Enricher.Run() which selects unenriched manga media_items,
// resolves the per-folder metadata-provider chain at content_level='manga',
// and writes results back to the database.
type SyncMangaMetadataTask struct {
	enricher mangaMetadataEnricher
}

// NewSyncMangaMetadataTask constructs the task.
func NewSyncMangaMetadataTask(enricher mangaMetadataEnricher) *SyncMangaMetadataTask {
	return &SyncMangaMetadataTask{enricher: enricher}
}

func (t *SyncMangaMetadataTask) Key() string  { return "sync_manga_metadata" }
func (t *SyncMangaMetadataTask) Name() string { return "Sync Manga Metadata" }
func (t *SyncMangaMetadataTask) Description() string {
	return "Fetches metadata (cover art, overview, authors) for manga that have not yet been enriched"
}
func (t *SyncMangaMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SyncMangaMetadataTask) IsHidden() bool { return false }

func (t *SyncMangaMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000},
	}
}

func (t *SyncMangaMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Scanning for unenriched manga")

	enriched, err := t.enricher.Run(ctx)
	if err != nil {
		return fmt.Errorf("manga metadata sync: %w", err)
	}

	result, _ := json.Marshal(map[string]int{"items_enriched": enriched})
	progress.SetResultData(result)
	progress.Report(100, fmt.Sprintf("Manga metadata sync complete (%d items enriched)", enriched))
	return nil
}
