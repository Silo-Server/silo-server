package recommendations

import (
	"context"
	"log/slog"
	"math"
)

// The quality prior lets a title's public rating break near-ties in
// similarity, so a well-rated title ranks ahead of a poorly rated plot
// neighbor. It is scale-free: the nudge is measured in the pool's own spread
// of scores, which varies several-fold between embedding models, and ratings
// are compared only within one media type, because series rate higher than
// movies.
const (
	// qualityPriorWeight is the largest nudge, in standard deviations of the
	// pool's scores. Raising it would start to push a profile away from
	// genres that rate low on average.
	qualityPriorWeight = 0.5
	// qualityMinRated is the fewest rated candidates of a media type the
	// prior needs to standardize that type's ratings.
	qualityMinRated = 10
	// qualityMinSmallPool is the smallest cluster or Because You Watched pool
	// the prior applies to; smaller pools give noisy statistics.
	qualityMinSmallPool = 30
)

// applyQualityPrior sets each candidate's Score to
//
//	score + qualityPriorWeight × sd(scores) × clamp(z, −1, 1)
//
// where z is the candidate's rating standardized among the pool's rated
// candidates of the same media type. A candidate without a rating, or of a
// type with fewer than qualityMinRated rated candidates or no spread in
// ratings, keeps its score. Score is then a relevance score, not a raw
// cosine. The candidates keep their order.
func applyQualityPrior(candidates []ScoredItem, ratings map[string]float64, mediaTypes map[string]string) []ScoredItem {
	if len(candidates) == 0 || len(ratings) == 0 {
		return candidates
	}
	scores := make([]float64, len(candidates))
	byType := make(map[string][]float64)
	for i, c := range candidates {
		scores[i] = c.Score
		if rating, ok := ratings[c.MediaItemID]; ok {
			mediaType := mediaTypes[c.MediaItemID]
			byType[mediaType] = append(byType[mediaType], rating)
		}
	}
	_, scoreSD := meanSD(scores)
	if scoreSD == 0 {
		return candidates
	}
	type stats struct{ mean, sd float64 }
	typeStats := make(map[string]stats, len(byType))
	for mediaType, values := range byType {
		if len(values) < qualityMinRated {
			continue
		}
		if mean, sd := meanSD(values); sd > 0 {
			typeStats[mediaType] = stats{mean, sd}
		}
	}

	out := make([]ScoredItem, len(candidates))
	for i, c := range candidates {
		out[i] = c
		rating, rated := ratings[c.MediaItemID]
		s, ok := typeStats[mediaTypes[c.MediaItemID]]
		if !rated || !ok {
			continue
		}
		z := max(-1, min(1, (rating-s.mean)/s.sd))
		out[i].Score += qualityPriorWeight * scoreSD * z
	}
	return out
}

// meanSD returns the mean and population standard deviation of values.
func meanSD(values []float64) (mean, sd float64) {
	if len(values) == 0 {
		return 0, 0
	}
	for _, v := range values {
		mean += v
	}
	mean /= float64(len(values))
	for _, v := range values {
		sd += (v - mean) * (v - mean)
	}
	return mean, math.Sqrt(sd / float64(len(values)))
}

// withQualityPrior applies applyQualityPrior to pool when it holds at least
// minPool candidates, reading the ratings and media types it needs. A pool
// whose reads fail keeps its scores.
func (b rowBuilder) withQualityPrior(ctx context.Context, pool []ScoredItem, minPool int) []ScoredItem {
	if len(pool) == 0 || len(pool) < minPool {
		return pool
	}
	ids := scoredItemIDs(pool)
	ratings, err := b.store.GetItemQualityRatings(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "quality ratings failed; candidates keep their similarity scores", "component", "recommendations", "error", err)
		return pool
	}
	mediaTypes, err := b.store.GetItemMediaTypes(ctx, ids)
	if err != nil {
		slog.WarnContext(ctx, "candidate media types failed; candidates keep their similarity scores", "component", "recommendations", "error", err)
		return pool
	}
	return applyQualityPrior(pool, ratings, mediaTypes)
}
