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

// The scanner's manga_series identity rows must never reach the metadata
// flow: they made the search-skip guard treat every item as already matched.
func TestFilterMangaProviderIDsDropsInternalIdentity(t *testing.T) {
	filtered := filterMangaProviderIDs(map[string]string{
		"manga_series": "abc123",
		"anilist":      "42",
		"asin":         "B000",
	})
	if _, ok := filtered["manga_series"]; ok {
		t.Fatalf("manga_series identity must be filtered, got %v", filtered)
	}
	if filtered["anilist"] != "42" {
		t.Fatalf("anilist id must survive, got %v", filtered)
	}
	if len(filtered) != 1 {
		t.Fatalf("filtered = %v, want only anilist", filtered)
	}
}

func TestNormalizeMangaStatus(t *testing.T) {
	cases := map[string]string{
		// AniList enum
		"RELEASING":        "Ongoing",
		"FINISHED":         "Completed",
		"NOT_YET_RELEASED": "Upcoming",
		"CANCELLED":        "Cancelled",
		"HIATUS":           "Hiatus",
		// MangaDex / lowercase
		"ongoing":   "Ongoing",
		"completed": "Completed",
		"hiatus":    "Hiatus",
		"cancelled": "Cancelled",
		// SDK Continuing/Ended + spacing/casing variants
		"Continuing":  "Ongoing",
		"Ended":       "Completed",
		"on hiatus":   "Hiatus",
		"  Upcoming ": "Upcoming",
		// Empty and unknown pass through (trimmed)
		"":          "",
		"  ":        "",
		"Weird-Val": "Weird-Val",
	}
	for in, want := range cases {
		if got := normalizeMangaStatus(in); got != want {
			t.Fatalf("normalizeMangaStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
