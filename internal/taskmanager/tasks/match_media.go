package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// BatchMatcher processes a batch of unmatched media files.
type BatchMatcher interface {
	ProcessBatch(ctx context.Context) (processed int, err error)
}

// MatchMediaTask processes unmatched media files against metadata providers.
type MatchMediaTask struct {
	matcher BatchMatcher
}

// NewMatchMediaTask creates a new MatchMediaTask.
func NewMatchMediaTask(matcher BatchMatcher) *MatchMediaTask {
	return &MatchMediaTask{matcher: matcher}
}

func (t *MatchMediaTask) Key() string  { return "match_media" }
func (t *MatchMediaTask) Name() string { return "Match Media Files" }
func (t *MatchMediaTask) Description() string {
	return "Matches unmatched media files to metadata from providers"
}
func (t *MatchMediaTask) Category() taskmanager.TaskCategory { return taskmanager.TaskCategoryMetadata }
func (t *MatchMediaTask) IsHidden() bool                     { return true }

func (t *MatchMediaTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 30_000},
	}
}

func (t *MatchMediaTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Processing unmatched files")

	processed, err := t.matcher.ProcessBatch(ctx)
	if err != nil {
		return fmt.Errorf("processing unmatched files: %w", err)
	}

	progress.Report(100, fmt.Sprintf("Processed %d files", processed))
	return nil
}
