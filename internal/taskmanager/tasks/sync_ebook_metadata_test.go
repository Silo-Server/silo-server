package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ebooks"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

type fakeEbookMetadataEnricher struct {
	results         []ebooks.EnrichmentRunResult
	err             error
	countErr        error
	scopes          []ebooks.EnrichmentScope
	limits          []int
	readyCountCalls int
	initialReady    int
	onRun           func(int)
}

func (f *fakeEbookMetadataEnricher) ReadyCount(_ context.Context, _ ebooks.EnrichmentScope) (int, error) {
	f.readyCountCalls++
	if f.countErr != nil {
		return 0, f.countErr
	}
	if f.initialReady > 0 || len(f.results) == 0 {
		return f.initialReady, nil
	}
	return f.results[0].Claimed + f.results[0].Remaining, nil
}

func (f *fakeEbookMetadataEnricher) RunLimited(_ context.Context, scope ebooks.EnrichmentScope, limit int) (ebooks.EnrichmentRunResult, error) {
	f.scopes = append(f.scopes, scope)
	f.limits = append(f.limits, limit)
	call := len(f.scopes)
	if f.onRun != nil {
		f.onRun(call)
	}
	if f.err != nil {
		return ebooks.EnrichmentRunResult{}, f.err
	}
	if call > len(f.results) {
		return ebooks.EnrichmentRunResult{}, nil
	}
	result := f.results[call-1]
	result.HasMore = result.Remaining > 0
	return result, nil
}

type ebookMetadataProgressReporter struct {
	percents []float64
	messages []string
	results  []json.RawMessage
}

func (p *ebookMetadataProgressReporter) Report(percent float64, message string) {
	p.percents = append(p.percents, percent)
	p.messages = append(p.messages, message)
}

func (p *ebookMetadataProgressReporter) SetResultData(data json.RawMessage) {
	p.results = append(p.results, append(json.RawMessage(nil), data...))
}

func TestEbookMetadataTaskPropertiesAndScopes(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{}
	syncTask := NewSyncEbookMetadataTask(enricher)
	backfillTask := NewBackfillEbookMetadataTask(enricher)

	if syncTask.Key() != "sync_ebook_metadata" || syncTask.Name() != "Sync Ebook Metadata" {
		t.Fatalf("unexpected sync identity: %q %q", syncTask.Key(), syncTask.Name())
	}
	if backfillTask.Key() != "backfill_ebook_metadata" {
		t.Fatalf("backfill Key() = %q", backfillTask.Key())
	}
	if !strings.Contains(strings.ToLower(backfillTask.Description()), "legacy") {
		t.Fatalf("backfill description does not explain legacy work: %q", backfillTask.Description())
	}
	for _, task := range []taskmanager.Task{syncTask, backfillTask} {
		if task.Category() != taskmanager.TaskCategoryMetadata || task.IsHidden() {
			t.Fatalf("unexpected task properties for %q", task.Key())
		}
	}
	triggers := syncTask.DefaultTriggers()
	if len(triggers) != 1 || triggers[0].Type != taskmanager.TriggerTypeInterval || triggers[0].IntervalMs != 5*60*1000 {
		t.Fatalf("sync DefaultTriggers() = %#v", triggers)
	}
	backfillTriggers := backfillTask.DefaultTriggers()
	if len(backfillTriggers) != 1 ||
		backfillTriggers[0].Type != taskmanager.TriggerTypeInterval ||
		backfillTriggers[0].IntervalMs != 15*60*1000 {
		t.Fatalf("backfill DefaultTriggers() = %#v, want a 15m interval", backfillTriggers)
	}
}

func TestEbookMetadataTaskDrainsBatchesAndReportsHonestProgress(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 4, Enriched: 2, NoMatch: 1, Failed: 1, Remaining: 3},
		{Claimed: 3, Enriched: 1, Deferred: 2, Remaining: 0},
	}}
	task := NewSyncEbookMetadataTask(enricher)
	progress := &ebookMetadataProgressReporter{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(enricher.scopes) != 2 {
		t.Fatalf("Run calls = %d, want 2", len(enricher.scopes))
	}
	if enricher.readyCountCalls != 1 {
		t.Fatalf("initial exact ready counts = %d, want 1", enricher.readyCountCalls)
	}
	for _, scope := range enricher.scopes {
		if scope != ebooks.EnrichmentScopeIncremental {
			t.Fatalf("sync scope = %q, want incremental", scope)
		}
	}
	var result ebooks.EnrichmentRunResult
	if err := json.Unmarshal(progress.results[len(progress.results)-1], &result); err != nil {
		t.Fatalf("result JSON error: %v", err)
	}
	want := ebooks.EnrichmentRunResult{Claimed: 7, Enriched: 3, NoMatch: 1, Failed: 1, Deferred: 2}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if progress.percents[1] >= 100 {
		t.Fatalf("first batch progress = %.1f, must be below 100 with remaining work", progress.percents[1])
	}
	if got := progress.percents[len(progress.percents)-1]; got != 100 {
		t.Fatalf("final progress = %.1f, want 100", got)
	}
}

