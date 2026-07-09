package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// weightedAverage computes a weighted average of embedding vectors. Each entry
// in vecs is scaled by the corresponding weight in weights. The result is
// L2-normalised. Returns nil when no items contribute a non-zero weight or
// when all embeddings are nil.
func weightedAverage(vecs [][]float32, weights []float64) []float32 {
	if len(vecs) == 0 {
		return nil
	}

	var dims int
	for _, v := range vecs {
		if v != nil {
			dims = len(v)
			break
		}
	}
	if dims == 0 {
		return nil
	}

	sum := make([]float64, dims)
	totalAbsWeight := 0.0

	for i, v := range vecs {
		if v == nil || i >= len(weights) {
			continue
		}
		w := weights[i]
		if w == 0 {
			continue
		}
		totalAbsWeight += math.Abs(w)
		for j, val := range v {
			sum[j] += float64(val) * w
		}
	}

	if totalAbsWeight == 0 {
		return nil
	}

	// L2 normalise.
	var norm float64
	for _, v := range sum {
		norm += v * v
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return nil
	}

	result := make([]float32, dims)
	for i, v := range sum {
		result[i] = float32(v / norm)
	}
	return result
}

// timeDecay computes an exponential decay factor for a signal recorded at signalTime.
// halfLifeDays controls how fast signals lose weight. Floor is 10% of original.
func timeDecay(signalTime time.Time, now time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		return 1.0
	}
	lambda := math.Ln2 / halfLifeDays
	daysSince := now.Sub(signalTime).Hours() / 24.0
	if daysSince < 0 {
		daysSince = 0
	}
	factor := math.Exp(-lambda * daysSince)
	if factor < 0.1 {
		factor = 0.1 // 10% floor
	}
	return factor
}

// contentRatingScore returns a numeric precedence for a content rating so
// ratings can be compared by strictness. Higher score = more restrictive.
func contentRatingScore(rating string) int {
	scores := map[string]int{
		"NC-17": 7,
		"TV-MA": 6,
		"R":     5,
		"TV-14": 4,
		"PG-13": 3,
		"TV-PG": 2,
		"PG":    2,
		"G":     1,
		"TV-G":  1,
		"TV-Y":  0,
	}
	if s, ok := scores[rating]; ok {
		return s
	}
	return 0
}

// maxContentRatingFromSets derives the highest content rating among
// positively-signaled and completed canonical items.
func maxContentRatingFromSets(ratedSet, completedSet, favSet map[string]struct{}, itemContentRatings map[string]string) string {
	best := ""
	bestScore := -1

	check := func(id string) {
		cr, ok := itemContentRatings[id]
		if !ok || cr == "" {
			return
		}
		s := contentRatingScore(cr)
		if s > bestScore {
			bestScore = s
			best = cr
		}
	}

	for id := range ratedSet {
		check(id)
	}
	for id := range completedSet {
		check(id)
	}
	for id := range favSet {
		check(id)
	}
	return best
}

