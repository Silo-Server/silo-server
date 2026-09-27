package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// CollectionSyncRunner runs a single pass of the collection sync scheduler.
type CollectionSyncRunner interface {
	RunOnce(ctx context.Context) (json.RawMessage, error)
}

// SyncCollectionsTask finds collections with a sync schedule that are due
// and syncs them. It delegates to CollectionSyncRunner for the actual work.
type SyncCollectionsTask struct {
	scheduler CollectionSyncRunner
}

// NewSyncCollectionsTask creates a new SyncCollectionsTask.
func NewSyncCollectionsTask(scheduler CollectionSyncRunner) *SyncCollectionsTask {
	return &SyncCollectionsTask{scheduler: scheduler}
}

func (t *SyncCollectionsTask) Key() string  { return "sync_collections" }
func (t *SyncCollectionsTask) Name() string { return "Sync Collections" }
func (t *SyncCollectionsTask) Description() string {
	return "Syncs collections that have a scheduled automatic sync"
}
func (t *SyncCollectionsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *SyncCollectionsTask) IsHidden() bool { return true }

func (t *SyncCollectionsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000}, // every 5 minutes
	}
}

func (t *SyncCollectionsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Checking for due collections")

	resultData, err := t.scheduler.RunOnce(ctx)
	if err != nil {
		return fmt.Errorf("collection sync scheduler: %w", err)
	}

	if resultData != nil {
		progress.SetResultData(resultData)
	}

	progress.Report(100, "Collection sync complete")
	return nil
}
