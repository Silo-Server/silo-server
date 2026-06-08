package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/audiobooks"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// SyncAudiobookMetadataTask runs the periodic audiobook enrichment sweep.
// It calls audiobooks.Enricher.Run() which selects unenriched audiobook
// media_items, resolves the per-folder metadata-provider chain at
// content_level='audiobook', and writes results back to the database.
type SyncAudiobookMetadataTask struct {
	enricher *audiobooks.Enricher
}

// NewSyncAudiobookMetadataTask constructs the task.
func NewSyncAudiobookMetadataTask(enricher *audiobooks.Enricher) *SyncAudiobookMetadataTask {
	return &SyncAudiobookMetadataTask{enricher: enricher}
}

func (t *SyncAudiobookMetadataTask) Key() string  { return "sync_audiobook_metadata" }
func (t *SyncAudiobookMetadataTask) Name() string { return "Sync Audiobook Metadata" }
func (t *SyncAudiobookMetadataTask) Description() string {
	return "Fetches metadata (cover art, overview, narrator) for audiobooks that have not yet been enriched"
}
func (t *SyncAudiobookMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SyncAudiobookMetadataTask) IsHidden() bool { return false }

func (t *SyncAudiobookMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000}, // every 5 minutes
	}
}

func (t *SyncAudiobookMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Scanning for unenriched audiobooks")

	enriched, err := t.enricher.Run(ctx)
	if err != nil {
		return fmt.Errorf("audiobook metadata sync: %w", err)
	}

	result, _ := json.Marshal(map[string]int{"items_enriched": enriched})
	progress.SetResultData(result)
	progress.Report(100, fmt.Sprintf("Audiobook metadata sync complete (%d items enriched)", enriched))
	return nil
}
