package recommendations

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const aggregateMediaTypeFloorDivisor = 5

var aggregateSupplementMediaTypes = []string{"movie", "series", "audiobook", "ebook"}

// ForYou returns personalised recommendations grouped by taste clusters.
// For cold-start users, non-personalized rows are returned.
func (e *Engine) ForYou(ctx context.Context, userID int, profileID string, limit int) (*ForYouResponse, error) {
	// Check signal count to determine cold-start level.
	meta, err := e.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste profile meta: %w", err)
	}

	positiveSignals := 0
	if meta != nil {
		for k, v := range meta.SignalCounts {
			switch k {
			case "rated_low", "watch_low":
				// negative signals don't count
			default:
				positiveSignals += v
			}
		}
	}

	level := coldStartLevel(positiveSignals)

	// Build cold-start rows (always available).
	coldStartRows, err := e.buildColdStartRows(ctx)
	if err != nil {
		return nil, fmt.Errorf("build cold start rows: %w", err)
	}

	watchedSet, err := e.watchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watched item IDs: %w", err)
	}
	watchedIDs := scoredItemIDsFromSet(watchedSet)
	coldStartRows = excludeWatchedRows(coldStartRows, watchedSet)

	// If no taste profile at all, return cold-start only.
	if meta == nil || level == 0 {
		return &ForYouResponse{Rows: coldStartRows}, nil
	}

	// Build personalized rows from taste clusters.
	liveFilter := catalog.AccessFilter{UserID: userID, ProfileID: profileID}
	personalRows, err := e.buildClusterRows(ctx, userID, profileID, limit, watchedIDs, liveFilter)
	if err != nil {
		return nil, fmt.Errorf("build cluster rows: %w", err)
	}

	aggregatedRow, err := e.buildAggregatedRow(ctx, userID, profileID, limit, watchedIDs, liveFilter)
	if err != nil {
		return nil, fmt.Errorf("build aggregated row: %w", err)
	}
	personalRows = combinePersonalRows(aggregatedRow, personalRows)

	merged := mergePersonalizedAndColdStart(personalRows, coldStartRows, level)
	return &ForYouResponse{Rows: merged}, nil
}

func combinePersonalRows(aggregated *ForYouRow, clusterRows []ForYouRow) []ForYouRow {
	if aggregated == nil {
		return clusterRows
	}

	rows := make([]ForYouRow, 0, len(clusterRows)+1)
	rows = append(rows, *aggregated)
	rows = append(rows, clusterRows...)
	return rows
}

// buildClusterRows generates per-cluster recommendation rows.
func (e *Engine) buildClusterRows(ctx context.Context, userID int, profileID string, limit int, excludeIDs []string, filter catalog.AccessFilter) ([]ForYouRow, error) {
	clusters, err := e.repo.GetTasteClusters(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste clusters: %w", err)
	}
	if len(clusters) == 0 {
		return nil, nil
	}

	// Calculate total weight across all clusters for proportional allocation.
	var totalWeight float64
	for _, c := range clusters {
		totalWeight += c.TotalWeight
	}

	var rows []ForYouRow
	for _, c := range clusters {
		if c.Embedding == nil || len(c.Embedding) == 0 {
			continue
		}

		// Proportional candidate count.
		proportion := 1.0 / float64(len(clusters))
		if totalWeight > 0 {
			proportion = c.TotalWeight / totalWeight
		}
		clusterLimit := int(float64(limit) * proportion)
		if clusterLimit < 3 {
			clusterLimit = 3
		}

		// Fetch after access and genre constraints so filtered-out items do not
		// consume the candidate headroom before MMR.
		candidates, _, err := e.repo.FindTasteProfileCandidates(ctx, c.Embedding, excludeIDs, c.DominantGenres, clusterLimit*3, filter)
		if err != nil {
			continue
		}
		if len(candidates) == 0 {
			continue
		}

		// Apply MMR re-ranking.
		candidateIDs := make([]string, len(candidates))
		for i, item := range candidates {
			candidateIDs[i] = item.MediaItemID
		}

		embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
		reranked := applyMMR(candidates, embMap, e.mmrLambda(LambdaGenreRow), clusterLimit)

		// Apply recency boost.
		addedDates, _ := e.repo.GetItemAddedDates(ctx, candidateIDs)
		reranked = applyRecencyBoost(reranked, addedDates, time.Now())

		label := c.Label
		if label == "" {
			label = "For You"
		}

		reason := "Because you enjoy " + label
		for i := range reranked {
			reranked[i].Reason = reason
		}

		rows = append(rows, ForYouRow{
			Type:         "cluster",
			Label:        reason,
			ClusterIndex: c.ClusterIdx,
			Items:        reranked,
		})
	}

	return rows, nil
}