func TestEbookMetadataTaskDoesNotReportCompleteWhileBoundedCheckFindsMoreWork(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{
		initialReady: 1,
		results: []ebooks.EnrichmentRunResult{
			{Claimed: 1, Enriched: 1, Remaining: 1},
			{Claimed: 1, Enriched: 1},
		},
	}
	progress := &ebookMetadataProgressReporter{}

	if err := NewSyncEbookMetadataTask(enricher).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := progress.percents[1]; got >= 100 {
		t.Fatalf("first batch progress = %.1f, must stay below 100 while HasMore is true", got)
	}
}

func TestEbookMetadataTaskStopsAfterOneAllFailedBatch(t *testing.T) {
	t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", "1")
	t.Setenv("SILO_EBOOK_BACKFILL_BATCH_DELAY", "1h")
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 4, Failed: 4, Remaining: 100},
		{Claimed: 4, Enriched: 4, Remaining: 96},
	}}
	progress := &ebookMetadataProgressReporter{}
	task := NewBackfillEbookMetadataTask(enricher)
	task.sleep = func(context.Context, time.Duration) error {
		t.Fatal("no-progress batch must circuit-break before pacing")
		return nil
	}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertNoProgressCircuitBreak(t, enricher, progress, ebooks.EnrichmentRunResult{
		Claimed: 4, Failed: 4, Remaining: 100,
	})
}

func TestEbookMetadataTaskStopsAfterOneAllDeferredBatch(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 4, Deferred: 4, Remaining: 0},
		{Claimed: 4, Enriched: 4, Remaining: 0},
	}}
	progress := &ebookMetadataProgressReporter{}

	if err := NewBackfillEbookMetadataTask(enricher).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	assertNoProgressCircuitBreak(t, enricher, progress, ebooks.EnrichmentRunResult{
		Claimed: 4, Deferred: 4, Remaining: 0,
	})
}

func TestEbookMetadataTaskContinuesAfterMixedBatchWithProgress(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 4, Enriched: 1, Failed: 2, Deferred: 1, Remaining: 2},
		{Claimed: 2, NoMatch: 2, Remaining: 0},
	}}
	progress := &ebookMetadataProgressReporter{}

	if err := NewBackfillEbookMetadataTask(enricher).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(enricher.scopes) != 2 {
		t.Fatalf("Run calls = %d, want 2 when a mixed batch made progress", len(enricher.scopes))
	}
}

func TestEbookMetadataBackfillStopsAtClaimCap(t *testing.T) {
	t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", "4")
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 2, Enriched: 2, Remaining: 10},
		{Claimed: 2, Enriched: 2, Remaining: 8},
		{Claimed: 2, Enriched: 2, Remaining: 6},
	}}
	progress := &ebookMetadataProgressReporter{}

	if err := NewBackfillEbookMetadataTask(enricher).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(enricher.scopes) != 2 {
		t.Fatalf("Run calls = %d, want 2 at claim cap", len(enricher.scopes))
	}
	var result ebooks.EnrichmentRunResult
	if err := json.Unmarshal(progress.results[len(progress.results)-1], &result); err != nil {
		t.Fatalf("result JSON error: %v", err)
	}
	want := ebooks.EnrichmentRunResult{Claimed: 4, Enriched: 4, Remaining: 8}
	if result != want {
		t.Fatalf("result JSON = %+v, want %+v", result, want)
	}
	if got := progress.percents[len(progress.percents)-1]; got >= 100 {
		t.Fatalf("claim-capped progress = %.1f, must not report completion", got)
	}
	message := strings.ToLower(progress.messages[len(progress.messages)-1])
	if !strings.Contains(message, "claim cap") || !strings.Contains(message, "retry later") {
		t.Fatalf("claim-cap progress message = %q", message)
	}
}

