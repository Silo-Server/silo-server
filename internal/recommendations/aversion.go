package recommendations

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Titles a profile turned away, by marking them Not interested or rating them
// 1–2 stars, teach its rows without entering the taste vector (see
// positiveTasteItems). They act on two levels, each measured against the row
// being built, so the rules hold whatever range of cosines an embedding model
// gives:
//
//   - A turned-away title at the heart of the row, as near the row's query
//     as its median candidate, pushes down the candidates that sit nearest
//     it of all the row's: a sequel or near copy goes far down, its
//     genre-mates barely move. Several such titles add up, so a run of marks
//     in one area reaches the area.
//   - Each such title counts against the row's interest the way the
//     interest's own titles count for it: enough of them shrink the
//     interest's share of the main row, then drop its row.
const (
	// aversionWeightMark and aversionWeightRatedLow are how strongly a fresh
	// mark and a fresh 1–2 star rating count.
	aversionWeightMark     = 1.0
	aversionWeightRatedLow = 0.6
	// aversionMarkHalfLifeDays is how fast a mark's push fades. The marked
	// title itself stays out of every row however old the mark is.
	aversionMarkHalfLifeDays = 365
	// aversionReachQuantile and aversionReachSD bound the push: only the
	// candidates nearer a turned-away title than that share of the row's
	// candidates are pushed, and one aversionReachSD standard deviations of
	// the row's similarities to the title nearer still gets the title's whole
	// weight. Measuring within the row keeps a minority genre in a mixed row
	// from being pushed as a block.
	aversionReachQuantile = 0.9
	aversionReachSD       = 2.0
	// aversionPenaltySD is the largest push, in standard deviations of the
	// pool's scores. It is several times qualityPriorWeight: the profile
	// said so itself.
	aversionPenaltySD = 2.0
	// aversionInterestTitles is how many fully weighted titles of an
	// interest one fresh mark at its heart outweighs; aversionInterestFloor
	// is the share of its weight under which the interest's own row is
	// dropped.
	aversionInterestTitles = 4.0
	aversionInterestFloor  = 0.3
	// aversionMaxTitles is how many turned-away titles, newest first, a
	// build reads.
	aversionMaxTitles = 200
)

// aversionTitle is one turned-away title: its embedding and its weight now.
type aversionTitle struct {
	embedding []float32
	weight    float64
}

// aversion is the turned-away titles one row build ranks against. A nil
// aversion, or one with no titles, changes nothing.
type aversion struct {
	titles []aversionTitle
}

func (a *aversion) empty() bool {
	return a == nil || len(a.titles) == 0
}

// size is how many turned-away titles the aversion holds.
func (a *aversion) size() int {
	if a == nil {
		return 0
	}
	return len(a.titles)
}

// aversionResult is what applying an aversion to one pool found.
type aversionResult struct {
	// penalized is how many candidates were pushed down.
	penalized int
	// central is the summed weight of the turned-away titles at the heart
	// of the row's interest.
	central float64
}

// interestShare is the share of an interest of the given weight left after
// res's turned-away titles at its heart: 1 when there are none.
func (res aversionResult) interestShare(weight float64) float64 {
	if res.central <= 0 {
		return 1
	}
	if weight <= 0 {
		return 0
	}
	return weight / (weight + aversionInterestTitles*res.central)
}

// apply lowers each candidate's Score by its aversion and returns the pool,
// in its order, with what it found. query is what the pool was fetched
// around: a taste vector, cluster centroid or source title. A pool without a
// query, or whose candidates lack embeddings, is returned unchanged.
func (a *aversion) apply(pool []ScoredItem, embeddings map[string][]float32, query []float32) ([]ScoredItem, aversionResult) {
	var res aversionResult
	if a.empty() || len(pool) == 0 || len(query) == 0 {
		return pool, res
	}
	type candidate struct {
		index int
		emb   []float32
	}
	candidates := make([]candidate, 0, len(pool))
	querySims := make([]float64, 0, len(pool))
	for i, item := range pool {
		emb := embeddings[item.MediaItemID]
		if emb == nil {
			continue
		}
		candidates = append(candidates, candidate{i, emb})
		querySims = append(querySims, cosineSimilarity(query, emb))
	}
	if len(candidates) < 2 {
		return pool, res
	}
	median := quantile(querySims, 0.5)

	// keep[i] is 1 minus the aversion of candidates[i] so far.
	keep := make([]float64, len(candidates))
	for i := range keep {
		keep[i] = 1
	}
	sims := make([]float64, len(candidates))
	for _, title := range a.titles {
		// A title less near the row's query than its median candidate is
		// not one the row is about, and says nothing about it.
		if cosineSimilarity(title.embedding, query) < median {
			continue
		}
		res.central += title.weight
		for i, c := range candidates {
			sims[i] = cosineSimilarity(c.emb, title.embedding)
		}
		_, sd := meanSD(sims)
		if sd == 0 {
			continue
		}
		start := quantile(sims, aversionReachQuantile)
		for i := range candidates {
			reach := min(1, max(0, (sims[i]-start)/(aversionReachSD*sd)))
			keep[i] *= 1 - reach*title.weight
		}
	}

	scores := make([]float64, len(pool))
	for i, item := range pool {
		scores[i] = item.Score
	}
	_, scoreSD := meanSD(scores)
	if scoreSD == 0 {
		// Equal scores: push by a tenth of the score, so a turned-away
		// neighbor still falls behind its peers.
		scoreSD = math.Abs(scores[0]) / 10
	}
	out := slices.Clone(pool)
	for i, c := range candidates {
		if averse := 1 - keep[i]; averse > 0 {
			out[c.index].Score -= aversionPenaltySD * scoreSD * averse
			res.penalized++
		}
	}
	return out, res
}