// buildAggregatedRow builds a single "For You" row from the aggregated taste profile.
func (e *Engine) buildAggregatedRow(ctx context.Context, userID int, profileID string, limit int, excludeIDs []string, filter catalog.AccessFilter) (*ForYouRow, error) {
	embedding, err := e.repo.GetTasteProfile(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste profile: %w", err)
	}
	if embedding == nil {
		return nil, nil
	}

	candidates, genreMap, err := e.repo.FindTasteProfileCandidates(ctx, embedding, excludeIDs, nil, limit*3, filter)
	if err != nil {
		return nil, fmt.Errorf("find similar for aggregated: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	candidates, genreMap, mediaTypes := e.addAggregateMediaTypeSupplements(ctx, embedding, excludeIDs, filter, candidates, genreMap, limit)
	candidateIDs := make([]string, len(candidates))
	for i, item := range candidates {
		candidateIDs[i] = item.MediaItemID
	}

	embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
	reranked := applyMMR(candidates, embMap, e.mmrLambda(LambdaForYou), limit)

	// Apply genre cap on the main For You row.
	reranked = applyGenreCap(reranked, genreMap, GenreCapPercent)
	reranked = applyMediaTypeFloor(reranked, candidates, mediaTypes)

	for i := range reranked {
		reranked[i].Reason = "Personalized for you"
	}

	return &ForYouRow{
		Type:  "cluster",
		Label: "For You",
		Items: reranked,
	}, nil
}

func (e *Engine) addAggregateMediaTypeSupplements(
	ctx context.Context,
	embedding []float32,
	excludeIDs []string,
	filter catalog.AccessFilter,
	candidates []ScoredItem,
	genreMap map[string][]string,
	limit int,
) ([]ScoredItem, map[string][]string, map[string]string) {
	mediaTypes, err := e.repo.GetItemMediaTypes(ctx, scoredItemIDs(candidates))
	if err != nil {
		return candidates, genreMap, map[string]string{}
	}

	floor := mediaTypeFloor(limit)
	changed := false
	for _, mediaType := range aggregateSupplementMediaTypes {
		if countMediaType(candidates, mediaTypes, mediaType) >= floor {
			continue
		}

		extra, extraGenres, err := e.repo.FindTasteProfileCandidatesByMediaType(ctx, embedding, excludeIDs, nil, limit, filter, mediaType)
		if err != nil || len(extra) == 0 {
			continue
		}
		candidates = mergeScoredCandidates(candidates, extra)
		for id, genres := range extraGenres {
			genreMap[id] = genres
		}
		changed = true
	}

	if changed {
		if refreshed, err := e.repo.GetItemMediaTypes(ctx, scoredItemIDs(candidates)); err == nil {
			mediaTypes = refreshed
		}
	}
	return candidates, genreMap, mediaTypes
}

func applyMediaTypeFloor(items []ScoredItem, candidates []ScoredItem, mediaTypes map[string]string) []ScoredItem {
	if len(items) == 0 || len(candidates) == 0 || len(mediaTypes) == 0 {
		return items
	}

	floor := mediaTypeFloor(len(items))
	for _, mediaType := range aggregateSupplementMediaTypes {
		if countMediaType(candidates, mediaTypes, mediaType) == 0 {
			continue
		}
		items = ensureMediaTypeFloor(items, candidates, mediaTypes, mediaType, floor)
	}
	return items
}

func ensureMediaTypeFloor(items []ScoredItem, candidates []ScoredItem, mediaTypes map[string]string, mediaType string, floor int) []ScoredItem {
	if floor <= 0 || countMediaType(items, mediaTypes, mediaType) >= floor {
		return items
	}

	selected := make(map[string]struct{}, len(items))
	for _, item := range items {
		selected[item.MediaItemID] = struct{}{}
	}

	needed := floor - countMediaType(items, mediaTypes, mediaType)
	replacements := make([]ScoredItem, 0, needed)
	for _, candidate := range candidates {
		if len(replacements) >= needed {
			break
		}
		if mediaTypes[candidate.MediaItemID] != mediaType {
			continue
		}
		if _, ok := selected[candidate.MediaItemID]; ok {
			continue
		}
		replacements = append(replacements, candidate)
		selected[candidate.MediaItemID] = struct{}{}
	}
	if len(replacements) == 0 {
		return items
	}

	remove := make(map[string]struct{}, len(replacements))
	for i := len(items) - 1; i >= 0 && len(remove) < len(replacements); i-- {
		if mediaTypes[items[i].MediaItemID] == mediaType {
			continue
		}
		remove[items[i].MediaItemID] = struct{}{}
	}
	if len(remove) < len(replacements) {
		return items
	}

	mixed := make([]ScoredItem, 0, len(items))
	for _, item := range items {
		if _, ok := remove[item.MediaItemID]; ok {
			continue
		}
		mixed = append(mixed, item)
	}
	mixed = append(mixed, replacements...)
	sortScoredItems(mixed)
	return mixed
}

func mediaTypeFloor(limit int) int {
	if limit <= 0 {
		return 0
	}
	floor := (limit + aggregateMediaTypeFloorDivisor - 1) / aggregateMediaTypeFloorDivisor
	if floor < 1 {
		return 1
	}
	return floor
}

func countMediaType(items []ScoredItem, mediaTypes map[string]string, mediaType string) int {
	count := 0
	for _, item := range items {
		if mediaTypes[item.MediaItemID] == mediaType {
			count++
		}
	}
	return count
}

func scoredItemIDs(items []ScoredItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MediaItemID)
	}
	return ids
}