// RefreshTasteProfile rebuilds the taste profile embedding for the given user
// and profile by aggregating signal weights from ratings, favorites, watchlist,
// watch progress, and rewatches. Applies time decay and builds clustered
// sub-profiles.
func (e *Engine) RefreshTasteProfile(ctx context.Context, userID int, profileID string) error {
	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("get user store for user %d: %w", userID, err)
	}

	ratings, err := e.ratingsRepo.List(ctx, userID, profileID, 1000, 0)
	if err != nil {
		return fmt.Errorf("list ratings: %w", err)
	}

	favorites, err := store.ListFavorites(ctx, profileID, 1000, 0)
	if err != nil {
		return fmt.Errorf("list favorites: %w", err)
	}

	watchlist, err := store.ListWatchlist(ctx, profileID, 1000, 0)
	if err != nil {
		return fmt.Errorf("list watchlist: %w", err)
	}

	watchProgress, err := e.signalReader().WatchProgressForUser(ctx, userID, profileID)
	if err != nil {
		return fmt.Errorf("get watch progress: %w", err)
	}

	rewatchCounts, err := e.signalReader().RewatchCounts(ctx, userID, profileID)
	if err != nil {
		return fmt.Errorf("get rewatch counts: %w", err)
	}

	now := time.Now()
	halfLife := e.cfg.TasteDecayHalfLifeDays
	if halfLife == 0 {
		halfLife = 180
	}

	rawIDSet := make(map[string]struct{})
	for _, r := range ratings {
		rawIDSet[r.MediaItemID] = struct{}{}
	}
	for _, f := range favorites {
		rawIDSet[f.MediaItemID] = struct{}{}
	}
	for _, w := range watchlist {
		rawIDSet[w.MediaItemID] = struct{}{}
	}
	for _, wp := range watchProgress {
		rawIDSet[wp.MediaItemID] = struct{}{}
	}
	for _, rc := range rewatchCounts {
		rawIDSet[rc.MediaItemID] = struct{}{}
	}

	rawIDs := make([]string, 0, len(rawIDSet))
	for id := range rawIDSet {
		rawIDs = append(rawIDs, id)
	}

	refs, err := e.repo.ResolveCanonicalContentRefs(ctx, rawIDs)
	if err != nil {
		return fmt.Errorf("resolve canonical content refs: %w", err)
	}

	signalCounts := make(map[string]int)
	signals := make(map[string]*canonicalWeightComponents)
	ensureSignal := func(id string) *canonicalWeightComponents {
		if s, ok := signals[id]; ok {
			return s
		}
		s := &canonicalWeightComponents{}
		signals[id] = s
		return s
	}
	ratedSet := make(map[string]struct{})

	for _, r := range ratings {
		ref, ok := refs[r.MediaItemID]
		if !ok || !ref.isCanonicalTasteItem() {
			continue
		}

		s := ensureSignal(ref.CanonicalID)
		rating := r.Rating
		s.Rating = &rating
		ratedSet[ref.CanonicalID] = struct{}{}

		decay := timeDecay(r.RatedAt, now, halfLife)
		switch {
		case r.Rating == 5:
			s.ExplicitWeight += WeightRated5 * decay
			signalCounts["rated_5"]++
		case r.Rating == 4:
			s.ExplicitWeight += WeightRated4 * decay
			signalCounts["rated_4"]++
		case r.Rating == 3:
			s.ExplicitWeight += WeightRated3 * decay
			signalCounts["rated_3"]++
		default:
			s.ExplicitWeight += WeightRatedLow * decay
			signalCounts["rated_low"]++
		}
	}

	for _, wp := range watchProgress {
		ref, ok := refs[wp.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}
		weight, ok := implicitWatchWeight(wp)
		if !ok {
			continue
		}
		switch weight {
		case WeightWatchHigh:
			signalCounts["watch_high"]++
		case WeightWatchMed:
			signalCounts["watch_med"]++
		case WeightWatchLow:
			signalCounts["watch_low"]++
		}
	}

	for _, rc := range rewatchCounts {
		ref, ok := refs[rc.MediaItemID]
		if !ok || ref.CanonicalID == "" || rc.Count < 2 {
			continue
		}
		signalCounts["rewatch"]++
	}

	implicitSignals, completedSet := buildCanonicalImplicitSignals(watchProgress, rewatchCounts, refs, now, halfLife)
	for canonicalID, weight := range implicitSignals {
		ensureSignal(canonicalID).ImplicitWeight += weight
	}

	favSet := make(map[string]struct{}, len(favorites))
	for _, f := range favorites {
		ref, ok := refs[f.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}
		favSet[ref.CanonicalID] = struct{}{}
		s := ensureSignal(ref.CanonicalID)
		s.IntentWeight += WeightFavorited * timeDecay(parseSignalTime(f.AddedAt, now), now, halfLife)
		signalCounts["favorited"]++
	}

	for _, w := range watchlist {
		ref, ok := refs[w.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}
		s := ensureSignal(ref.CanonicalID)
		s.IntentWeight += WeightWatchlist * timeDecay(parseSignalTime(w.AddedAt, now), now, halfLife)
		signalCounts["watchlist"]++
	}

	if len(signals) == 0 {
		return nil
	}

	allIDs := make([]string, 0, len(signals))
	for id := range signals {
		allIDs = append(allIDs, id)
	}

	items, err := e.itemRepo.GetByIDs(ctx, allIDs)
	if err != nil {
		slog.ErrorContext(ctx, "taste: failed to fetch items for genre enrichment, cluster labels will be degraded", "component", "recommendations",
			"user_id", userID, "profile_id", profileID,
			"id_count", len(allIDs), "error", err)
	} else {
		itemMap := make(map[string]*models.MediaItem, len(items))
		for _, item := range items {
			itemMap[item.ContentID] = item
		}
		for id, s := range signals {
			if item, ok := itemMap[id]; ok {
				s.Genres = item.Genres
			}
		}
	}

	embMap, err := e.repo.GetBatchEmbeddings(ctx, allIDs)
	if err != nil {
		return fmt.Errorf("get batch embeddings: %w", err)
	}

	vecs := make([][]float32, 0, len(signals))
	embWeights := make([]float64, 0, len(signals))
	var positiveItems []clusterItem

	signalIDs := make([]string, 0, len(signals))
	for id := range signals {
		signalIDs = append(signalIDs, id)
	}
	sort.Strings(signalIDs)

	for _, id := range signalIDs {
		s := signals[id]
		emb, ok := embMap[id]
		if !ok || emb == nil {
			continue
		}
		finalWeight := combineCanonicalWeight(s.Rating, s.ExplicitWeight, s.ImplicitWeight, s.IntentWeight)
		if finalWeight == 0 {
			continue
		}

		vecs = append(vecs, emb)
		embWeights = append(embWeights, finalWeight)

		if finalWeight > 0 {
			positiveItems = append(positiveItems, clusterItem{
				itemID:    id,
				embedding: emb,
				weight:    finalWeight,
				genres:    s.Genres,
			})
		}
	}

	profile := weightedAverage(vecs, embWeights)
	if profile == nil {
		return nil
	}

	maxContentRating := ""
	if len(allIDs) > 0 && items != nil {
		crMap := make(map[string]string, len(items))
		for _, item := range items {
			crMap[item.ContentID] = item.ContentRating
		}
		maxContentRating = maxContentRatingFromSets(ratedSet, completedSet, favSet, crMap)
	}

	if err := e.repo.UpsertTasteProfile(ctx, userID, profileID, profile, signalCounts, maxContentRating); err != nil {
		return fmt.Errorf("upsert taste profile: %w", err)
	}

	if len(positiveItems) > 0 {
		clusters := buildTasteClusters(positiveItems)
		for i := range clusters {
			clusters[i].UserID = userID
			clusters[i].ProfileID = profileID
		}
		if err := e.repo.UpsertTasteClusters(ctx, userID, profileID, clusters); err != nil {
			return fmt.Errorf("upsert taste clusters: %w", err)
		}
	}

	return nil
}

