package manga

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClaimBatchQueryTargetsManga(t *testing.T) {
	if !strings.Contains(claimBatchQuery, "mi.type = 'manga'") {
		t.Fatalf("claimBatchQuery must filter type='manga'")
	}
	if strings.Contains(claimBatchQuery, "'ebook'") {
		t.Fatalf("claimBatchQuery must not reference ebook")
	}
	if !strings.Contains(claimBatchQuery, "manga_enrichment_state") {
		t.Fatalf("claimBatchQuery must join manga_enrichment_state")
	}
	// Secondary-fields arm: enriched items missing a backdrop or publication
	// status are claimed too; has_poster/has_backdrop distinguish them so only
	// the missing secondary fields are written.
	if !strings.Contains(claimBatchQuery, "mi.backdrop_path IS NULL OR mi.backdrop_path = ''") {
		t.Fatalf("claimBatchQuery must claim backdrop-missing items")
	}
	if !strings.Contains(claimBatchQuery, "mi.show_status IS NULL OR mi.show_status = ''") {
		t.Fatalf("claimBatchQuery must claim status-missing items")
	}
	if !strings.Contains(claimBatchQuery, "AS has_poster") {
		t.Fatalf("claimBatchQuery must project has_poster")
	}
	if !strings.Contains(claimBatchQuery, "AS has_backdrop") {
		t.Fatalf("claimBatchQuery must project has_backdrop")
	}
}

func TestContentTypeIsManga(t *testing.T) {
	if got := mangaContentType(); got != "manga" {
		t.Fatalf("mangaContentType() = %q, want %q", got, "manga")
	}
}

// runBatch must keep the three terminal outcomes apart: a stamped no-match is
// neither an enrichment (the old behavior overcounted it as one) nor a
// failure, and only real failures reach recordFailure.
func TestRunBatchSeparatesOutcomes(t *testing.T) {
	e := &Enricher{workers: 2}
	items := []enrichmentItemRow{
		{ContentID: "enriched-1"},
		{ContentID: "enriched-2"},
		{ContentID: "no-match"},
		{ContentID: "skipped"},
		{ContentID: "failed"},
	}

	var failures int64
	stats := e.runBatch(context.Background(), items,
		func(_ context.Context, item enrichmentItemRow) error {
			switch item.ContentID {
			case "no-match":
				return errEnrichmentNoMatch
			case "skipped":
				return errEnrichmentSkipped
			case "failed":
				return errors.New("provider exploded")
			default:
				return nil
			}
		},
		func(context.Context, enrichmentItemRow) {
			atomic.AddInt64(&failures, 1)
		},
	)

	if stats.enriched != 2 {
		t.Fatalf("enriched = %d, want 2", stats.enriched)
	}
	if stats.noMatch != 1 {
		t.Fatalf("noMatch = %d, want 1", stats.noMatch)
	}
	if stats.failed != 1 {
		t.Fatalf("failed = %d, want 1", stats.failed)
	}
	if failures != 1 {
		t.Fatalf("recordFailure calls = %d, want 1", failures)
	}
}
