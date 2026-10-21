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

// rowStore is the part of *Repo that builds a profile's personal rows.
type rowStore interface {
	GetTasteProfile(ctx context.Context, userID int, profileID string) ([]float32, error)
	GetTasteClusters(ctx context.Context, userID int, profileID string) ([]TasteCluster, error)
	FindTasteProfileCandidates(ctx context.Context, embedding []float32, excludeIDs []string, genres []string, limit int, filter catalog.AccessFilter) ([]ScoredItem, map[string][]string, error)
	FindTasteProfileCandidatesByMediaType(ctx context.Context, embedding []float32, excludeIDs []string, genres []string, limit int, filter catalog.AccessFilter, mediaType string) ([]ScoredItem, map[string][]string, error)
	GetBatchEmbeddings(ctx context.Context, itemIDs []string) (map[string][]float32, error)
	GetItemMediaTypes(ctx context.Context, itemIDs []string) (map[string]string, error)
	GetItemAddedDates(ctx context.Context, itemIDs []string) (map[string]time.Time, error)
	PresentMediaTypes(ctx context.Context, mediaTypes []string, filter catalog.AccessFilter) (map[string]struct{}, error)
	GetItemQualityRatings(ctx context.Context, itemIDs []string) (map[string]float64, error)
}

// rowBuilder builds a profile's main and cluster rows.
type rowBuilder struct {
	store  rowStore
	lambda float64
	now    time.Time
	// aversion is the titles the profile turned away, which the rows rank
	// against (see aversion.apply); nil for none.
	aversion *aversion
	// penalized, when set, counts the candidates the aversion pushed down.
	penalized *int
}

// withAversion applies the builder's aversion to a pool fetched around
// query, before MMR ranks it.
func (b rowBuilder) withAversion(pool []ScoredItem, embeddings map[string][]float32, query []float32) ([]ScoredItem, aversionResult) {
	pool, res := b.aversion.apply(pool, embeddings, query)
	if b.penalized != nil {
		*b.penalized += res.penalized
	}
	return pool, res
}

// rowBuilder returns the builder for the rows cached now.
func (e *Engine) rowBuilder() rowBuilder {
	return rowBuilder{store: e.repo, lambda: e.mmrLambda(), now: time.Now()}
}

