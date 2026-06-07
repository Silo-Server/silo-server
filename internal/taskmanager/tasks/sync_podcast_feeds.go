package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/audiobooks/podcastfeed"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// SyncPodcastFeedsTask refreshes RSS podcast feeds that are due for a poll.
// It wraps podcastfeed.Refresher so the task manager can invoke it on a
// schedule, report progress, and display it in the admin task panel.
type SyncPodcastFeedsTask struct {
	refresher *podcastfeed.Refresher
	store     podcastfeed.Store
}

// NewSyncPodcastFeedsTask constructs the task. refresher and store come
// from the audiobooks subsystem wired in cmd/silo/main.go.
func NewSyncPodcastFeedsTask(refresher *podcastfeed.Refresher, store podcastfeed.Store) *SyncPodcastFeedsTask {
	return &SyncPodcastFeedsTask{refresher: refresher, store: store}
}

func (t *SyncPodcastFeedsTask) Key() string  { return "sync_podcast_feeds" }
func (t *SyncPodcastFeedsTask) Name() string { return "Sync Podcast Feeds" }
func (t *SyncPodcastFeedsTask) Description() string {
	return "Refreshes RSS podcast feeds that are due for polling and upserts new episodes"
}
func (t *SyncPodcastFeedsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *SyncPodcastFeedsTask) IsHidden() bool { return false }

func (t *SyncPodcastFeedsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 10 * 60 * 1000}, // every 10 minutes
	}
}

func (t *SyncPodcastFeedsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Checking for due podcast feeds")

	attempted, err := t.refresher.RefreshDue(ctx, t.store)
	if err != nil {
		return fmt.Errorf("podcast feed refresh: %w", err)
	}

	result, _ := json.Marshal(map[string]int{"feeds_attempted": attempted})
	progress.SetResultData(result)
	progress.Report(100, fmt.Sprintf("Podcast feed sync complete (%d feeds attempted)", attempted))
	return nil
}