func TestEbookMetadataBackfillPassesRemainingClaimAllowanceToEnricher(t *testing.T) {
	t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", "3")
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 2, Enriched: 2, Remaining: 8},
		{Claimed: 1, Enriched: 1, Remaining: 7},
	}}

	if err := NewBackfillEbookMetadataTask(enricher).Execute(context.Background(), &ebookMetadataProgressReporter{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := enricher.limits; len(got) != 2 || got[0] != 3 || got[1] != 1 {
		t.Fatalf("claim limits = %v, want [3 1]", got)
	}
}

func TestEbookMetadataBackfillDelaysOnlyBetweenProductiveBatches(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 1, Enriched: 1, Remaining: 2},
		{Claimed: 1, NoMatch: 1, Remaining: 1},
		{Claimed: 1, Enriched: 1, Remaining: 0},
	}}
	task := NewBackfillEbookMetadataTask(enricher)
	task.batchDelay = time.Second
	var sleeps []time.Duration
	task.sleep = func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	}

	if err := task.Execute(context.Background(), &ebookMetadataProgressReporter{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != time.Second {
		t.Fatalf("sleeps = %v, want two 1s inter-batch delays", sleeps)
	}
}

func TestEbookMetadataBackfillCancelsDuringBatchDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 1, Enriched: 1, Remaining: 2},
		{Claimed: 1, Enriched: 1, Remaining: 1},
	}}
	task := NewBackfillEbookMetadataTask(enricher)
	task.batchDelay = time.Second
	task.sleep = func(ctx context.Context, _ time.Duration) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	err := task.Execute(ctx, &ebookMetadataProgressReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if len(enricher.scopes) != 1 {
		t.Fatalf("Run calls = %d, want 1 before cancellation", len(enricher.scopes))
	}
}

func TestEbookMetadataBackfillDoesNotDelayPastExecutionBudget(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{
		{Claimed: 1, Enriched: 1, Remaining: 2},
		{Claimed: 1, Enriched: 1, Remaining: 1},
	}}
	task := NewBackfillEbookMetadataTask(enricher)
	task.now = func() time.Time { return now }
	task.budget = 1500 * time.Millisecond
	task.batchDelay = time.Second
	var sleeps int
	task.sleep = func(_ context.Context, delay time.Duration) error {
		sleeps++
		now = now.Add(delay)
		return nil
	}
	progress := &ebookMetadataProgressReporter{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if sleeps != 1 {
		t.Fatalf("sleep calls = %d, want 1 before remaining budget became too short", sleeps)
	}
	if len(enricher.scopes) != 2 {
		t.Fatalf("Run calls = %d, want 2", len(enricher.scopes))
	}
	message := strings.ToLower(progress.messages[len(progress.messages)-1])
	if !strings.Contains(message, "execution budget") {
		t.Fatalf("budget progress message = %q", message)
	}
}

func TestEbookMetadataBackfillInvalidNonemptyEnvironmentFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		max   string
		delay string
	}{
		{name: "malformed", max: "many", delay: "later"},
		{name: "negative", max: "-5", delay: "-1s"},
		{name: "zero", max: "0", delay: "0"},
		{name: "invalid max only", max: "many", delay: "1s"},
		{name: "invalid delay only", max: "20", delay: "later"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", tc.max)
			t.Setenv("SILO_EBOOK_BACKFILL_BATCH_DELAY", tc.delay)
			enricher := &fakeEbookMetadataEnricher{}
			err := NewBackfillEbookMetadataTask(enricher).Execute(context.Background(), &ebookMetadataProgressReporter{})
			if err == nil || !strings.Contains(err.Error(), "invalid ebook backfill configuration") {
				t.Fatalf("Execute() error = %v, want clear invalid configuration error", err)
			}
			if len(enricher.scopes) != 0 || enricher.readyCountCalls != 0 {
				t.Fatalf("invalid configuration reached queue: runs=%d counts=%d", len(enricher.scopes), enricher.readyCountCalls)
			}
		})
	}
}

func TestEbookMetadataBackfillUnsetAndValidEnvironment(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", "")
		t.Setenv("SILO_EBOOK_BACKFILL_BATCH_DELAY", "")
		task := NewBackfillEbookMetadataTask(&fakeEbookMetadataEnricher{})
		if task.configErr != nil || task.maxClaims != 0 || task.batchDelay != 0 {
			t.Fatalf("unset controls = (%d, %s, %v)", task.maxClaims, task.batchDelay, task.configErr)
		}
	})
	t.Run("valid", func(t *testing.T) {
		t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", " 20 ")
		t.Setenv("SILO_EBOOK_BACKFILL_BATCH_DELAY", " 1s ")
		task := NewBackfillEbookMetadataTask(&fakeEbookMetadataEnricher{})
		if task.configErr != nil || task.maxClaims != 20 || task.batchDelay != time.Second {
			t.Fatalf("valid controls = (%d, %s, %v)", task.maxClaims, task.batchDelay, task.configErr)
		}
	})
}