// clusterRows generates per-cluster recommendation rows. A cluster whose
// candidates or their embeddings cannot be read is logged and skipped;
// failed counts them. A cluster
// with no candidates gets a row without items, so its cached row is dropped.
func (b rowBuilder) clusterRows(ctx context.Context, userID int, profileID string, limit int, excludeIDs []string, filter catalog.AccessFilter) (rows []ForYouRow, failed int, err error) {
	clusters, err := b.store.GetTasteClusters(ctx, userID, profileID)
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
		// The cache takes the main row's served window out of the row (see
		// withoutMainRowItems), so it gets that many places more.
		clusterLimit = min(limit, clusterLimit+ServedRowSize)

		// Fetch after access and genre constraints so filtered-out items do not
		// consume the candidate headroom before MMR.
		candidates, _, err := b.store.FindTasteProfileCandidates(ctx, c.Embedding, excludeIDs, c.DominantGenres, clusterLimit*3, filter)
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
		candidates = b.withRecencyBoost(ctx, b.withQualityPrior(ctx, candidates, qualityMinSmallPool))
		embMap, err := b.store.GetBatchEmbeddings(ctx, scoredItemIDs(candidates))
		if err != nil {
			// Ranked without embeddings, MMR could not spread the row.
			failed++
			slog.WarnContext(ctx, "cluster recommendation embeddings failed", "component", "recommendations",
				"user_id", userID, "profile_id", profileID, "cluster_idx", c.ClusterIdx, "error", err)
			continue
		}
		candidates, averse := b.withAversion(candidates, embMap, c.Embedding)
		if averse.interestShare(c.TotalWeight) < aversionInterestFloor {
			// The profile turned away enough of this interest's titles that
			// it no longer gets a row of its own.
			rows = append(rows, ForYouRow{Type: clusterRowType, Label: clusterTitle(c.Label), ClusterIndex: c.ClusterIdx})
			continue
		}
		reranked := applyMMR(candidates, embMap, b.lambda, clusterLimit)

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

// withRecencyBoost applies applyRecencyBoost to a candidate pool with its
// candidates' added dates. A pool whose dates cannot be read keeps its
// scores.
func (b rowBuilder) withRecencyBoost(ctx context.Context, pool []ScoredItem) []ScoredItem {
	if len(pool) == 0 {
		return pool
	}
	added, err := b.store.GetItemAddedDates(ctx, scoredItemIDs(pool))
	if err != nil {
		slog.WarnContext(ctx, "candidate added dates failed; candidates keep their scores", "component", "recommendations", "error", err)
		return pool
	}
	return applyRecencyBoost(pool, added, b.now)
}

// minClusterRowItems is the fewest items a cluster row keeps once the main
// row's served items are taken out of it; a thinner row is cached empty.
const minClusterRowItems = 10

// withoutMainRowItems takes every title the main row can serve, its first
// rotationReach(ServedRowSize) under the daily rotation, out of each cluster
// row, so the main row and a cluster row built from the same interest do not
// show the same titles. A row this leaves with fewer than minClusterRowItems
// is emptied; a row it takes nothing from is kept whole.
func withoutMainRowItems(rows []ForYouRow, main []ScoredItem) []ForYouRow {
	reach := rotationReach(ServedRowSize)
	served := make(map[string]struct{}, reach)
	for _, item := range main[:min(len(main), reach)] {
		served[item.MediaItemID] = struct{}{}
	}
	for i, row := range rows {
		kept := make([]ScoredItem, 0, len(row.Items))
		for _, item := range row.Items {
			if _, ok := served[item.MediaItemID]; !ok {
				kept = append(kept, item)
			}
		}
		if len(kept) == len(row.Items) {
			continue
		}
		if len(kept) < minClusterRowItems {
			kept = []ScoredItem{}
		}
		rows[i].Items = kept
	}
	return rows
}

// mainRow builds the "For You" row of a profile with a taste profile. A
// profile with several interests gets a row composed per interest (see
// mainRowAnchors), whose every prefix holds each interest's share by weight;
// any other, or one whose anchors find nothing, a row of the candidates
// nearest its averaged taste vector, ranked by MMR, whose served window the
// genre pass then shapes. A profile that turned away every one of its
// interests gets a main row with no items (see anchoredRow). The genre pass leaves a
// composed row alone: capping
// a genre at half the window would cut an interest heavier than half the
// profile below its share. Type supplements fill the rest of either row.
func (b rowBuilder) mainRow(ctx context.Context, userID int, profileID string, limit int, excludeIDs []string, filter catalog.AccessFilter) (*ForYouRow, error) {
	embedding, err := b.store.GetTasteProfile(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste profile: %w", err)
	}
	if embedding == nil {
		return nil, nil
	}
	clusters, err := b.store.GetTasteClusters(ctx, userID, profileID)
	if err != nil {
		return nil, fmt.Errorf("get taste clusters: %w", err)
	}

	var row, pool []ScoredItem
	if anchors := mainRowAnchors(clusters); len(anchors) > 0 {
		var allAverted bool
		if row, pool, allAverted, err = b.anchoredRow(ctx, anchors, limit, excludeIDs, filter); err != nil {
			return nil, err
		}
		if allAverted {
			// The profile turned away every interest; a row around the
			// averaged taste vector would serve them all again. The empty
			// row is cached, so reads know it was built.
			return &ForYouRow{Type: clusterRowType, Label: ForYouLabel, Items: []ScoredItem{}}, nil
		}
	}
	if len(row) == 0 {
		var genreMap map[string][]string
		pool, genreMap, err = b.store.FindTasteProfileCandidates(ctx, embedding, excludeIDs, nil, limit*3, filter)
		if err != nil {
			return nil, fmt.Errorf("find similar for aggregated: %w", err)
		}
		pool = b.withRecencyBoost(ctx, b.withQualityPrior(ctx, pool, 0))
		embMap, err := b.store.GetBatchEmbeddings(ctx, scoredItemIDs(pool))
		if err != nil {
			return nil, fmt.Errorf("main row embeddings: %w", err)
		}
		pool, _ = b.withAversion(pool, embMap, embedding)
		row = applyGenreCap(applyMMR(pool, embMap, b.lambda, limit), genreMap)
	}
	if len(row) == 0 {
		return nil, nil
	}
	row = b.addTypeSupplements(ctx, embedding, excludeIDs, filter, pool, row, limit)

	for i := range row {
		row[i].Reason = "Personalized for you"
	}

	return &ForYouRow{
		Type:  clusterRowType,
		Label: ForYouLabel,
		Items: row,
	}, nil
}

// addTypeSupplements gives row at least mediaTypeFloor(len(row)) items of
// each media type the viewer can see, so a library section, which scopes the
// whole cached row to its libraries, still fills. The extra items come from
// pool, the candidates the row was ranked from, and from a per-type candidate
// query for a type pool is short of; placeTypeSupplements puts them after the
// served window. A type the viewer has no titles of is not queried.
func (b rowBuilder) addTypeSupplements(
	ctx context.Context,
	embedding []float32,
	excludeIDs []string,
	filter catalog.AccessFilter,
	pool []ScoredItem,
	row []ScoredItem,
	limit int,
) []ScoredItem {
	if len(row) <= ServedRowSize {
		return row
	}
	mediaTypes, err := b.store.GetItemMediaTypes(ctx, scoredItemIDs(pool))
	if err != nil {
		slog.WarnContext(ctx, "main row media types failed; row left without type supplements", "component", "recommendations", "error", err)
		return row
	}

	floor := mediaTypeFloor(len(row))
	var short []string
	for _, mediaType := range recommendableMediaTypes {
		if countMediaType(pool, mediaTypes, mediaType) < floor {
			short = append(short, mediaType)
		}
	}
	if len(short) > 0 {
		present, err := b.store.PresentMediaTypes(ctx, short, filter)
		if err != nil {
			slog.WarnContext(ctx, "main row media type check failed; querying every short type", "component", "recommendations", "error", err)
			present = make(map[string]struct{}, len(short))
			for _, mediaType := range short {
				present[mediaType] = struct{}{}
			}
		}
		for _, mediaType := range short {
			if _, ok := present[mediaType]; !ok {
				continue
			}
			extra, _, err := b.store.FindTasteProfileCandidatesByMediaType(ctx, embedding, excludeIDs, nil, limit, filter, mediaType)
			if err != nil {
				slog.WarnContext(ctx, "main row type supplement failed", "component", "recommendations", "media_type", mediaType, "error", err)
				continue
			}
			extra = b.withQualityPrior(ctx, extra, 0)
			if !b.aversion.empty() {
				// Supplements rank against the turned-away titles like the
				// rest of the row; a type whose embeddings cannot be read is
				// not supplemented rather than supplemented unranked.
				embMap, err := b.store.GetBatchEmbeddings(ctx, scoredItemIDs(extra))
				if err != nil {
					slog.WarnContext(ctx, "main row type supplement embeddings failed", "component", "recommendations", "media_type", mediaType, "error", err)
					continue
				}
				extra, _ = b.withAversion(extra, embMap, embedding)
			}
			pool = mergeScoredCandidates(pool, extra)
			for _, item := range extra {
				mediaTypes[item.MediaItemID] = mediaType
			}
		}
	}
	return placeTypeSupplements(row, pool, mediaTypes)
}

// placeTypeSupplements raises each media type pool offers to at least
// mediaTypeFloor(len(row)) items of row. The type's best candidates not yet in
// the row join its end, and as many items leave the part after the served
// window, last first, from types holding more than the floor. The served
// window and the order of every item kept are unchanged.
func placeTypeSupplements(row, pool []ScoredItem, mediaTypes map[string]string) []ScoredItem {
	if len(row) <= ServedRowSize {
		return row
	}
	floor := mediaTypeFloor(len(row))
	counts := make(map[string]int)
	inRow := make(map[string]struct{}, len(row))
	for _, item := range row {
		counts[mediaTypes[item.MediaItemID]]++
		inRow[item.MediaItemID] = struct{}{}
	}

	var added []ScoredItem
	for _, mediaType := range recommendableMediaTypes {
		for _, candidate := range pool {
			if counts[mediaType] >= floor {
				break
			}
			if mediaTypes[candidate.MediaItemID] != mediaType {
				continue
			}
			if _, ok := inRow[candidate.MediaItemID]; ok {
				continue
			}
			added = append(added, candidate)
			inRow[candidate.MediaItemID] = struct{}{}
			counts[mediaType]++
		}
	}
	if len(added) == 0 {
		return row
	}

	drop := make(map[int]struct{}, len(added))
	for i := len(row) - 1; i >= ServedRowSize && len(drop) < len(added); i-- {
		if mediaType := mediaTypes[row[i].MediaItemID]; counts[mediaType] > floor {
			drop[i] = struct{}{}
			counts[mediaType]--
		}
	}

	out := make([]ScoredItem, 0, len(row))
	for i, item := range row {
		if _, ok := drop[i]; !ok {
			out = append(out, item)
		}
	}
	return append(out, added[:len(drop)]...)
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
	builder := e.rowBuilder()
	if builder.aversion, err = e.loadAversion(ctx, userID, profileID, builder.now); err != nil {
		return nil, fmt.Errorf("get turned-away titles: %w", err)
	}
	return e.becauseYouWatched(ctx, builder, sourceItemID, limit, excluded, filter)
}

// becauseYouWatched is BecauseYouWatched with the exclusion set given, ranked
// by builder, whose aversion it applies.
func (e *Engine) becauseYouWatched(ctx context.Context, builder rowBuilder, sourceItemID string, limit int, excluded map[string]struct{}, filter catalog.AccessFilter) ([]ScoredItem, error) {
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

	// Get co-watch neighbors the viewer can see, of the source item's media
	// type like the embedding candidates: co-watch counts ebook reading too,
	// and a movie's row would otherwise take series its viewers also watched.
	cowatchPairs, _ := e.repo.GetCowatchNeighbors(ctx, sourceItemID, limit*3)
	cowatchMap, err := e.cowatchScores(ctx, cowatchPairs, filter, cowatchMediaTypes(sourceType, recommendableMediaTypes))
	if err != nil {
		return nil, fmt.Errorf("filter co-watch neighbors for because watched: %w", err)
	}

	// Blend scores, then drop excluded titles before MMR so they do not take
	// the row's places.
	blended := blendScores(embCandidates, cowatchMap, 0.7, 0.3)
	blended = excludeScoredItems(blended, excluded)
	blended = builder.withQualityPrior(ctx, blended, qualityMinSmallPool)

	// Apply MMR re-ranking.
	candidateIDs := make([]string, len(blended))
	for i, item := range blended {
		candidateIDs[i] = item.MediaItemID
	}
	embMap, err := e.repo.GetBatchEmbeddings(ctx, candidateIDs)
	if err != nil {
		if !builder.aversion.empty() {
			// Without embeddings the row could not rank against the
			// profile's turned-away titles.
			return nil, fmt.Errorf("because watched embeddings: %w", err)
		}
		embMap = nil
	}
	blended, _ = builder.withAversion(blended, embMap, embedding)
	result := applyMMR(blended, embMap, builder.lambda, limit)

	for i := range result {
		if result[i].Reason == "" {
			result[i].Reason = "because_you_watched"
		}
	}

	return result, nil
}

// cowatchMediaTypes is the media types a list anchored on an item of
// sourceType keeps its co-watch neighbors to: the source's own, or
// unknownSource when the source's type is unknown.
func cowatchMediaTypes(sourceType string, unknownSource []string) []string {
	if sourceType == "" {
		return unknownSource
	}
	return []string{sourceType}
}

// cowatchScores maps each co-watch neighbor filter admits, and whose media
// type is one of mediaTypes, to its Jaccard score. Co-watch neighbors bypass
// the access and type predicates of the embedding query, so they are checked
// here.
func (e *Engine) cowatchScores(ctx context.Context, pairs []CowatchPair, filter catalog.AccessFilter, mediaTypes []string) (map[string]float64, error) {
	scores := make(map[string]float64, len(pairs))
	if len(pairs) == 0 {
		return scores, nil
	}
	ids := make([]string, len(pairs))
	for i, p := range pairs {
		ids[i] = p.SimilarItemID
	}
	accessible, err := e.repo.filterItemIDs(ctx, ids, filter, mediaTypes)
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
