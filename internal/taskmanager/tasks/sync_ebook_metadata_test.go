package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/ebooks"
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
