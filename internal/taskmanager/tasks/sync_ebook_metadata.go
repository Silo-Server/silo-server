package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type ebookMetadataEnricher interface {
	Run(ctx context.Context) (int, error)
}

// SyncEbookMetadataTask runs the periodic ebook enrichment sweep.
// It calls ebooks.Enricher.Run() which selects unenriched ebook media_items,
// resolves the per-folder metadata-provider chain at content_level='ebook',
// and writes results back to the database.
type SyncEbookMetadataTask struct {
	enricher ebookMetadataEnricher
}

// NewSyncEbookMetadataTask constructs the task.
func NewSyncEbookMetadataTask(enricher ebookMetadataEnricher) *SyncEbookMetadataTask {
	return &SyncEbookMetadataTask{enricher: enricher}
}

func (t *SyncEbookMetadataTask) Key() string  { return "sync_ebook_metadata" }
func (t *SyncEbookMetadataTask) Name() string { return "Sync Ebook Metadata" }
func (t *SyncEbookMetadataTask) Description() string {
	return "Fetches metadata (cover art, overview, authors) for ebooks that have not yet been enriched"
}
func (t *SyncEbookMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SyncEbookMetadataTask) IsHidden() bool { return false }

func (t *SyncEbookMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000},
	}
}

func (t *SyncEbookMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Scanning for unenriched ebooks")

	enriched, err := t.enricher.Run(ctx)
	if err != nil {
		return fmt.Errorf("ebook metadata sync: %w", err)
	}

	result, _ := json.Marshal(map[string]int{"items_enriched": enriched})
	progress.SetResultData(result)
	progress.Report(100, fmt.Sprintf("Ebook metadata sync complete (%d items enriched)", enriched))
	return nil
}
