package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/animeids"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// AnimeIDsRefresher replaces the stored anime list. Satisfied by
// *animeids.Refresher.
type AnimeIDsRefresher interface {
	Refresh(ctx context.Context) (animeids.Result, error)
}

// RefreshAnimeIDsTask downloads the AniDB-based anime list request routing
// uses to tell anime TMDB does not tag. One server downloads it at a time.
type RefreshAnimeIDsTask struct {
	refresher AnimeIDsRefresher
}

// NewRefreshAnimeIDsTask creates a new RefreshAnimeIDsTask.
func NewRefreshAnimeIDsTask(refresher AnimeIDsRefresher) *RefreshAnimeIDsTask {
	return &RefreshAnimeIDsTask{refresher: refresher}
}

func (t *RefreshAnimeIDsTask) Key() string  { return "refresh_anime_ids" }
func (t *RefreshAnimeIDsTask) Name() string { return "Refresh Anime List" }
func (t *RefreshAnimeIDsTask) Description() string {
	return "Downloads the AniDB-based anime list that request routing uses to recognize anime TMDB does not tag"
}

func (t *RefreshAnimeIDsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}

func (t *RefreshAnimeIDsTask) IsHidden() bool { return false }

func (t *RefreshAnimeIDsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 24 * 60 * 60 * 1000}, // daily
	}
}

func (t *RefreshAnimeIDsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.refresher == nil {
		return errors.New("anime list refresh: refresher not configured")
	}
	progress.Report(0, "Downloading the anime list")
	result, err := t.refresher.Refresh(ctx)
	if err != nil {
		return fmt.Errorf("anime list refresh: %w", err)
	}
	if data, err := json.Marshal(result); err == nil {
		progress.SetResultData(data)
	}
	switch {
	case result.Skipped:
		progress.Report(100, "Another server is refreshing the anime list")
	case result.Unchanged:
		progress.Report(100, "The anime list has not changed")
	default:
		progress.Report(100, fmt.Sprintf("Stored %d anime IDs", result.Entries))
	}
	return nil
}
