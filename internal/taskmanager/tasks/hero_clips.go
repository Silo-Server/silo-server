package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/heroclips"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// heroClipsPerRun bounds one run. A run that stops at the bound leaves the rest
// for the next one, so a large feature refresh cannot hold a task slot for hours.
const heroClipsPerRun = 12

// heroClipRetryAfter is how long a title that failed waits before another try.
// A trailer that 404s today is usually still gone tomorrow.
const heroClipRetryAfter = 24 * time.Hour

// HeroClipCandidates lists the titles a run should make clips for.
type HeroClipCandidates interface {
	ClipCandidates(ctx context.Context, recipe string, retryAfter time.Duration, limit int) ([]heroclips.Candidate, error)
}

// HeroClipGenerator makes one title's clip. Satisfied by *heroclips.Generator.
type HeroClipGenerator interface {
	Create(ctx context.Context, candidate heroclips.Candidate) error
}

// GenerateHeroClipsTask gives the titles the featured rows hold a hero clip:
// a short cut of the title's own trailer, played muted behind the backdrop.
//
// It runs as a task rather than on a page load, so an administrator can see it,
// schedule it, or turn it off where every other piece of background work is
// managed.
type GenerateHeroClipsTask struct {
	candidates HeroClipCandidates
	generator  HeroClipGenerator
	recipe     heroclips.Recipe
}

func NewGenerateHeroClipsTask(candidates HeroClipCandidates, generator HeroClipGenerator, recipe heroclips.Recipe) *GenerateHeroClipsTask {
	return &GenerateHeroClipsTask{candidates: candidates, generator: generator, recipe: recipe}
}

func (t *GenerateHeroClipsTask) Key() string  { return "generate_hero_clips" }
func (t *GenerateHeroClipsTask) Name() string { return "Generate Hero Clips" }
func (t *GenerateHeroClipsTask) Description() string {
	return "Downloads the trailers of the titles the featured rows hold and cuts each into the short clip a video hero plays behind its backdrop."
}
func (t *GenerateHeroClipsTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *GenerateHeroClipsTask) IsHidden() bool { return false }
func (t *GenerateHeroClipsTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeStartup},
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: int64((6 * time.Hour) / time.Millisecond)},
	}
}

func (t *GenerateHeroClipsTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.candidates == nil || t.generator == nil {
		progress.Report(100, "Hero clips are not configured")
		return nil
	}

	candidates, err := t.candidates.ClipCandidates(ctx, t.recipe.String(), heroClipRetryAfter, heroClipsPerRun)
	if err != nil {
		return fmt.Errorf("list hero clip candidates: %w", err)
	}
	if len(candidates) == 0 {
		progress.Report(100, "Every featured title already has its clip")
		return nil
	}

	// Create records its own failures on the title and reports success, so the
	// run keeps going and the row is what makes a broken trailer wait.
	for index, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress.Report(float64(index)*100/float64(len(candidates)), fmt.Sprintf("Making a clip for %s", candidate.ContentID))
		if err := t.generator.Create(ctx, candidate); err != nil {
			return fmt.Errorf("generate hero clip for %s: %w", candidate.ContentID, err)
		}
	}

	progress.Report(100, fmt.Sprintf("Checked %d featured titles", len(candidates)))

	return nil
}
