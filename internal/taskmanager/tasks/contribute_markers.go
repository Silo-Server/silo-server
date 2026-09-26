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
	CandidateLocalIntroFiles(ctx context.Context, minConfidence float64, providers []string, after *markers.ContributionCandidate, limit int) ([]markers.ContributionCandidate, error)
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
	wait       func(context.Context, time.Duration) error
}

const (
	contributionCandidateBatch = 500
	// contributionMaxInlineWait bounds how long the task sleeps through a
	// provider rate limit. Longer resets are daily usage limits: the run ends
	// and the next scheduled run resumes.
	contributionMaxInlineWait = 2 * time.Minute
	// contributionMaxRateLimitWaits caps consecutive short waits so a provider
	// that keeps answering "retry shortly" cannot hold the task open forever.
	contributionMaxRateLimitWaits = 20
)

// NewContributeMarkersTask constructs the task.
func NewContributeMarkersTask(service ContributionRunner, config AutoContributeConfigReader, candidates ContributionCandidateSource, files ContributionFileLoader) *ContributeMarkersTask {
	return &ContributeMarkersTask{service: service, config: config, candidates: candidates, files: files, wait: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (t *ContributeMarkersTask) Key() string  { return "contribute_markers" }
func (t *ContributeMarkersTask) Name() string { return "Share intro markers" }
func (t *ContributeMarkersTask) Description() string {
	return "Sends eligible intros detected on this server to providers with automatic sharing enabled."
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
	var providers []string
	for _, c := range t.config.List() {
		if !c.ContributeEnabled || !c.ContributeAutoLocal {
			continue
		}
		if len(providers) == 0 || c.ContributeMinConfidence < minConfidence {
			minConfidence = c.ContributeMinConfidence
		}
		providers = append(providers, c.Provider)
	}
	if len(providers) == 0 {
		progress.Report(100, "No provider has auto-contribution enabled")
		return nil
	}

	var counts contributionCounts
	var after *markers.ContributionCandidate
	rateLimitWaits := 0

	for {
		candidates, err := t.candidates.CandidateLocalIntroFiles(ctx, minConfidence, providers, after, contributionCandidateBatch)
		if err != nil {
			return fmt.Errorf("load contribution candidates: %w", err)
		}
		if len(candidates) == 0 {
			break
		}
		ids := make([]int, len(candidates))
		for i, c := range candidates {
			ids[i] = c.FileID
		}
		files, err := t.files.GetByIDs(ctx, ids)
		if err != nil {
			return fmt.Errorf("load candidate files: %w", err)
		}
		byID := make(map[int]*models.MediaFile, len(files))
		for _, f := range files {
			byID[f.ID] = f
		}
		for i := range candidates {
			after = &candidates[i]
			file := byID[candidates[i].FileID]
			if file == nil {
				continue
			}
			// A retry after a rate limit runs every provider again; the ones
			// that already finished report skips that must not be counted twice.
			tallied := map[string]string{}
			for {
				outcomes, err := t.service.ContributeFile(ctx, file, markers.ContributeOptions{Auto: true})
				if err != nil {
					counts.failed++
					break
				}
				retryAfter, limited := counts.add(outcomes, tallied)
				if !limited {
					rateLimitWaits = 0
					break
				}
				if retryAfter <= 0 || retryAfter > contributionMaxInlineWait || rateLimitWaits >= contributionMaxRateLimitWaits {
					counts.write(progress, retryAfter)
					progress.Report(100, fmt.Sprintf("Contribution usage-limited; retry after %s", formatRetryAfter(retryAfter)))
					return nil
				}
				rateLimitWaits++
				progress.Report(50, fmt.Sprintf("Contribution rate-limited; waiting %s", retryAfter))
				if err := t.wait(ctx, retryAfter); err != nil {
					return err
				}
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	counts.write(progress, 0)
	progress.Report(100, fmt.Sprintf("Contributed %d, skipped %d, invalid %d, failed %d", counts.submitted, counts.skipped, counts.invalid, counts.failed))
	return nil
}

type contributionCounts struct {
	submitted, skipped, invalid, failed int
}

// add tallies one file's outcomes, once per provider and segment across the
// file's retries (tallied holds the status counted for each). An error
// releases its claim, so the retry submits again and its outcome replaces the
// counted failure. A rate-limited outcome ends the file's attempt; its reset
// is returned so the caller can wait or stop.
func (c *contributionCounts) add(outcomes []markers.ContributionOutcome, tallied map[string]string) (time.Duration, bool) {
	for _, o := range outcomes {
		if o.Status == markers.OutcomeStatusRateLimited {
			return o.RetryAfter, true
		}
		key := fmt.Sprintf("%s|%d", o.Provider, o.Segment)
		if prev, ok := tallied[key]; ok {
			if prev != markers.OutcomeStatusError {
				continue
			}
			c.failed--
		}
		tallied[key] = o.Status
		switch o.Status {
		case markers.OutcomeStatusSkipped, markers.OutcomeStatusConflict:
			c.skipped++
		case markers.OutcomeStatusInvalid:
			c.invalid++
		case markers.OutcomeStatusError:
			c.failed++
		default:
			c.submitted++
		}
	}
	return 0, false
}

func (c contributionCounts) write(progress taskmanager.ProgressReporter, retryAfter time.Duration) {
	writeContributionTaskResult(progress, c.submitted, c.skipped, c.invalid, c.failed, retryAfter)
}

func writeContributionTaskResult(progress taskmanager.ProgressReporter, submitted, skipped, invalid, failed int, retryAfter time.Duration) {
	result := map[string]int{"submitted": submitted, "skipped": skipped, "invalid": invalid, "failed": failed}
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