func mergeScoredCandidates(base []ScoredItem, extra []ScoredItem) []ScoredItem {
	seen := make(map[string]struct{}, len(base)+len(extra))
	merged := make([]ScoredItem, 0, len(base)+len(extra))
	for _, item := range base {
		if _, ok := seen[item.MediaItemID]; ok {
			continue
		}
		seen[item.MediaItemID] = struct{}{}
		merged = append(merged, item)
	}
	for _, item := range extra {
		if _, ok := seen[item.MediaItemID]; ok {
			continue
		}
		seen[item.MediaItemID] = struct{}{}
		merged = append(merged, item)
	}
	sortScoredItems(merged)
	return merged
}

func sortScoredItems(items []ScoredItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].MediaItemID < items[j].MediaItemID
	})
}

// buildColdStartRows generates non-personalized rows.
func (e *Engine) buildColdStartRows(ctx context.Context) ([]ForYouRow, error) {
	popular, _ := e.repo.GetPopularItems(ctx, 30, 20)
	recentlyAdded, _ := e.repo.GetRecentlyAddedItems(ctx, 14, 20)
	topRated, _ := e.repo.GetTopRatedItems(ctx, 5, 20)

	genreSamplers := make(map[string][]ScoredItem)
	topGenres, _ := e.repo.GetTopGenres(ctx, 5)
	for _, genre := range topGenres {
		items, _ := e.repo.GetGenreSamplerItems(ctx, genre, 20)
		if len(items) > 0 {
			genreSamplers[genre] = items
		}
	}

	return buildColdStartRows(popular, recentlyAdded, topRated, genreSamplers), nil
}

// BecauseYouWatched returns items similar to a specific item the user has
// watched. Blends embedding similarity (70%) with co-watch data (30%).
func (e *Engine) BecauseYouWatched(ctx context.Context, userID int, profileID string, sourceItemID string, limit int) ([]ScoredItem, error) {
	embedding, err := e.repo.GetEmbedding(ctx, sourceItemID)
	if err != nil {
		return nil, fmt.Errorf("get embedding for item %s: %w", sourceItemID, err)
	}
	if embedding == nil {
		return nil, nil
	}

	// Constrain to the source item's media type so "Because you watched" never
	// mixes movies, series, and audiobooks in one rail.
	sourceMeta, _ := e.repo.GetItemMetadata(ctx, sourceItemID)
	sourceType := ""
	if sourceMeta != nil {
		sourceType = sourceMeta.Type
	}

	// Get embedding-based candidates (3x for MMR).
	embCandidates, err := e.repo.FindSimilar(ctx, embedding, []string{sourceItemID}, sourceType, limit*3)
	if err != nil {
		return nil, fmt.Errorf("find similar for because watched: %w", err)
	}

	// Get co-watch neighbors.
	cowatchPairs, _ := e.repo.GetCowatchNeighbors(ctx, sourceItemID, limit*3)
	cowatchMap := make(map[string]float64, len(cowatchPairs))
	for _, p := range cowatchPairs {
		cowatchMap[p.SimilarItemID] = p.JaccardScore
	}

	// Blend scores.
	blended := blendScores(embCandidates, cowatchMap, 0.7, 0.3)

	// Apply MMR re-ranking.
	candidateIDs := make([]string, len(blended))
	for i, item := range blended {
		candidateIDs[i] = item.MediaItemID
	}
	embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
	result := applyMMR(blended, embMap, e.mmrLambda(LambdaBecauseWatched), limit)

	for i := range result {
		if result[i].Reason == "" {
			result[i].Reason = "because_you_watched"
		}
	}

	watchedSet, err := e.watchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get watched item IDs: %w", err)
	}

	return excludeScoredItems(result, watchedSet), nil
}

func excludeWatchedRows(rows []ForYouRow, watchedSet map[string]struct{}) []ForYouRow {
	if len(rows) == 0 || len(watchedSet) == 0 {
		return rows
	}

	filteredRows := make([]ForYouRow, 0, len(rows))
	for _, row := range rows {
		row.Items = excludeScoredItems(row.Items, watchedSet)
		if len(row.Items) == 0 {
			continue
		}
		filteredRows = append(filteredRows, row)
	}
	return filteredRows
}