// quantile returns the q-quantile of values, which must not be empty, by
// linear interpolation between the nearest ranks.
func quantile(values []float64, q float64) float64 {
	sorted := slices.Sorted(slices.Values(values))
	pos := q * float64(len(sorted)-1)
	lo := int(pos)
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (pos-float64(lo))*(sorted[lo+1]-sorted[lo])
}

// loadAversion reads the turned-away titles the profile's rows are built
// against at now.
func (e *Engine) loadAversion(ctx context.Context, userID int, profileID string, now time.Time) (*aversion, error) {
	marks, err := e.signalReader().NotInterestedMarks(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	marked := make([]datedTitle, len(marks))
	for i, m := range marks {
		marked[i] = datedTitle{m.MediaItemID, m.MarkedAt}
	}
	var low []datedTitle
	if e.ratingsRepo != nil {
		ratings, err := e.ratingsRepo.ListAll(ctx, userID, profileID)
		if err != nil {
			return nil, fmt.Errorf("list ratings: %w", err)
		}
		var ids []string
		for _, r := range ratings {
			if r.Rating <= DislikedRatingMax {
				ids = append(ids, r.MediaItemID)
			}
		}
		canonical, err := e.repo.ResolveCanonicalItemIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve low-rated item IDs: %w", err)
		}
		for _, r := range ratings {
			if r.Rating > DislikedRatingMax {
				continue
			}
			id := r.MediaItemID
			if c := canonical[id]; c != "" {
				id = c
			}
			low = append(low, datedTitle{id, r.RatedAt})
		}
	}
	halfLife := e.cfg.TasteDecayHalfLifeDays
	if halfLife == 0 {
		halfLife = 180
	}
	return buildAversion(ctx, e.repo, marked, low, now, halfLife)
}

// aversionEmbeddings reads turned-away titles' embeddings. *Repo implements
// it.
type aversionEmbeddings interface {
	GetBatchEmbeddings(ctx context.Context, itemIDs []string) (map[string][]float32, error)
}

// datedTitle is a canonical title and when the profile turned it away.
type datedTitle struct {
	id string
	at time.Time
}

// buildAversion weighs the profile's turned-away titles at now: its Not
// interested marks and its 1–2 star ratings, newest first, at most
// aversionMaxTitles, each decayed by its age. A title both marked and rated
// low counts once, as the heavier. Titles without an embedding are left out.
func buildAversion(ctx context.Context, store aversionEmbeddings, marks, lowRatings []datedTitle, now time.Time, ratingHalfLifeDays float64) (*aversion, error) {
	type weighed struct {
		at     time.Time
		weight float64
	}
	byID := make(map[string]weighed, len(marks)+len(lowRatings))
	add := func(id string, at time.Time, weight float64) {
		if prev, ok := byID[id]; ok && prev.weight >= weight {
			return
		}
		byID[id] = weighed{at, weight}
	}
	for _, m := range marks {
		add(m.id, m.at, aversionWeightMark*timeDecay(m.at, now, aversionMarkHalfLifeDays))
	}
	for _, r := range lowRatings {
		add(r.id, r.at, aversionWeightRatedLow*timeDecay(r.at, now, ratingHalfLifeDays))
	}
	if len(byID) == 0 {
		return &aversion{}, nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(x, y string) int {
		if c := byID[y].at.Compare(byID[x].at); c != 0 {
			return c
		}
		return strings.Compare(x, y)
	})
	ids = ids[:min(len(ids), aversionMaxTitles)]
	embeddings, err := store.GetBatchEmbeddings(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("turned-away title embeddings: %w", err)
	}
	a := &aversion{titles: make([]aversionTitle, 0, len(ids))}
	for _, id := range ids {
		if emb := embeddings[id]; emb != nil {
			a.titles = append(a.titles, aversionTitle{embedding: emb, weight: byID[id].weight})
		}
	}
	return a, nil
}
