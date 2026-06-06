package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// ContributionRunner submits a file's eligible markers (satisfied by
// *markers.ContributionService).
type ContributionRunner interface {
	ContributeFile(ctx context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error)
}

// AutoContributeConfigReader exposes per-provider contribution config (satisfied
// by *markers.ProviderConfigStore).
type AutoContributeConfigReader interface {
	List() []markers.ProviderConfig
}

// ContributionCandidateSource lists local-intro files eligible for auto
// contribution (satisfied by *markers.ContributionStore).
type ContributionCandidateSource interface {
	CandidateLocalIntroFiles(ctx context.Context, minConfidence float64, afterID, limit int) ([]int, error)
}

// ContributionFileLoader loads files by id (satisfied by *scanner.FileRepository).
type ContributionFileLoader interface {
	GetByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error)
}

// ContributeMarkersTask submits high-confidence local intro markers to providers
// that have auto-contribution enabled. It is a no-op when no provider opts in.
type ContributeMarkersTask struct {
	service    ContributionRunner
	config     AutoContributeConfigReader
	candidates ContributionCandidateSource
	files      ContributionFileLoader
}

// NewContributeMarkersTask constructs the task.
func NewContributeMarkersTask(service ContributionRunner, config AutoContributeConfigReader, candidates ContributionCandidateSource, files ContributionFileLoader) *ContributeMarkersTask {
	return &ContributeMarkersTask{service: service, config: config, candidates: candidates, files: files}
}

func (t *ContributeMarkersTask) Key() string  { return "contribute_markers" }
func (t *ContributeMarkersTask) Name() string { return "Contribute Markers" }
func (t *ContributeMarkersTask) Description() string {
	return "Submits high-confidence local intro markers to enabled contribution providers"
}
func (t *ContributeMarkersTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryLibrary
}
func (t *ContributeMarkersTask) IsHidden() bool { return false }

func (t *ContributeMarkersTask) DefaultTriggers() []taskmanager.TriggerConfig {
	// Runs after the 03:30 local-detection task so freshly detected markers are
	// eligible the same night.
	return []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "04:00"}}
}

func (t *ContributeMarkersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.service == nil || t.config == nil || t.candidates == nil || t.files == nil {
		progress.Report(100, "Contribution is not configured")
		return nil
	}

	minConfidence := 0.0
	autoEnabled := false
	for _, c := range t.config.List() {
		if !c.ContributeEnabled || !c.ContributeAutoLocal {
			continue
		}
		if !autoEnabled || c.ContributeMinConfidence < minConfidence {
			minConfidence = c.ContributeMinConfidence
		}
		autoEnabled = true
	}
	if !autoEnabled {
		progress.Report(100, "No provider has auto-contribution enabled")
		return nil
	}

	const batch = 100
	afterID := 0
	submitted, skipped, failed := 0, 0, 0

	for {
		ids, err := t.candidates.CandidateLocalIntroFiles(ctx, minConfidence, afterID, batch)
		if err != nil {
			return fmt.Errorf("load contribution candidates: %w", err)
		}
		if len(ids) == 0 {
			break
		}
		files, err := t.files.GetByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("load candidate files: %w", err)
		}
		byID := make(map[int]*models.MediaFile, len(files))
		for _, f := range files {
			byID[f.ID] = f
		}
		for _, id := range ids {
			afterID = id
			file := byID[id]
			if file == nil {
				continue
			}
			outcomes, err := t.service.ContributeFile(ctx, file, markers.ContributeOptions{Auto: true})
			if err != nil {
				failed++
				continue
			}
			for _, o := range outcomes {
				switch o.Status {
				case markers.OutcomeStatusSkipped:
					skipped++
				case markers.OutcomeStatusRateLimited:
					failed++
					writeContributionTaskResult(progress, submitted, skipped, failed, o.RetryAfter)
					progress.Report(100, fmt.Sprintf("Contribution usage-limited; retry after %s", formatRetryAfter(o.RetryAfter)))
					return nil
				case markers.OutcomeStatusError:
					failed++
				default:
					submitted++
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	writeContributionTaskResult(progress, submitted, skipped, failed, 0)
	progress.Report(100, fmt.Sprintf("Contributed %d, skipped %d, failed %d", submitted, skipped, failed))
	return nil
}

func writeContributionTaskResult(progress taskmanager.ProgressReporter, submitted, skipped, failed int, retryAfter time.Duration) {
	result := map[string]int{"submitted": submitted, "skipped": skipped, "failed": failed}
	if retryAfter > 0 {
		result["retry_after_seconds"] = int(retryAfter.Seconds())
	}
	if data, err := json.Marshal(result); err == nil {
		progress.SetResultData(data)
	}
}

func formatRetryAfter(d time.Duration) string {
	if d <= 0 {
		return "later"
	}
	return d.String()
}
