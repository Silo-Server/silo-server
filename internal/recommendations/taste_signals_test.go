package recommendations

import (
	"fmt"
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

	abandonedAt := now.Add(-30 * 24 * time.Hour)
	progress := []WatchProgressRow{
		// Mid-read book (60%) mirrors a movie watched to 60%.
		{MediaItemID: "ebook-mid", PositionSeconds: 0.6, DurationSeconds: 1, UpdatedAt: now},
		// Abandoned book (<15%, untouched for a month) mirrors an abandoned
		// movie: negative signal.
		{MediaItemID: "ebook-abandoned", PositionSeconds: 0.05, DurationSeconds: 1, UpdatedAt: abandonedAt},
		// A book opened today is not abandoned yet.
		{MediaItemID: "ebook-started", PositionSeconds: 0.05, DurationSeconds: 1, UpdatedAt: now},
	}
	refs := map[string]canonicalContentRef{
		"ebook-mid":       {Kind: canonicalKindEbook, CanonicalID: "ebook-mid"},
		"ebook-abandoned": {Kind: canonicalKindEbook, CanonicalID: "ebook-abandoned"},
		"ebook-started":   {Kind: canonicalKindEbook, CanonicalID: "ebook-started"},
	}

	signals, completed := buildCanonicalImplicitSignals(progress, nil, refs, now, halfLife)

	if got := signals["ebook-mid"]; math.Abs(got-WeightWatchMed) > 1e-9 {
		t.Fatalf("mid-read ebook weight = %v, want WeightWatchMed (%v)", got, WeightWatchMed)
	}
	if got, want := signals["ebook-abandoned"], WeightWatchLow*timeDecay(abandonedAt, now, halfLife); math.Abs(got-want) > 1e-9 {
		t.Fatalf("abandoned ebook weight = %v, want decayed WeightWatchLow (%v)", got, want)
	}
	if got, ok := signals["ebook-started"]; ok {
		t.Fatalf("ebook opened today has weight %v, want no signal", got)
	}
	if len(completed) != 0 {
		t.Fatalf("no row was completed, got completed set %v", completed)
	}
}

// A title left below 15% counts as abandoned only once it has sat untouched
// for abandonedProgressAge; before that it carries no weight either way.
func TestImplicitWatchWeightLowProgressCountsOnceStale(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name       string
		row        WatchProgressRow
		wantWeight float64
		wantOK     bool
	}{
		{"completed", WatchProgressRow{Completed: true, UpdatedAt: now.Add(-400 * day)}, WeightWatchHigh, true},
		{"nearly finished", WatchProgressRow{PositionSeconds: 95, DurationSeconds: 100, UpdatedAt: now}, WeightWatchHigh, true},
		{"half watched", WatchProgressRow{PositionSeconds: 60, DurationSeconds: 100, UpdatedAt: now}, WeightWatchMed, true},
		{"middle band", WatchProgressRow{PositionSeconds: 30, DurationSeconds: 100, UpdatedAt: now.Add(-100 * day)}, 0, false},
		{"paused early tonight", WatchProgressRow{PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: now.Add(-2 * time.Hour)}, 0, false},
		{"paused early 13 days ago", WatchProgressRow{PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: now.Add(-13 * day)}, 0, false},
		{"at the window", WatchProgressRow{PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: now.Add(-abandonedProgressAge)}, WeightWatchLow, true},
		{"abandoned 15 days ago", WatchProgressRow{PositionSeconds: 10, DurationSeconds: 100, UpdatedAt: now.Add(-15 * day)}, WeightWatchLow, true},
		{"low progress without a timestamp", WatchProgressRow{PositionSeconds: 10, DurationSeconds: 100}, 0, false},
		{"no duration", WatchProgressRow{PositionSeconds: 10, UpdatedAt: now.Add(-100 * day)}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			weight, ok := implicitWatchWeight(tc.row, now)
			if weight != tc.wantWeight || ok != tc.wantOK {
				t.Fatalf("weight = %v, %v; want %v, %v", weight, ok, tc.wantWeight, tc.wantOK)
			}
		})
	}
}

