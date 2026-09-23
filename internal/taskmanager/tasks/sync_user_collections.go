package tasks

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type SyncUserCollectionsTask struct {
	scheduler CollectionSyncRunner
}

func NewSyncUserCollectionsTask(scheduler CollectionSyncRunner) *SyncUserCollectionsTask {
	return &SyncUserCollectionsTask{scheduler: scheduler}
}

func (t *SyncUserCollectionsTask) Key() string  { return "sync_user_collections" }
func (t *SyncUserCollectionsTask) Name() string { return "Sync User Collections" }
func (t *SyncUserCollectionsTask) Description() string {
	return "Refreshes profile-owned imported collections (TMDB, Trakt, MDBList) on their schedule"
}
func (t *SyncUserCollectionsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *SyncUserCollectionsTask) IsHidden() bool { return true }

func (t *SyncUserCollectionsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000},
	}
}

func (t *SyncUserCollectionsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Checking for due user collections")
	resultData, err := t.scheduler.RunOnce(ctx)
	if err != nil {
		return fmt.Errorf("user collection sync scheduler: %w", err)
	}
	if resultData != nil {
		progress.SetResultData(resultData)
	}
	progress.Report(100, "User collection sync complete")
	return nil
}
