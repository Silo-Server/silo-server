package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/boxsets"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// SyncMovieBoxSetsTaskKey names the box set sync task; turning box sets on
// for a library starts it.
const SyncMovieBoxSetsTaskKey = "sync_movie_box_sets"

type movieBoxSetSyncer interface {
	Run(ctx context.Context, report func(percent float64, message string)) (boxsets.Result, error)
}

// SyncMovieBoxSetsTask looks up the TMDB collections of the movies in
// libraries with box sets on, so they can browse as box sets.
type SyncMovieBoxSetsTask struct {
	syncer movieBoxSetSyncer
}

func NewSyncMovieBoxSetsTask(syncer movieBoxSetSyncer) *SyncMovieBoxSetsTask {
	return &SyncMovieBoxSetsTask{syncer: syncer}
}

func (t *SyncMovieBoxSetsTask) Key() string  { return SyncMovieBoxSetsTaskKey }
func (t *SyncMovieBoxSetsTask) Name() string { return "Sync Movie Box Sets" }
func (t *SyncMovieBoxSetsTask) Description() string {
	return "Looks up which TMDB collection each movie belongs to in libraries with box sets on, and refreshes the collections' names, artwork, and order."
}
func (t *SyncMovieBoxSetsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *SyncMovieBoxSetsTask) IsHidden() bool { return false }
func (t *SyncMovieBoxSetsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64(time.Hour / time.Millisecond)},
	}
}

func (t *SyncMovieBoxSetsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t == nil || t.syncer == nil {
		progress.Report(100, "Box set sync is not configured")
		return nil
	}
	progress.Report(0, "Looking up movie collections")
	res, err := t.syncer.Run(ctx, progress.Report)
	data, _ := json.Marshal(res)
	progress.SetResultData(data)
	if err != nil {
		return fmt.Errorf("sync movie box sets: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Checked %d movies and fetched %d collections", res.MoviesChecked, res.CollectionsFetched))
	return nil
}
