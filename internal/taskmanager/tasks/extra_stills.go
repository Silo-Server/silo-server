package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ExtraStillGenerator gives local extras without an image, and movies
// without a poster, a frame of their video; extrastills.Generator.
type ExtraStillGenerator interface {
	Generate(ctx context.Context, limit int) (extras, posters int, err error)
}

// GenerateExtraStillsTask takes a frame for each local extra that has no
// image file beside it, then for each movie that has no poster.
type GenerateExtraStillsTask struct {
	generator ExtraStillGenerator
	limit     int
}

func NewGenerateExtraStillsTask(generator ExtraStillGenerator, limit int) *GenerateExtraStillsTask {
	return &GenerateExtraStillsTask{generator: generator, limit: limit}
}

func (t *GenerateExtraStillsTask) Key() string { return "generate_extra_stills" }

// Name and Description are the fallback for catalog text keyed by the task
// key, which predates the movie poster pass; the task is hidden.
func (t *GenerateExtraStillsTask) Name() string { return "Generate Extra Thumbnails" }
func (t *GenerateExtraStillsTask) Description() string {
	return "Takes a frame from each local extra that has no image file beside it"
}
func (t *GenerateExtraStillsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *GenerateExtraStillsTask) IsHidden() bool { return true }

func (t *GenerateExtraStillsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((15 * time.Minute) / time.Millisecond)},
	}
}

func (t *GenerateExtraStillsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	progress.Report(0, "Finding extras and movies without an image")
	extras, posters, err := t.generator.Generate(ctx, t.limit)
	if err != nil {
		return fmt.Errorf("generate extra thumbnails: %w", err)
	}
	progress.Report(100, fmt.Sprintf("Saved %d extra thumbnails and %d movie posters", extras, posters))
	return nil
}
