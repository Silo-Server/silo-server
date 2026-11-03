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

// ContributionRunner lists the providers that share automatically or take
// manual corrections and submits a file's eligible markers (satisfied by
// *markers.ContributionService).
type ContributionRunner interface {
	AutoContributors(ctx context.Context) ([]markers.AutoContributor, error)
	AmendContributors(ctx context.Context) ([]markers.AmendContributor, error)
	ContributeFile(ctx context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error)
}

// ContributionCandidateSource lists local-marker files eligible for auto
// contribution and manual corrections to resend (satisfied by
// *markers.ContributionStore).
type ContributionCandidateSource interface {
	CandidateLocalMarkerFiles(ctx context.Context, kind markers.MarkerKind, providers []markers.AutoContributor, after *markers.ContributionCandidate, limit int) ([]markers.ContributionCandidate, error)
	CandidateManualCorrections(ctx context.Context, providers []markers.AmendContributor, after *markers.ContributionCandidate, limit int) ([]markers.ContributionCandidate, error)
}

// ContributionFileLoader loads files by id (satisfied by *scanner.FileRepository).
type ContributionFileLoader interface {
	GetByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error)
}

var _ taskmanager.StartSpreader = (*ContributeMarkersTask)(nil)

// ContributeMarkersTask submits high-confidence local intro and credits markers
// to providers that have auto-contribution enabled, and resends manual
// corrections a save did not deliver to providers that support amend. It is a
// no-op when no provider opts in.
type ContributeMarkersTask struct {
	service    ContributionRunner
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
	// contributionErrorBackoff is the wait before the next file after one
	// whose submissions all failed. It doubles for each further such file in a
	// row, up to contributionMaxErrorBackoff, so a failing provider is not sent
	// a full night's traffic.
	contributionErrorBackoff    = 30 * time.Second
	contributionMaxErrorBackoff = 10 * time.Minute
	// contributionMaxErrorWaits caps those waits: one more failed file ends
	// the run, and the next scheduled run tries again.
	contributionMaxErrorWaits = 8
)

