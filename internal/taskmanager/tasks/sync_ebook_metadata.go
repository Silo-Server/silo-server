package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/ebooks"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

const (
	ebookMetadataExecutionBudget = 4 * time.Minute
	ebookBackfillMaxClaimsEnv    = "SILO_EBOOK_BACKFILL_MAX_CLAIMS"
	ebookBackfillBatchDelayEnv   = "SILO_EBOOK_BACKFILL_BATCH_DELAY"
)

type ebookMetadataEnricher interface {
	ReadyCount(ctx context.Context, scope ebooks.EnrichmentScope) (int, error)
	RunLimited(ctx context.Context, scope ebooks.EnrichmentScope, maxClaims int) (ebooks.EnrichmentRunResult, error)
}

type ebookMetadataTask struct {
	enricher    ebookMetadataEnricher
	scope       ebooks.EnrichmentScope
	key         string
	name        string
	description string
	triggers    []taskmanager.TriggerConfig
	budget      time.Duration
	now         func() time.Time
	maxClaims   int
	batchDelay  time.Duration
	sleep       func(context.Context, time.Duration) error
	errorPrefix string
	configErr   error
}

// SyncEbookMetadataTask drains new and recurring ebook metadata work.
type SyncEbookMetadataTask struct {
	*ebookMetadataTask
}

// BackfillEbookMetadataTask drains the legacy ebook backlog on its own
// interval, isolated from the scheduled sync lane so backlog work never
// competes with fresh-item enrichment.
type BackfillEbookMetadataTask struct {
	*ebookMetadataTask
}

func NewSyncEbookMetadataTask(enricher ebookMetadataEnricher) *SyncEbookMetadataTask {
	return &SyncEbookMetadataTask{ebookMetadataTask: &ebookMetadataTask{
		enricher:    enricher,
		scope:       ebooks.EnrichmentScopeIncremental,
		key:         "sync_ebook_metadata",
		name:        "Sync Ebook Metadata",
		description: "Fetches metadata for new ebooks and ebooks due for a recurring refresh",
		triggers:    []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 5 * 60 * 1000}},
		budget:      ebookMetadataExecutionBudget,
		now:         time.Now,
		errorPrefix: "ebook metadata sync",
	}}
}

func NewBackfillEbookMetadataTask(enricher ebookMetadataEnricher) *BackfillEbookMetadataTask {
	maxClaims, maxClaimsErr := parseEbookBackfillMaxClaims(os.Getenv(ebookBackfillMaxClaimsEnv))
	batchDelay, batchDelayErr := parseEbookBackfillBatchDelay(os.Getenv(ebookBackfillBatchDelayEnv))
	return &BackfillEbookMetadataTask{ebookMetadataTask: &ebookMetadataTask{
		enricher:    enricher,
		scope:       ebooks.EnrichmentScopeLegacy,
		key:         "backfill_ebook_metadata",
		name:        "Backfill Ebook Metadata",
		description: "Enriches the legacy ebook backlog without competing with scheduled metadata sync",
		triggers:    []taskmanager.TriggerConfig{{Type: taskmanager.TriggerTypeInterval, IntervalMs: 15 * 60 * 1000}},
		budget:      ebookMetadataExecutionBudget,
		now:         time.Now,
		maxClaims:   maxClaims,
		batchDelay:  batchDelay,
		sleep:       sleepEbookBackfill,
		errorPrefix: "ebook metadata backfill",
		configErr:   errors.Join(maxClaimsErr, batchDelayErr),
	}}
}

func (t *ebookMetadataTask) Key() string         { return t.key }
func (t *ebookMetadataTask) Name() string        { return t.name }
func (t *ebookMetadataTask) Description() string { return t.description }
func (t *ebookMetadataTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *ebookMetadataTask) IsHidden() bool { return false }

func (t *ebookMetadataTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return append([]taskmanager.TriggerConfig(nil), t.triggers...)
}