// GetTasteProfileSummary returns a human-readable summary of the user's taste
// profile including top genres and directors.
func (e *Engine) GetTasteProfileSummary(ctx context.Context, userID int, profileID string) (*TasteProfileSummary, error) {
	meta, err := e.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste profile meta: %w", err)
	}
	if meta == nil {
		return &TasteProfileSummary{
			TopGenres:         []string{},
			FavoriteDirectors: []string{},
			SignalCounts:      map[string]int{},
			UpdatedAt:         "",
		}, nil
	}

	// Gather top-rated and favorited item IDs to derive genre/director preferences.
	ratings, err := e.ratingsRepo.List(ctx, userID, profileID, 1000, 0)
	if err != nil {
		return nil, fmt.Errorf("list ratings for summary: %w", err)
	}

	store, err := e.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user store for summary: %w", err)
	}

	favorites, err := store.ListFavorites(ctx, profileID, 1000, 0)
	if err != nil {
		return nil, fmt.Errorf("list favorites for summary: %w", err)
	}

	rawIDSet := make(map[string]struct{})
	for _, r := range ratings {
		if r.Rating >= 4 {
			rawIDSet[r.MediaItemID] = struct{}{}
		}
	}
	for _, f := range favorites {
		rawIDSet[f.MediaItemID] = struct{}{}
	}

	rawIDs := make([]string, 0, len(rawIDSet))
	for id := range rawIDSet {
		rawIDs = append(rawIDs, id)
	}

	refs, err := e.repo.ResolveCanonicalContentRefs(ctx, rawIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical summary refs: %w", err)
	}

	idSet := make(map[string]struct{})
	for _, r := range ratings {
		if r.Rating < 4 {
			continue
		}
		ref, ok := refs[r.MediaItemID]
		if !ok || !ref.isCanonicalTasteItem() {
			continue
		}
		idSet[ref.CanonicalID] = struct{}{}
	}
	for _, f := range favorites {
		ref, ok := refs[f.MediaItemID]
		if !ok || ref.CanonicalID == "" {
			continue
		}
		idSet[ref.CanonicalID] = struct{}{}
	}

	topGenres := []string{}
	topDirectors := []string{}

	if len(idSet) > 0 {
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}

		items, err := e.itemRepo.GetByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("get items for summary: %w", err)
		}

		genreCounts := make(map[string]int)
		directorCounts := make(map[string]int)

		for _, item := range items {
			for _, g := range item.Genres {
				genreCounts[g]++
			}
			for _, p := range item.People {
				if p.Kind == models.PersonKindDirector {
					directorCounts[p.Name]++
				}
			}
		}

		topGenres = topN(genreCounts, 5)
		topDirectors = topN(directorCounts, 5)
	}

	return &TasteProfileSummary{
		TopGenres:         topGenres,
		FavoriteDirectors: topDirectors,
		SignalCounts:      meta.SignalCounts,
		UpdatedAt:         meta.UpdatedAt,
	}, nil
}

// topN returns up to n keys from counts, ordered by count descending.
func topN(counts map[string]int, n int) []string {
	type kv struct {
		key   string
		count int
	}

	pairs := make([]kv, 0, len(counts))
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}

	// Simple selection sort — n is always small (≤ 5).
	for i := 0; i < len(pairs) && i < n; i++ {
		maxIdx := i
		for j := i + 1; j < len(pairs); j++ {
			if pairs[j].count > pairs[maxIdx].count {
				maxIdx = j
			}
		}
		pairs[i], pairs[maxIdx] = pairs[maxIdx], pairs[i]
	}

	end := n
	if end > len(pairs) {
		end = len(pairs)
	}

	result := make([]string, end)
	for i := 0; i < end; i++ {
		result[i] = pairs[i].key
	}
	return result
}
