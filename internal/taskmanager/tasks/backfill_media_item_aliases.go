package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const mediaItemAliasBackfillBatchSize = 500

type MediaItemAliasBackfiller interface {
	BackfillBatch(ctx context.Context, afterContentID string, limit int) (nextContentID string, processed int, err error)
	// ResetCompletedBackfill re-arms a finished backfill so a manual re-run
	// performs a fresh pass instead of a permanent no-op; an interrupted run
	// keeps its persisted cursor and resumes.
	ResetCompletedBackfill(ctx context.Context) error
}

// BackfillMediaItemAliasesTask seeds the alias table from metadata Silo
// already owns. It is manual and idempotent; provider refreshes subsequently
// replace only their own authoritative aliases.
type BackfillMediaItemAliasesTask struct {
	backfiller MediaItemAliasBackfiller
}

func NewBackfillMediaItemAliasesTask(backfiller MediaItemAliasBackfiller) *BackfillMediaItemAliasesTask {
	return &BackfillMediaItemAliasesTask{backfiller: backfiller}
}

func (t *BackfillMediaItemAliasesTask) Key() string { return "backfill_media_item_aliases" }
func (t *BackfillMediaItemAliasesTask) Name() string {
	return "Backfill Media Item Aliases"
}
func (t *BackfillMediaItemAliasesTask) Description() string {
	return "Seeds searchable aliases from existing original and localized media titles in resumable batches."
}
func (t *BackfillMediaItemAliasesTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *BackfillMediaItemAliasesTask) IsHidden() bool { return false }
func (t *BackfillMediaItemAliasesTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return nil
}

func (t *BackfillMediaItemAliasesTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.backfiller == nil {
		progress.Report(100, "Media item alias backfill is not configured")
		return nil
	}

	if err := t.backfiller.ResetCompletedBackfill(ctx); err != nil {
		return fmt.Errorf("reset completed media item alias backfill: %w", err)
	}

	cursor := ""
	total := 0
	progress.Report(0, "Backfilling media item aliases")
	for {
		next, processed, err := t.backfiller.BackfillBatch(ctx, cursor, mediaItemAliasBackfillBatchSize)
		if err != nil {
			return fmt.Errorf("backfill media item aliases after %q: %w", cursor, err)
		}
		total += processed
		if processed == 0 || next == cursor {
			break
		}
		cursor = next
		progress.Report(0, fmt.Sprintf("Backfilled aliases for %d media items", total))
		if processed < mediaItemAliasBackfillBatchSize {
			break
		}
	}

	result, _ := json.Marshal(map[string]any{"processed_items": total, "last_content_id": cursor})
	progress.SetResultData(result)
	progress.Report(100, fmt.Sprintf("Backfilled aliases for %d media items", total))
	return nil
}