// NewContributeMarkersTask constructs the task.
func NewContributeMarkersTask(service ContributionRunner, candidates ContributionCandidateSource, files ContributionFileLoader) *ContributeMarkersTask {
	return &ContributeMarkersTask{service: service, candidates: candidates, files: files, wait: sleepContext}
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
func (t *ContributeMarkersTask) Name() string { return "Share detected markers" }
func (t *ContributeMarkersTask) Description() string {
	return "Sends eligible intros and credits detected on this server to providers with automatic sharing enabled, and manual corrections a save could not deliver to providers that accept corrections."
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

// StartSpread spreads scheduled runs over the four hours after their
// configured time. Servers in one time zone would otherwise all submit to a
// shared provider at 04:00.
func (t *ContributeMarkersTask) StartSpread() time.Duration { return 4 * time.Hour }

func (t *ContributeMarkersTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.service == nil || t.candidates == nil || t.files == nil {
		progress.Report(100, "Contribution is not configured")
		return nil
	}

	providers, err := t.service.AutoContributors(ctx)
	if err != nil {
		return fmt.Errorf("load marker sharing settings: %w", err)
	}
	amenders, err := t.service.AmendContributors(ctx)
	if err != nil {
		return fmt.Errorf("load marker sharing settings: %w", err)
	}
	if len(providers) == 0 && len(amenders) == 0 {
		progress.Report(100, "No provider has auto-contribution enabled")
		return nil
	}

	var state contributionState
	// Manual corrections first: there are few, and a backlog of local
	// markers must not hold them back when a provider's daily limit ends the
	// run.
	corrections := func(after *markers.ContributionCandidate) ([]markers.ContributionCandidate, error) {
		return t.candidates.CandidateManualCorrections(ctx, amenders, after, contributionCandidateBatch)
	}
	stopped, err := t.contributeCandidates(ctx, progress, markers.ContributeOptions{Corrections: true}, corrections, &state)
	if err != nil || stopped {
		return err
	}
	// One pass per kind, intros first, so a pass asks each file for the one
	// segment it was selected for.
	for _, kind := range markers.AutoContributedKinds() {
		next := func(after *markers.ContributionCandidate) ([]markers.ContributionCandidate, error) {
			return t.candidates.CandidateLocalMarkerFiles(ctx, kind, providers, after, contributionCandidateBatch)
		}
		opts := markers.ContributeOptions{Segments: []markers.MarkerKind{kind}, Auto: true}
		stopped, err := t.contributeCandidates(ctx, progress, opts, next, &state)
		if err != nil || stopped {
			return err
		}
	}

	counts := state.counts
	counts.write(progress, 0)
	progress.Report(100, fmt.Sprintf("Contributed %d, skipped %d, invalid %d, failed %d", counts.submitted, counts.skipped, counts.invalid, counts.failed))
	return nil
}

// contributionState is one run's tallies and waits, carried across its
// passes.
type contributionState struct {
	counts         contributionCounts
	rateLimitWaits int
	// errorStreak counts the files in a row whose submissions all failed.
	errorStreak int
	// backoffDue is set by a failed file and cleared by the wait before the
	// next one, so the files a provider never sees (skips) don't each wait.
	backoffDue bool
}

// contributeCandidates runs opts for every candidate file next pages through.
// It reports stopped when a provider's usage limit, or a streak of failed
// files, ends the run.
func (t *ContributeMarkersTask) contributeCandidates(
	ctx context.Context,
	progress taskmanager.ProgressReporter,
	opts markers.ContributeOptions,
	next func(after *markers.ContributionCandidate) ([]markers.ContributionCandidate, error),
	state *contributionState,
) (bool, error) {
	counts := &state.counts
	var after *markers.ContributionCandidate
	for {
		candidates, err := next(after)
		if err != nil {
			return false, fmt.Errorf("load contribution candidates: %w", err)
		}
		if len(candidates) == 0 {
			return false, nil
		}
		ids := make([]int, len(candidates))
		for i, c := range candidates {
			ids[i] = c.FileID
		}
		files, err := t.files.GetByIDs(ctx, ids)
		if err != nil {
			return false, fmt.Errorf("load candidate files: %w", err)
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
			if state.backoffDue {
				if state.errorStreak > contributionMaxErrorWaits {
					counts.write(progress, 0)
					progress.Report(100, fmt.Sprintf("Contribution unavailable after %d failed files in a row; the next scheduled run retries", state.errorStreak))
					return true, nil
				}
				backoff := contributionErrorWait(state.errorStreak)
				progress.Report(50, fmt.Sprintf("Contribution failing; waiting %s", backoff))
				if err := t.wait(ctx, backoff); err != nil {
					return false, err
				}
				state.backoffDue = false
			}
			// A retry after a rate limit runs every provider again; the ones
			// that already finished report skips that must not be counted twice.
			tallied := map[string]string{}
			failed := false
			for {
				outcomes, err := t.service.ContributeFile(ctx, file, opts)
				if err != nil {
					counts.failed++
					failed = true
					break
				}
				retryAfter, limited := counts.add(outcomes, tallied)
				if !limited {
					state.rateLimitWaits = 0
					break
				}
				if retryAfter <= 0 || retryAfter > contributionMaxInlineWait || state.rateLimitWaits >= contributionMaxRateLimitWaits {
					counts.write(progress, retryAfter)
					progress.Report(100, fmt.Sprintf("Contribution usage-limited; retry after %s", formatRetryAfter(retryAfter)))
					return true, nil
				}
				state.rateLimitWaits++
				progress.Report(50, fmt.Sprintf("Contribution rate-limited; waiting %s", retryAfter))
				if err := t.wait(ctx, retryAfter); err != nil {
					return false, err
				}
			}
			state.noteFile(failed, tallied)
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
	}
}

// noteFile updates the error streak with one file's result: failed reports
// that ContributeFile itself returned an error, and tallied holds the file's
// final status for each provider and segment. A file with an error and no
// provider answer extends the streak and makes a wait due before the next
// file. Any answer, a refusal included, shows the provider is up and resets
// it. A skip never reached a provider and leaves it as it is.
func (s *contributionState) noteFile(failed bool, tallied map[string]string) {
	answered := false
	for _, status := range tallied {
		switch status {
		case markers.OutcomeStatusError:
			failed = true
		case markers.OutcomeStatusSkipped:
		default:
			answered = true
		}
	}
	switch {
	case answered:
		s.errorStreak = 0
	case failed:
		s.errorStreak++
		s.backoffDue = true
	}
}

// contributionErrorWait is the wait after n failed files in a row.
func contributionErrorWait(n int) time.Duration {
	wait := contributionErrorBackoff
	for i := 1; i < n && wait < contributionMaxErrorBackoff; i++ {
		wait *= 2
	}
	return min(wait, contributionMaxErrorBackoff)
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
