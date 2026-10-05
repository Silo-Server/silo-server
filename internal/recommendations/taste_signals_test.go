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
		}, 10, now, halfLife)
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
		got := aggregateSeriesImplicitScore(seasons, 28, now, halfLife)
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
		got := aggregateSeriesImplicitScore(seasons, 20, now, halfLife)
		mean := (WeightWatchHigh + WeightWatchMed) / 2
		if want := mean * timeDecay(daysAgo(3), now, halfLife); math.Abs(got-want) > 1e-9 {
			t.Fatalf("score = %v, want %v", got, want)
		}
	})

	t.Run("no timestamp counts as now", func(t *testing.T) {
		got := aggregateSeriesImplicitScore([]seasonAggregate{{Score: WeightWatchHigh, SampleCount: 3}}, 3, now, halfLife)
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

// A 3-star rating is half credit: it halves what watching and intent say
// and adds nothing of its own, so a 3-star completion weighs less than an
// unrated one instead of more.
func TestRatedThreeIsHalfCredit(t *testing.T) {
	weight := func(rating *int, implicit, intent float64) float64 {
		explicit := 0.0
		if rating != nil {
			w, _ := ratingSignal(*rating)
			explicit = w
		}
		return combineCanonicalWeight(rating, explicit, implicit, intent)
	}
	stars := func(n int) *int { return &n }

	if w, kind := ratingSignal(3); w != 0 || kind != "rated_3" {
		t.Fatalf("ratingSignal(3) = %v, %q; want no weight, still counted as rated_3", w, kind)
	}
	unrated := weight(nil, WeightWatchHigh, 0)
	meh := weight(stars(3), WeightWatchHigh, 0)
	if meh >= unrated || math.Abs(meh-0.5*WeightWatchHigh) > 1e-9 {
		t.Fatalf("3-star completion = %v, unrated completion = %v; want half of it", meh, unrated)
	}
	if liked := weight(stars(4), WeightWatchHigh, 0); liked <= unrated {
		t.Fatalf("4-star completion = %v, want above the unrated %v", liked, unrated)
	}
	if fav := weight(stars(3), WeightWatchHigh, WeightFavorited); fav >= weight(nil, WeightWatchHigh, WeightFavorited) {
		t.Fatalf("3-star favorite completion = %v, want below the unrated favorite", fav)
	}
	// A 3-star rating alone carries no weight, and one on an abandoned title
	// stays negative, so neither shapes the taste vector.
	if w := weight(stars(3), 0, 0); w != 0 {
		t.Fatalf("3-star rating alone = %v, want 0", w)
	}
	if w := weight(stars(3), WeightWatchLow, 0); w >= 0 {
		t.Fatalf("3-star abandoned title = %v, want negative", w)
	}
}

// A series that has gone quiet counts by how many of its episodes were
// watched with a positive weight; one being watched now, and a dislike, are
// not scaled.
func TestBuildCanonicalImplicitSignalsScalesQuietSeriesByDepth(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	const halfLife = 180.0
	daysAgo := func(d int) time.Time { return now.Add(-time.Duration(d) * 24 * time.Hour) }
	completed := func(at time.Time) WatchProgressRow { return WatchProgressRow{Completed: true, UpdatedAt: at} }
	stopped := func(at time.Time) WatchProgressRow {
		return WatchProgressRow{PositionSeconds: 5, DurationSeconds: 100, UpdatedAt: at}
	}
	repeat := func(n int, row WatchProgressRow) []WatchProgressRow {
		rows := make([]WatchProgressRow, n)
		for i := range rows {
			rows[i] = row
		}
		return rows
	}
	seriesWeight := func(t *testing.T, kind canonicalContentKind, episodes []WatchProgressRow) float64 {
		t.Helper()
		refs := map[string]canonicalContentRef{}
		progress := make([]WatchProgressRow, len(episodes))
		for i, row := range episodes {
			id := fmt.Sprintf("episode-%d", i)
			refs[id] = canonicalContentRef{Kind: kind, CanonicalID: "series", SeriesID: "series", SeasonNumber: 1 + i/10, HasSeason: true}
			row.MediaItemID = id
			progress[i] = row
		}
		signals, _ := buildCanonicalImplicitSignals(progress, nil, refs, now, halfLife)
		return signals["series"]
	}
	movie := func(at time.Time) float64 { return WeightWatchHigh * timeDecay(at, now, halfLife) }

	for _, tc := range []struct {
		name     string
		kind     canonicalContentKind
		episodes []WatchProgressRow
		want     float64
	}{
		{"pilot watched last night", canonicalKindEpisode, []WatchProgressRow{completed(daysAgo(1))}, movie(daysAgo(1))},
		{"three episodes this week", canonicalKindEpisode, repeat(3, completed(daysAgo(2))), movie(daysAgo(2))},
		{"quiet for exactly the window", canonicalKindEpisode, []WatchProgressRow{completed(daysAgo(30))}, movie(daysAgo(30))},
		{"pilot only, quiet for 60 days", canonicalKindEpisode, []WatchProgressRow{completed(daysAgo(60))}, seriesDepthFloor * movie(daysAgo(60))},
		{"two episodes, quiet for 40 days", canonicalKindEpisode, repeat(2, completed(daysAgo(40))), 0.4 * movie(daysAgo(40))},
		{"miniseries of six finished 90 days ago", canonicalKindEpisode, repeat(6, completed(daysAgo(90))), movie(daysAgo(90))},
		{"binge of 30 episodes 700 days ago", canonicalKindEpisode, repeat(30, completed(daysAgo(700))), movie(daysAgo(700))},
		{
			"dropped at episode 2, 45 days ago", canonicalKindEpisode,
			[]WatchProgressRow{completed(daysAgo(60)), stopped(daysAgo(45))},
			seriesDepthFloor * (WeightWatchHigh + WeightWatchLow) / 2 * timeDecay(daysAgo(45), now, halfLife),
		},
		{"every episode abandoned keeps its dislike", canonicalKindEpisode, repeat(2, stopped(daysAgo(60))), WeightWatchLow * timeDecay(daysAgo(60), now, halfLife)},
		{"season rows are not episodes", canonicalKindSeason, repeat(6, completed(daysAgo(60))), seriesDepthFloor * movie(daysAgo(60))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := seriesWeight(t, tc.kind, tc.episodes); math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("series weight = %v, want %v", got, tc.want)
			}
		})
	}

	// A series dropped after the pilot and a stopped second episode used to
	// weigh 0.3 decayed, a mild like; it now weighs a quarter of that.
	if dropped := seriesWeight(t, canonicalKindEpisode, []WatchProgressRow{completed(daysAgo(60)), stopped(daysAgo(45))}); dropped >= 0.1 {
		t.Fatalf("dropped series weight = %v, want far below 0.3", dropped)
	}
}

