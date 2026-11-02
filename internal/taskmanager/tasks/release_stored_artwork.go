package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// StoredArtworkReleaseRunner is the release surface. Satisfied by
// *metadata.StoredArtworkReleaser.
type StoredArtworkReleaseRunner interface {
	CachingOff(ctx context.Context) (bool, error)
	Run(ctx context.Context) (metadata.StoredArtworkReleaseStats, error)
}

// ReleaseStoredArtworkTask moves stored artwork back to its providers while
// metadata.cache_images is off; see metadata.StoredArtworkReleaser. It runs at
// startup, daily, and when the setting is turned off, and does nothing while
// caching is on.
type ReleaseStoredArtworkTask struct {
	runner StoredArtworkReleaseRunner
}

func NewReleaseStoredArtworkTask(runner StoredArtworkReleaseRunner) *ReleaseStoredArtworkTask {
	return &ReleaseStoredArtworkTask{runner: runner}
}

// ReleaseStoredArtworkTaskKey is the task's key.
const ReleaseStoredArtworkTaskKey = "release_stored_artwork"

func (t *ReleaseStoredArtworkTask) Key() string  { return ReleaseStoredArtworkTaskKey }
func (t *ReleaseStoredArtworkTask) Name() string { return "Release Stored Artwork" }
func (t *ReleaseStoredArtworkTask) Description() string {
	return "While provider artwork is not saved to storage, points stored artwork back at its providers and queues refreshes for artwork only a provider can replace"
}
func (t *ReleaseStoredArtworkTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *ReleaseStoredArtworkTask) IsHidden() bool { return true }

func (t *ReleaseStoredArtworkTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Daily as well as at startup: a pass also undoes a copy a job still in
	// flight published after the previous one, and picks up artwork a storage
	// recovery cleared.
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(24 * time.Hour / time.Millisecond)},
	}
}

// ShouldRun skips scheduled runs while caching is on.
func (t *ReleaseStoredArtworkTask) ShouldRun(ctx context.Context) (bool, error) {
	if t.runner == nil {
		return false, nil
	}
	return t.runner.CachingOff(ctx)
}

func (t *ReleaseStoredArtworkTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.runner == nil {
		return nil
	}
	stats, err := t.runner.Run(ctx)
	if err != nil {
		return err
	}
	progress.Report(100, fmt.Sprintf("Released %d, queued %d refreshes", stats.Released, stats.RefreshesQueued))
	return nil
}
