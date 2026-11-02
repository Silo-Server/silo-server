package recommendations

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// qualityPool returns n candidates of mediaType, scored 0.80, 0.79, …, with
// ratings from rating(i); a rating of 0 leaves the candidate unrated.
func qualityPool(prefix, mediaType string, n int, rating func(int) float64, ratings map[string]float64, types map[string]string) []ScoredItem {
	items := make([]ScoredItem, n)
	for i := range items {
		id := fmt.Sprintf("%s%02d", prefix, i)
		items[i] = ScoredItem{MediaItemID: id, Score: 0.8 - float64(i)/100}
		types[id] = mediaType
		if r := rating(i); r > 0 {
			ratings[id] = r
		}
	}
	return items
}

func scoreSD(items []ScoredItem) float64 {
	scores := make([]float64, len(items))
	for i, item := range items {
		scores[i] = item.Score
	}
	_, sd := meanSD(scores)
	return sd
}

// The nudge is at most qualityPriorWeight standard deviations of the pool's
// scores, a missing rating is neutral, and the candidates keep their order.
func TestApplyQualityPriorIsBoundedAndNeutralWithoutARating(t *testing.T) {
	ratings, types := map[string]float64{}, map[string]string{}
	pool := qualityPool("m", "movie", 20, func(i int) float64 { return 5 + float64(i%5) }, ratings, types)
	ratings["m00"] = 100 // far outside the pool: clamped to z = 1
	delete(ratings, "m01")

	got := applyQualityPrior(pool, ratings, types)

	if !slices.Equal(mmrItemIDs(got), mmrItemIDs(pool)) {
		t.Fatalf("order = %v, want unchanged", mmrItemIDs(got))
	}
	bound := qualityPriorWeight * scoreSD(pool)
	for i := range got {
		if shift := got[i].Score - pool[i].Score; math.Abs(shift) > bound+1e-12 {
			t.Fatalf("%s moved %f, beyond the bound %f", got[i].MediaItemID, shift, bound)
		}
	}
	if shift := got[0].Score - pool[0].Score; math.Abs(shift-bound) > 1e-12 {
		t.Fatalf("outlier moved %f, want the full bound %f", shift, bound)
	}
	if got[1].Score != pool[1].Score {
		t.Fatalf("unrated candidate moved from %f to %f", pool[1].Score, got[1].Score)
	}
}

// Ratings are compared within a media type, so series, which rate higher,
// gain nothing on movies as a group.
func TestApplyQualityPriorStandardizesPerMediaType(t *testing.T) {
	ratings, types := map[string]float64{}, map[string]string{}
	movies := qualityPool("m", "movie", 20, func(i int) float64 { return 5.5 + float64(i%4)/2 }, ratings, types)
	series := qualityPool("s", "series", 20, func(i int) float64 { return 7.5 + float64(i%4)/2 }, ratings, types)
	pool := append(slices.Clone(movies), series...)

	got := applyQualityPrior(pool, ratings, types)

	var movieShift, seriesShift float64
	for i := range got {
		shift := got[i].Score - pool[i].Score
		if types[got[i].MediaItemID] == "series" {
			seriesShift += shift
		} else {
			movieShift += shift
		}
	}
	if math.Abs(movieShift) > 1e-9 || math.Abs(seriesShift) > 1e-9 {
		t.Fatalf("total shift movies %f, series %f; want each type to net zero", movieShift, seriesShift)
	}
}

// A type with too few rated candidates or no spread in ratings, and a pool
// with no spread in scores, are left alone.
func TestApplyQualityPriorSkipsThinOrFlatStatistics(t *testing.T) {
	for name, build := range map[string]func(map[string]float64, map[string]string) []ScoredItem{
		"few rated": func(r map[string]float64, ty map[string]string) []ScoredItem {
			return qualityPool("m", "movie", 30, func(i int) float64 {
				if i < qualityMinRated-1 {
					return float64(4 + i)
				}
				return 0
			}, r, ty)
		},
		"equal ratings": func(r map[string]float64, ty map[string]string) []ScoredItem {
			return qualityPool("m", "movie", 30, func(int) float64 { return 7 }, r, ty)
		},
		"equal scores": func(r map[string]float64, ty map[string]string) []ScoredItem {
			pool := qualityPool("m", "movie", 30, func(i int) float64 { return float64(3 + i%6) }, r, ty)
			for i := range pool {
				pool[i].Score = 0.5
			}
			return pool
		},
	} {
		ratings, types := map[string]float64{}, map[string]string{}
		pool := build(ratings, types)
		got := applyQualityPrior(pool, ratings, types)
		for i := range got {
			if got[i].Score != pool[i].Score {
				t.Fatalf("%s: %s moved from %f to %f", name, got[i].MediaItemID, pool[i].Score, got[i].Score)
			}
		}
	}
}