// Several favorited episodes of one series weigh as one favorite of the
// series, at the newest one's decay; entries with no title are dropped.
func TestNewestDecayByTitleCountsATitleOnce(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	type entry struct {
		id string
		at time.Time
	}
	refs := map[string]canonicalContentRef{
		"ep-1":  {Kind: canonicalKindSeries, CanonicalID: "series"},
		"ep-2":  {Kind: canonicalKindSeries, CanonicalID: "series"},
		"movie": {Kind: canonicalKindMovie, CanonicalID: "movie"},
	}
	entries := []entry{
		{"ep-1", now.AddDate(0, 0, -360)},
		{"ep-2", now.AddDate(0, 0, -180)},
		{"movie", now},
		{"unknown", now},
	}
	decays := newestDecayByTitle(entries, refs, func(e entry) (string, time.Time) { return e.id, e.at }, now, 180)
	if len(decays) != 2 {
		t.Fatalf("decays = %v, want one per title", decays)
	}
	if got, want := decays["series"], timeDecay(now.AddDate(0, 0, -180), now, 180); math.Abs(got-want) > 1e-9 {
		t.Fatalf("series decay = %v, want the newest episode's %v", got, want)
	}
	if got := decays["movie"]; math.Abs(got-1) > 1e-9 {
		t.Fatalf("movie decay = %v, want 1", got)
	}
}
