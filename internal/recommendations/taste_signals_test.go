package recommendations

import (
	"math"
	"testing"
	"time"
)

func TestBuildCanonicalImplicitSignalsIncludesEbookCompletion(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	halfLife := 90.0

	// Ebook reader progress rows map progress onto position with duration 1,
	// so a finished book carries the same ratio shape as a finished movie.
	progress := []WatchProgressRow{
		{MediaItemID: "ebook-1", PositionSeconds: 0.95, DurationSeconds: 1, Completed: true, UpdatedAt: now},
		{MediaItemID: "movie-1", PositionSeconds: 5400, DurationSeconds: 5700, Completed: true, UpdatedAt: now},
	}
	refs := map[string]canonicalContentRef{
		"ebook-1": {Kind: canonicalKindEbook, CanonicalID: "ebook-1"},
		"movie-1": {Kind: canonicalKindMovie, CanonicalID: "movie-1"},
	}

	signals, completed := buildCanonicalImplicitSignals(progress, nil, refs, now, halfLife)

	ebookWeight, ok := signals["ebook-1"]
	if !ok {
		t.Fatal("expected ebook completion to produce a canonical implicit signal")
	}
	movieWeight, ok := signals["movie-1"]
	if !ok {
		t.Fatal("expected movie completion to produce a canonical implicit signal")
	}
	if math.Abs(ebookWeight-movieWeight) > 1e-9 {
		t.Fatalf("ebook completion weight %v should match movie completion weight %v", ebookWeight, movieWeight)
	}
	if math.Abs(ebookWeight-WeightWatchHigh) > 1e-9 {
		t.Fatalf("ebook completion weight = %v, want WeightWatchHigh (%v) with no decay at t=now", ebookWeight, WeightWatchHigh)
	}
	if _, ok := completed["ebook-1"]; !ok {
		t.Fatal("expected completed ebook to enter the completed set")
	}
}

func TestBuildCanonicalImplicitSignalsWeightsPartialEbookLikeMovie(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	halfLife := 90.0

	progress := []WatchProgressRow{
		// Mid-read book (60%) mirrors a movie watched to 60%.
		{MediaItemID: "ebook-mid", PositionSeconds: 0.6, DurationSeconds: 1, UpdatedAt: now},
		// Abandoned book (<15%) mirrors an abandoned movie: negative signal.
		{MediaItemID: "ebook-abandoned", PositionSeconds: 0.05, DurationSeconds: 1, UpdatedAt: now},
	}
	refs := map[string]canonicalContentRef{
		"ebook-mid":       {Kind: canonicalKindEbook, CanonicalID: "ebook-mid"},
		"ebook-abandoned": {Kind: canonicalKindEbook, CanonicalID: "ebook-abandoned"},
	}

	signals, completed := buildCanonicalImplicitSignals(progress, nil, refs, now, halfLife)

	if got := signals["ebook-mid"]; math.Abs(got-WeightWatchMed) > 1e-9 {
		t.Fatalf("mid-read ebook weight = %v, want WeightWatchMed (%v)", got, WeightWatchMed)
	}
	if got := signals["ebook-abandoned"]; math.Abs(got-WeightWatchLow) > 1e-9 {
		t.Fatalf("abandoned ebook weight = %v, want WeightWatchLow (%v)", got, WeightWatchLow)
	}
	if len(completed) != 0 {
		t.Fatalf("no row was completed, got completed set %v", completed)
	}
}