func (t *ebookMetadataTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.configErr != nil {
		return fmt.Errorf("invalid ebook backfill configuration: %w", t.configErr)
	}
	progress.Report(0, fmt.Sprintf("%s started", t.name))
	started := t.now()
	initialReady, err := t.enricher.ReadyCount(ctx, t.scope)
	if err != nil {
		return fmt.Errorf("%s: count initial work: %w", t.errorPrefix, err)
	}
	total := ebooks.EnrichmentRunResult{Remaining: initialReady}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		claimLimit := 0
		if t.maxClaims > 0 {
			claimLimit = t.maxClaims - total.Claimed
			if claimLimit <= 0 {
				reportEbookEnrichmentPause(progress, total, fmt.Sprintf(
					"Paused after reaching manual backfill claim cap of %d; remaining work will retry later.",
					t.maxClaims,
				))
				return nil
			}
		}
		batch, err := t.enricher.RunLimited(ctx, t.scope, claimLimit)
		if err != nil {
			return fmt.Errorf("%s: %w", t.errorPrefix, err)
		}
		addEbookEnrichmentResult(&total, batch)
		total.Remaining = max(total.Remaining-batch.Claimed, 0)
		total.HasMore = batch.HasMore
		reportEbookEnrichmentProgress(progress, total, false)

		if err := ctx.Err(); err != nil {
			return err
		}
		if ebookEnrichmentBatchMadeNoProgress(batch) {
			reportEbookEnrichmentCircuitBreak(progress, total)
			return nil
		}
		if !batch.HasMore {
			reportEbookEnrichmentProgress(progress, total, true)
			return nil
		}
		if batch.Claimed == 0 {
			return nil
		}
		if t.maxClaims > 0 && total.Claimed >= t.maxClaims {
			reportEbookEnrichmentPause(progress, total, fmt.Sprintf(
				"Paused after reaching manual backfill claim cap of %d; remaining work will retry later.",
				t.maxClaims,
			))
			return nil
		}
		if t.now().Sub(started) >= t.budget {
			reportEbookEnrichmentProgress(progress, total, false)
			return nil
		}
		if t.batchDelay > 0 && ebookEnrichmentBatchMadeProgress(batch) {
			if t.batchDelay >= t.budget-t.now().Sub(started) {
				reportEbookEnrichmentPause(progress, total,
					"Paused before the next batch because its delay would exceed the ebook metadata execution budget; remaining work will retry later.")
				return nil
			}
			if err := t.sleep(ctx, t.batchDelay); err != nil {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if t.now().Sub(started) >= t.budget {
				reportEbookEnrichmentPause(progress, total,
					"Paused after reaching the ebook metadata execution budget; remaining work will retry later.")
				return nil
			}
		}
	}
}

func parseEbookBackfillMaxClaims(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", ebookBackfillMaxClaimsEnv, raw)
	}
	return value, nil
}

func parseEbookBackfillBatchDelay(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive Go duration, got %q", ebookBackfillBatchDelayEnv, raw)
	}
	return value, nil
}

func sleepEbookBackfill(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func ebookEnrichmentBatchMadeNoProgress(batch ebooks.EnrichmentRunResult) bool {
	return batch.Claimed > 0 &&
		batch.Enriched == 0 &&
		batch.NoMatch == 0 &&
		batch.Failed+batch.Deferred >= batch.Claimed
}

func ebookEnrichmentBatchMadeProgress(batch ebooks.EnrichmentRunResult) bool {
	return batch.Enriched > 0 || batch.NoMatch > 0
}

func addEbookEnrichmentResult(total *ebooks.EnrichmentRunResult, batch ebooks.EnrichmentRunResult) {
	total.Claimed += batch.Claimed
	total.Enriched += batch.Enriched
	total.NoMatch += batch.NoMatch
	total.Failed += batch.Failed
	total.Deferred += batch.Deferred
	total.Discarded += batch.Discarded
}

func reportEbookEnrichmentProgress(
	progress taskmanager.ProgressReporter,
	result ebooks.EnrichmentRunResult,
	complete bool,
) {
	data, _ := json.Marshal(result)
	progress.SetResultData(data)

	percent := ebookEnrichmentPercent(result)
	if complete && result.Remaining == 0 {
		percent = 100
	}
	progress.Report(percent, fmt.Sprintf(
		"Claimed %d, enriched %d, no match %d, failed %d, deferred %d, discarded %d, remaining %d",
		result.Claimed,
		result.Enriched,
		result.NoMatch,
		result.Failed,
		result.Deferred,
		result.Discarded,
		result.Remaining,
	))
}

func reportEbookEnrichmentCircuitBreak(
	progress taskmanager.ProgressReporter,
	result ebooks.EnrichmentRunResult,
) {
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	percent := ebookEnrichmentPercent(result)
	if percent >= 100 {
		percent = 99
	}
	progress.Report(percent, fmt.Sprintf(
		"Paused after a full batch made no progress; remaining work will retry later. Claimed %d, failed %d, deferred %d, remaining %d",
		result.Claimed,
		result.Failed,
		result.Deferred,
		result.Remaining,
	))
}

func reportEbookEnrichmentPause(
	progress taskmanager.ProgressReporter,
	result ebooks.EnrichmentRunResult,
	message string,
) {
	data, _ := json.Marshal(result)
	progress.SetResultData(data)
	percent := ebookEnrichmentPercent(result)
	if percent >= 100 {
		percent = 99
	}
	progress.Report(percent, fmt.Sprintf(
		"%s Claimed %d, enriched %d, no match %d, failed %d, deferred %d, discarded %d, remaining %d",
		message,
		result.Claimed,
		result.Enriched,
		result.NoMatch,
		result.Failed,
		result.Deferred,
		result.Discarded,
		result.Remaining,
	))
}

func ebookEnrichmentPercent(result ebooks.EnrichmentRunResult) float64 {
	if result.Remaining == 0 {
		if result.HasMore {
			return 99
		}
		return 100
	}
	total := result.Claimed + result.Remaining
	if total <= 0 {
		return 0
	}
	percent := float64(result.Claimed) * 100 / float64(total)
	if percent >= 100 {
		return 99
	}
	return percent
}
