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