// A series decays once, by its most recent season signal, the way a movie
// decays by its last watch. The season shares still weight the mean.
func TestAggregateSeriesImplicitScoreDecaysByLatestSignal(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	const halfLife = 180.0
	daysAgo := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }

	t.Run("binge from 700 days ago", func(t *testing.T) {
		got := aggregateSeriesImplicitScore([]seasonAggregate{
			{Score: WeightWatchHigh, SampleCount: 10, LastSignalAt: daysAgo(700)},
		}, now, halfLife)
		if got >= 0.1 {
			t.Fatalf("score = %v, want below 0.1", got)
		}
		if movie := WeightWatchHigh * timeDecay(daysAgo(700), now, halfLife); math.Abs(got-movie) > 1e-9 {
			t.Fatalf("score = %v, want %v, as a movie finished the same day", got, movie)
		}
	})

	t.Run("old seasons and one watched this week", func(t *testing.T) {
		seasons := []seasonAggregate{
			{Score: WeightWatchHigh, SampleCount: 10, LastSignalAt: daysAgo(700)},
			{Score: WeightWatchHigh, SampleCount: 10, LastSignalAt: daysAgo(500)},
			{Score: WeightWatchHigh, SampleCount: 8, LastSignalAt: daysAgo(5)},
		}
		got := aggregateSeriesImplicitScore(seasons, now, halfLife)
		if want := WeightWatchHigh * timeDecay(daysAgo(5), now, halfLife); math.Abs(got-want) > 1e-9 {
			t.Fatalf("score = %v, want %v: the mean decayed once by the latest season", got, want)
		}
		if got < 0.75 {
			t.Fatalf("score = %v, want near the 0.8 mean for a series watched this week", got)
		}
	})

	t.Run("mixed seasons keep their share weighting", func(t *testing.T) {
		seasons := []seasonAggregate{
			{Score: WeightWatchHigh, SampleCount: 10, LastSignalAt: daysAgo(3)},
			{Score: WeightWatchMed, SampleCount: 10, LastSignalAt: daysAgo(3)},
		}
		got := aggregateSeriesImplicitScore(seasons, now, halfLife)
		mean := (WeightWatchHigh + WeightWatchMed) / 2
		if want := mean * timeDecay(daysAgo(3), now, halfLife); math.Abs(got-want) > 1e-9 {
			t.Fatalf("score = %v, want %v", got, want)
		}
	})

	t.Run("no timestamp counts as now", func(t *testing.T) {
		got := aggregateSeriesImplicitScore([]seasonAggregate{{Score: WeightWatchHigh, SampleCount: 3}}, now, halfLife)
		if math.Abs(got-WeightWatchHigh) > 1e-9 {
			t.Fatalf("score = %v, want undecayed %v", got, WeightWatchHigh)
		}
	})
}

// Through the whole signal build, a series finished long ago weighs what a
// movie finished the same day weighs.
func TestBuildCanonicalImplicitSignalsDecaysOldSeriesLikeMovies(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	const halfLife = 180.0
	watched := now.Add(-700 * 24 * time.Hour)
	refs := map[string]canonicalContentRef{
		"movie": {Kind: canonicalKindMovie, CanonicalID: "movie"},
	}
	progress := []WatchProgressRow{{MediaItemID: "movie", Completed: true, UpdatedAt: watched}}
	for i := range 6 {
		id := fmt.Sprintf("episode-%d", i)
		refs[id] = canonicalContentRef{Kind: canonicalKindEpisode, CanonicalID: "series", SeriesID: "series", SeasonNumber: 1, HasSeason: true}
		progress = append(progress, WatchProgressRow{MediaItemID: id, Completed: true, UpdatedAt: watched})
	}

	signals, _ := buildCanonicalImplicitSignals(progress, nil, refs, now, halfLife)
	if math.Abs(signals["series"]-signals["movie"]) > 1e-9 || signals["series"] >= 0.1 {
		t.Fatalf("series weight = %v, movie weight = %v; want equal and below 0.1", signals["series"], signals["movie"])
	}
}
