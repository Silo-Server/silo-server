package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

const aggregateMediaTypeFloorDivisor = 5

var aggregateSupplementMediaTypes = []string{"movie", "series", "audiobook", "ebook"}

// clusterTitlePrefix starts every cluster row title and the Reason of every
// item in a cluster row.
const clusterTitlePrefix = "Because you enjoy "

// unlabeledClusterLabel stands in for a cluster with no label.
const unlabeledClusterLabel = "For You"

// clusterTitle is the title of the cluster row with the given label.
func clusterTitle(label string) string {
	if label == "" {
		label = unlabeledClusterLabel
	}
	return clusterTitlePrefix + label
}

// buildClusterRows generates per-cluster recommendation rows. A cluster whose
// candidate query fails is logged and skipped; failed counts them. A cluster
// with no candidates gets a row without items, so its cached row is dropped.
func (e *Engine) buildClusterRows(ctx context.Context, userID int, profileID string, limit int, excludeIDs []string, filter catalog.AccessFilter) (rows []ForYouRow, failed int, err error) {
	clusters, err := e.repo.GetTasteClusters(ctx, userID, profileID)
	if err != nil {
		return nil, 0, fmt.Errorf("get taste clusters: %w", err)
	}
	if len(clusters) == 0 {
		return nil, 0, nil
	}

	// Calculate total weight across all clusters for proportional allocation.
	var totalWeight float64
	for _, c := range clusters {
		totalWeight += c.TotalWeight
	}

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
			failed++
			slog.WarnContext(ctx, "cluster recommendation candidates failed", "component", "recommendations",
				"user_id", userID, "profile_id", profileID, "cluster_idx", c.ClusterIdx, "error", err)
			continue
		}
		if len(candidates) == 0 {
			rows = append(rows, ForYouRow{Type: clusterRowType, Label: clusterTitle(c.Label), ClusterIndex: c.ClusterIdx})
			continue
		}

		// Apply MMR re-ranking.
		candidateIDs := make([]string, len(candidates))
		for i, item := range candidates {
			candidateIDs[i] = item.MediaItemID
		}

		embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
		reranked := applyMMR(candidates, embMap, e.mmrLambda(), clusterLimit)

		// Apply recency boost.
		addedDates, _ := e.repo.GetItemAddedDates(ctx, candidateIDs)
		reranked = applyRecencyBoost(reranked, addedDates, time.Now())

		// Every item carries the row title, so a reader can title the cached
		// row from the build that produced it (see clusterRow).
		title := clusterTitle(c.Label)
		for i := range reranked {
			reranked[i].Reason = title
		}

		rows = append(rows, ForYouRow{
			Type:         clusterRowType,
			Label:        title,
			ClusterIndex: c.ClusterIdx,
			Items:        reranked,
		})
	}

	return rows, failed, nil
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
	reranked := applyMMR(candidates, embMap, e.mmrLambda(), limit)

	// Apply genre cap on the main For You row.
	reranked = applyGenreCap(reranked, genreMap, GenreCapPercent)
	reranked = applyMediaTypeFloor(reranked, candidates, mediaTypes)

	for i := range reranked {
		reranked[i].Reason = "Personalized for you"
	}

	return &ForYouRow{
		Type:  clusterRowType,
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

// BecauseYouWatched returns items similar to a specific item the user has
// watched. Blends embedding similarity (70%) with co-watch data (30%). Titles
// in the profile's recommendation exclusion set, and those filter does not
// admit, are left out before ranking.
func (e *Engine) BecauseYouWatched(ctx context.Context, userID int, profileID string, sourceItemID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	excluded, err := e.recommendationExclusionSet(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get recommendation exclusions: %w", err)
	}
	return e.becauseYouWatched(ctx, sourceItemID, limit, excluded, filter)
}

// becauseYouWatched is BecauseYouWatched with the exclusion set given.
func (e *Engine) becauseYouWatched(ctx context.Context, sourceItemID string, limit int, excluded map[string]struct{}, filter catalog.AccessFilter) ([]ScoredItem, error) {
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

	// Get embedding-based candidates (3x for MMR). Excluded titles and those
	// the viewer cannot see are left out in the query, so they do not take
	// the candidates' places.
	excludeIDs := append([]string{sourceItemID}, scoredItemIDsFromSet(excluded)...)
	embCandidates, err := e.repo.FindSimilar(ctx, embedding, excludeIDs, sourceType, limit*3, filter)
	if err != nil {
		return nil, fmt.Errorf("find similar for because watched: %w", err)
	}

	// Get co-watch neighbors the viewer can see.
	cowatchPairs, _ := e.repo.GetCowatchNeighbors(ctx, sourceItemID, limit*3)
	cowatchMap, err := e.accessibleCowatchScores(ctx, cowatchPairs, filter)
	if err != nil {
		return nil, fmt.Errorf("filter co-watch neighbors for because watched: %w", err)
	}

	// Blend scores, then drop excluded titles before MMR so they do not take
	// the row's places.
	blended := blendScores(embCandidates, cowatchMap, 0.7, 0.3)
	blended = excludeScoredItems(blended, excluded)

	// Apply MMR re-ranking.
	candidateIDs := make([]string, len(blended))
	for i, item := range blended {
		candidateIDs[i] = item.MediaItemID
	}
	embMap, _ := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
	result := applyMMR(blended, embMap, e.mmrLambda(), limit)

	for i := range result {
		if result[i].Reason == "" {
			result[i].Reason = "because_you_watched"
		}
	}

	return result, nil
}

// accessibleCowatchScores maps each co-watch neighbor filter admits to its
// Jaccard score. Co-watch neighbors bypass the access predicates of the
// embedding query, so they are checked here.
func (e *Engine) accessibleCowatchScores(ctx context.Context, pairs []CowatchPair, filter catalog.AccessFilter) (map[string]float64, error) {
	scores := make(map[string]float64, len(pairs))
	if len(pairs) == 0 {
		return scores, nil
	}
	ids := make([]string, len(pairs))
	for i, p := range pairs {
		ids[i] = p.SimilarItemID
	}
	accessible, err := e.repo.FilterAccessibleItemIDs(ctx, ids, filter)
	if err != nil {
		return nil, err
	}
	for _, p := range pairs {
		if _, ok := accessible[p.SimilarItemID]; ok {
			scores[p.SimilarItemID] = p.JaccardScore
		}
	}
	return scores, nil
}