func TestEbookMetadataSyncIgnoresBackfillCanaryEnvironment(t *testing.T) {
	t.Setenv("SILO_EBOOK_BACKFILL_MAX_CLAIMS", "1")
	t.Setenv("SILO_EBOOK_BACKFILL_BATCH_DELAY", "1h")
	task := NewSyncEbookMetadataTask(&fakeEbookMetadataEnricher{})
	if task.maxClaims != 0 || task.batchDelay != 0 {
		t.Fatalf("sync controls = (%d, %s), want disabled", task.maxClaims, task.batchDelay)
	}
}

func assertNoProgressCircuitBreak(
	t *testing.T,
	enricher *fakeEbookMetadataEnricher,
	progress *ebookMetadataProgressReporter,
	want ebooks.EnrichmentRunResult,
) {
	t.Helper()
	if len(enricher.scopes) != 1 {
		t.Fatalf("Run calls = %d, want exactly 1", len(enricher.scopes))
	}
	if len(progress.results) == 0 {
		t.Fatal("no result JSON reported")
	}
	var result ebooks.EnrichmentRunResult
	if err := json.Unmarshal(progress.results[len(progress.results)-1], &result); err != nil {
		t.Fatalf("result JSON error: %v", err)
	}
	if result != want {
		t.Fatalf("result JSON = %+v, want %+v", result, want)
	}
	if got := progress.percents[len(progress.percents)-1]; got >= 100 {
		t.Fatalf("circuit-break progress = %.1f, must not report completion", got)
	}
	message := progress.messages[len(progress.messages)-1]
	if !strings.Contains(strings.ToLower(message), "no progress") ||
		!strings.Contains(strings.ToLower(message), "retry later") {
		t.Fatalf("circuit-break progress message = %q", message)
	}
}

func TestEbookMetadataBackfillUsesLegacyScope(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{results: []ebooks.EnrichmentRunResult{{Remaining: 0}}}
	task := NewBackfillEbookMetadataTask(enricher)
	if err := task.Execute(context.Background(), &ebookMetadataProgressReporter{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(enricher.scopes) != 1 || enricher.scopes[0] != ebooks.EnrichmentScopeLegacy {
		t.Fatalf("backfill scopes = %#v, want legacy", enricher.scopes)
	}
}

func TestEbookMetadataTaskStopsBetweenBatchesOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	enricher := &fakeEbookMetadataEnricher{
		results: []ebooks.EnrichmentRunResult{{Claimed: 1, Enriched: 1, Remaining: 2}},
		onRun: func(int) {
			cancel()
		},
	}
	err := NewSyncEbookMetadataTask(enricher).Execute(ctx, &ebookMetadataProgressReporter{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
	if len(enricher.scopes) != 1 {
		t.Fatalf("Run calls = %d, want 1", len(enricher.scopes))
	}
}

func TestEbookMetadataTaskTreatsBudgetAsCleanDeferredCompletion(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	enricher := &fakeEbookMetadataEnricher{
		results: []ebooks.EnrichmentRunResult{{Claimed: 2, Enriched: 2, Remaining: 5}},
		onRun: func(int) {
			now = now.Add(5 * time.Minute)
		},
	}
	task := NewSyncEbookMetadataTask(enricher)
	task.now = func() time.Time { return now }
	task.budget = 4 * time.Minute
	progress := &ebookMetadataProgressReporter{}

	if err := task.Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(enricher.scopes) != 1 {
		t.Fatalf("Run calls = %d, want 1", len(enricher.scopes))
	}
	var result ebooks.EnrichmentRunResult
	if err := json.Unmarshal(progress.results[len(progress.results)-1], &result); err != nil {
		t.Fatalf("result JSON error: %v", err)
	}
	if result.Remaining != 5 || result.Deferred != 0 {
		t.Fatalf("budget result = %+v, want remaining=5 without double-counting it as deferred", result)
	}
	if got := progress.percents[len(progress.percents)-1]; got >= 100 {
		t.Fatalf("budget-limited progress = %.1f, must remain below 100", got)
	}
}

func TestEbookMetadataTaskWrapsRunError(t *testing.T) {
	enricher := &fakeEbookMetadataEnricher{err: errors.New("boom")}
	err := NewSyncEbookMetadataTask(enricher).Execute(context.Background(), &ebookMetadataProgressReporter{})
	if err == nil || !strings.Contains(err.Error(), "ebook metadata sync") {
		t.Fatalf("Execute() error = %v, want wrapped ebook metadata sync error", err)
	}
}
