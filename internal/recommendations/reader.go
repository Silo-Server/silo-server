package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SectionKind identifies a single recommendation row reachable via a dedicated
// "see all" page. Stable strings used in URLs and the discover API response.
const (
	SectionKindForYouMain    = "for-you-main"
	SectionKindCluster       = "cluster"
	SectionKindSimilarUsers  = "similar-users"
	SectionKindPopular       = "popular"
	SectionKindRecentlyAdded = "recently-added"
	SectionKindTopRated      = "top-rated"
	SectionKindGenre         = "genre"
)

// ProfileRefreshRequester queues a profile-scoped refresh without blocking the caller.
type ProfileRefreshRequester interface {
	RequestProfileRefresh(ctx context.Context, userID int, profileID string)
}

// Reader assembles recommendation rows from cache-backed data sources.
type Reader struct {
	repo        *Repo
	ratingsRepo *catalog.RatingsRepo
	refresh     ProfileRefreshRequester
	signals     *SignalReader
}

// NewReader creates a cache-backed recommendations reader.
func NewReader(repo *Repo, ratingsRepo *catalog.RatingsRepo, refresh ProfileRefreshRequester, storeProvider userstore.UserStoreProvider) *Reader {
	return &Reader{
		repo:        repo,
		ratingsRepo: ratingsRepo,
		refresh:     refresh,
		signals:     NewSignalReader(repo, storeProvider),
	}
}

func (r *Reader) signalReader() *SignalReader {
	if r.signals != nil {
		return r.signals
	}
	return NewSignalReader(r.repo, nil)
}

// GetForYouMain returns the first row the recommendations page should display.
func (r *Reader) GetForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)
	rows, _, err := r.getForYouPageRows(ctx, userID, profileID, filter)
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, limit)
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// GetForYouRows returns the remaining recommendations-page rows after the main row.
func (r *Reader) GetForYouRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)
	rows, _, err := r.getForYouPageRows(ctx, userID, profileID, filter)
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, limit)
	if len(rows) <= 1 {
		return []ForYouRow{}, nil
	}
	return rows[1:], nil
}

// GetSimilarUsersLiked returns the cached collaborative row for the profile.
func (r *Reader) GetSimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	limit = normalizeRecommendationLimit(limit)

	items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		if r.refresh != nil {
			r.refresh.RequestProfileRefresh(ctx, userID, profileID)
		}
		return []ScoredItem{}, nil
	}

	rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{{
		Type:  "similar_users_liked",
		Label: "Fans Like You Also Enjoyed",
		Items: items,
	}}, filter)
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, limit)
	if len(rows) == 0 {
		return []ScoredItem{}, nil
	}
	return rows[0].Items, nil
}

// GetBecauseYouWatched returns a cached because-you-watched row, using the
// requested source item when provided or the most recent completed items when not.
func (r *Reader) GetBecauseYouWatched(ctx context.Context, userID int, profileID, sourceItemID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	limit = normalizeRecommendationLimit(limit)

	sourceIDs := []string{}
	if sourceItemID != "" {
		sourceIDs = append(sourceIDs, sourceItemID)
	} else {
		recentCompleted, err := r.signalReader().RecentCompletedItemIDs(ctx, userID, profileID, 3)
		if err != nil {
			return nil, err
		}
		sourceIDs = append(sourceIDs, recentCompleted...)
	}

	for _, sourceID := range sourceIDs {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, sourceID)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			continue
		}
		rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{{
			Type:  RecTypeBecauseWatched,
			Label: "Because You Watched",
			Items: items,
		}}, filter)
		if err != nil {
			return nil, err
		}
		rows = trimRows(rows, limit)
		if len(rows) == 0 {
			return []ScoredItem{}, nil
		}
		return rows[0].Items, nil
	}

	if r.refresh != nil {
		r.refresh.RequestProfileRefresh(ctx, userID, profileID)
	}
	return []ScoredItem{}, nil
}

// GetTasteMatchRow returns the strongest matching personalized cluster row for a genre,
// falling back to the global genre sampler when no personalized cluster matches.
// An empty genre auto-picks: every taste cluster qualifies (strongest first),
// and the global fallback uses the server-wide top genre.
func (r *Reader) GetTasteMatchRow(ctx context.Context, userID int, profileID, genre string, limit int, filter catalog.AccessFilter) (*ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)

	clusters, err := r.repo.GetTasteClusters(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}

	matching := make([]TasteCluster, 0, len(clusters))
	for _, cluster := range clusters {
		if genre == "" {
			matching = append(matching, cluster)
			continue
		}
		for _, dominantGenre := range cluster.DominantGenres {
			if dominantGenre == genre {
				matching = append(matching, cluster)
				break
			}
		}
	}
	sort.SliceStable(matching, func(i, j int) bool {
		return matching[i].TotalWeight > matching[j].TotalWeight
	})

	for _, cluster := range matching {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouClusterPrefix+itoa(cluster.ClusterIdx), "")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			if r.refresh != nil {
				r.refresh.RequestProfileRefresh(ctx, userID, profileID)
			}
			continue
		}

		rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{clusterRow(cluster, items)}, filter)
		if err != nil {
			return nil, err
		}
		rows = trimRows(rows, limit)
		if len(rows) == 0 {
			// This cluster's cached items were entirely filtered out (e.g.
			// access restrictions) — try the next-strongest cluster instead of
			// giving up before the global fallback below.
			continue
		}
		return &rows[0], nil
	}

	if genre == "" {
		topGenres, err := r.repo.GetTopGenres(ctx, 1)
		if err != nil || len(topGenres) == 0 {
			return nil, err
		}
		genre = topGenres[0]
	}

	items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeGenreSamplerPrefix+genre, "")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{{
		Type:  "genre_sampler",
		Label: "Top " + genre,
		Items: items,
	}}, filter)
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, limit)
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

func (r *Reader) getForYouPageRows(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) ([]ForYouRow, bool, error) {
	level := 0
	meta, err := r.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, false, err
	}
	if meta != nil {
		positiveSignals := 0
		for key, count := range meta.SignalCounts {
			switch key {
			case "rated_low", "watch_low":
			default:
				positiveSignals += count
			}
		}
		level = coldStartLevel(positiveSignals)
	}

	globalRows, err := r.getGlobalRows(ctx)
	if err != nil {
		return nil, false, err
	}

	clusterRows, missingClusters, err := r.getClusterRows(ctx, userID, profileID)
	if err != nil {
		return nil, false, err
	}

	personalRows := make([]ForYouRow, 0, 1+len(clusterRows))
	mainItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
	if err != nil {
		return nil, false, err
	}
	missingPersonalized := level > 0 && (len(mainItems) == 0 || missingClusters)
	if len(mainItems) > 0 {
		personalRows = append(personalRows, ForYouRow{
			Type:  "cluster",
			Label: "For You",
			Items: mainItems,
		})
	}
	personalRows = append(personalRows, clusterRows...)
	if level == 0 && len(personalRows) > 0 {
		level = 3
	}

	rows := mergePersonalizedAndColdStart(personalRows, globalRows, level)
	rows, err = r.filterRows(ctx, userID, profileID, rows, filter)
	if err != nil {
		return nil, false, err
	}

	if missingPersonalized && r.refresh != nil {
		r.refresh.RequestProfileRefresh(ctx, userID, profileID)
	}

	return rows, missingPersonalized, nil
}

func (r *Reader) getGlobalRows(ctx context.Context) ([]ForYouRow, error) {
	popular, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
	if err != nil {
		return nil, err
	}
	recentlyAdded, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeRecentlyAdded, "")
	if err != nil {
		return nil, err
	}
	topRated, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeTopRated, "")
	if err != nil {
		return nil, err
	}
	return buildColdStartRows(popular, recentlyAdded, topRated, map[string][]ScoredItem{}), nil
}

func (r *Reader) getClusterRows(ctx context.Context, userID int, profileID string) ([]ForYouRow, bool, error) {
	clusters, err := r.repo.GetTasteClusters(ctx, userID, profileID)
	if err != nil {
		return nil, false, err
	}
	if len(clusters) == 0 {
		return []ForYouRow{}, false, nil
	}

	rows := make([]ForYouRow, 0, len(clusters))
	missing := false
	for _, cluster := range clusters {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouClusterPrefix+itoa(cluster.ClusterIdx), "")
		if err != nil {
			return nil, false, err
		}
		if len(items) == 0 {
			missing = true
			continue
		}
		rows = append(rows, clusterRow(cluster, items))
	}
	return rows, missing, nil
}

func clusterRow(cluster TasteCluster, items []ScoredItem) ForYouRow {
	label := cluster.Label
	if label == "" {
		label = "For You"
	}
	return ForYouRow{
		Type:         "cluster",
		Label:        "Because you enjoy " + label,
		ClusterIndex: cluster.ClusterIdx,
		Items:        items,
	}
}

func (r *Reader) filterRows(ctx context.Context, userID int, profileID string, rows []ForYouRow, filter catalog.AccessFilter) ([]ForYouRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}

	watchedSet, err := r.signalReader().WatchedItemIDSet(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}

	itemIDs := make([]string, 0)
	for _, row := range rows {
		for _, item := range row.Items {
			itemIDs = append(itemIDs, item.MediaItemID)
		}
	}

	lowRatings := map[string]int{}
	if len(itemIDs) > 0 && r.ratingsRepo != nil {
		lowRatings, err = r.ratingsRepo.ListForItems(ctx, userID, profileID, itemIDs)
		if err != nil {
			return nil, err
		}
	}

	accessible := map[string]struct{}{}
	if len(itemIDs) > 0 {
		var err error
		accessible, err = r.repo.FilterAccessibleItemIDs(ctx, itemIDs, filter)
		if err != nil {
			return nil, err
		}
	}

	filteredRows := make([]ForYouRow, 0, len(rows))
	for _, row := range rows {
		filteredItems := make([]ScoredItem, 0, len(row.Items))
		for _, item := range row.Items {
			if _, ok := accessible[item.MediaItemID]; !ok {
				continue
			}
			if _, watched := watchedSet[item.MediaItemID]; watched {
				continue
			}
			if rating, rated := lowRatings[item.MediaItemID]; rated && rating <= 2 {
				continue
			}
			filteredItems = append(filteredItems, item)
		}
		if len(filteredItems) == 0 {
			continue
		}
		row.Items = filteredItems
		filteredRows = append(filteredRows, row)
	}

	return filteredRows, nil
}

// GetDiscoverRows assembles all rows for the discover/recommendations page.
// It combines personalized for-you rows, similar-users, and random genre-based
// popular rows. All data is read from cache — no live aggregation queries.
func (r *Reader) GetDiscoverRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)

	// 1. For-you rows (personalized + cold-start blended, already filtered).
	forYouRows, _, err := r.getForYouPageRows(ctx, userID, profileID, filter)
	if err != nil {
		return nil, err
	}

	// Track seen items for cross-row deduplication.
	seen := make(map[string]struct{})
	for i := range forYouRows {
		forYouRows[i].Items = deduplicateItems(forYouRows[i].Items, seen)
	}
	forYouRows = trimRows(forYouRows, limit)
	// Drop rows emptied by dedup.
	forYouRows = dropEmptyRows(forYouRows)

	// 2. Similar users row.
	var extraRows []ForYouRow
	similarItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
	if err != nil {
		return nil, err
	}
	if len(similarItems) > 0 {
		extraRows = append(extraRows, ForYouRow{
			Type:  "similar_users_liked",
			Label: "Users Like You Also Enjoyed",
			Items: similarItems,
		})
	}

	// 3. Genre sampler rows — pick random genres from global cache.
	genreSamplers, err := r.repo.ListCachedGenreSamplers(ctx)
	if err != nil {
		return nil, err
	}
	if len(genreSamplers) > 0 {
		// Exclude genres that overlap with the user's taste clusters.
		excludeGenres := make(map[string]struct{})
		clusters, clusterErr := r.repo.GetTasteClusters(ctx, userID, profileID)
		if clusterErr != nil {
			slog.Warn("GetDiscoverRows: failed to load taste clusters for genre exclusion", "user_id", userID, "profile_id", profileID, "error", clusterErr)
		}
		for _, c := range clusters {
			for _, g := range c.DominantGenres {
				excludeGenres[g] = struct{}{}
			}
		}

		var availableGenres []string
		for genre := range genreSamplers {
			if _, excluded := excludeGenres[genre]; !excluded {
				availableGenres = append(availableGenres, genre)
			}
		}

		// Deterministic daily shuffle based on profile + date.
		selected := selectDailyGenres(availableGenres, profileID, 4)
		for _, genre := range selected {
			items := genreSamplers[genre]
			extraRows = append(extraRows, ForYouRow{
				Type:  "genre_sampler",
				Label: "Popular in " + genre,
				Items: items,
			})
		}
	}

	// Filter extra rows (watched + low-rated) and deduplicate across all rows.
	extraRows, err = r.filterRows(ctx, userID, profileID, extraRows, filter)
	if err != nil {
		return nil, err
	}
	for i := range extraRows {
		extraRows[i].Items = deduplicateItems(extraRows[i].Items, seen)
	}
	extraRows = trimRows(extraRows, limit)
	extraRows = dropEmptyRows(extraRows)

	// Interleave: for-you rows first, then genre rows woven after every 2 for-you rows,
	// similar-users at the end.
	var result []ForYouRow
	var genreRows []ForYouRow
	var similarRow *ForYouRow
	for i := range extraRows {
		if extraRows[i].Type == "similar_users_liked" {
			similarRow = &extraRows[i]
		} else {
			genreRows = append(genreRows, extraRows[i])
		}
	}

	genreIdx := 0
	for i, row := range forYouRows {
		result = append(result, row)
		// Insert a genre row after every 2nd for-you row.
		if (i+1)%2 == 0 && genreIdx < len(genreRows) {
			result = append(result, genreRows[genreIdx])
			genreIdx++
		}
	}
	// Append remaining genre rows.
	for ; genreIdx < len(genreRows); genreIdx++ {
		result = append(result, genreRows[genreIdx])
	}
	// Similar users at the end.
	if similarRow != nil && len(similarRow.Items) > 0 {
		result = append(result, *similarRow)
	}

	return result, nil
}

// GetSection returns a single recommendation row identified by kind/key, used
// by dedicated "see all" pages. Returns nil if the row is unknown or empty
// after filtering. The label and type mirror what the discover/for-you
// endpoints would produce so consumers can render a consistent header.
func (r *Reader) GetSection(
	ctx context.Context,
	userID int,
	profileID, kind, key string,
	limit int,
	filter catalog.AccessFilter,
) (*ForYouRow, error) {
	if limit <= 0 || limit > CacheCandidateLimit {
		limit = CacheCandidateLimit
	}

	row, err := r.loadSectionRow(ctx, userID, profileID, kind, key)
	if err != nil || row == nil {
		return nil, err
	}

	rows, err := r.filterRows(ctx, userID, profileID, []ForYouRow{*row}, filter)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	filtered := rows[0]
	if len(filtered.Items) > limit {
		filtered.Items = filtered.Items[:limit]
	}
	return &filtered, nil
}

func (r *Reader) loadSectionRow(ctx context.Context, userID int, profileID, kind, key string) (*ForYouRow, error) {
	switch kind {
	case SectionKindForYouMain:
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: "cluster", Label: "For You", Items: items}, nil

	case SectionKindCluster:
		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("invalid cluster index %q: %w", key, err)
		}
		clusters, err := r.repo.GetTasteClusters(ctx, userID, profileID)
		if err != nil {
			return nil, err
		}
		var match *TasteCluster
		for i := range clusters {
			if clusters[i].ClusterIdx == idx {
				match = &clusters[i]
				break
			}
		}
		if match == nil {
			return nil, nil
		}
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouClusterPrefix+itoa(idx), "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		row := clusterRow(*match, items)
		return &row, nil

	case SectionKindSimilarUsers:
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{
			Type:  "similar_users_liked",
			Label: "Users Like You Also Enjoyed",
			Items: items,
		}, nil

	case SectionKindPopular:
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: RecTypePopular, Label: "Popular on This Server", Items: items}, nil

	case SectionKindRecentlyAdded:
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeRecentlyAdded, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: RecTypeRecentlyAdded, Label: "Recently Added", Items: items}, nil

	case SectionKindTopRated:
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeTopRated, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: RecTypeTopRated, Label: "Top Rated", Items: items}, nil

	case SectionKindGenre:
		if key == "" {
			return nil, fmt.Errorf("genre section requires a key")
		}
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeGenreSamplerPrefix+key, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: "genre_sampler", Label: "Popular in " + key, Items: items}, nil
	}

	return nil, nil
}

// selectDailyGenres picks up to n genres from the available list using a
// deterministic daily seed so the selection is stable within a day for a given profile.
func selectDailyGenres(genres []string, profileID string, n int) []string {
	if len(genres) == 0 {
		return nil
	}
	if n > len(genres) {
		n = len(genres)
	}

	// Copy to avoid mutating the caller's slice.
	shuffled := make([]string, len(genres))
	copy(shuffled, genres)
	genres = shuffled

	// Sort for determinism before shuffling.
	sort.Strings(genres)

	// Simple daily seed from profile ID + date.
	now := time.Now().UTC()
	seed := int64(now.Year())*10000 + int64(now.Month())*100 + int64(now.Day())
	for _, b := range profileID {
		seed = seed*31 + int64(b)
	}

	// Fisher-Yates shuffle with deterministic seed.
	for i := len(genres) - 1; i > 0; i-- {
		seed = (seed*1103515245 + 12345) & 0x7fffffff
		j := int(seed) % (i + 1)
		genres[i], genres[j] = genres[j], genres[i]
	}

	return genres[:n]
}

// deduplicateItems removes items whose MediaItemID is already in the seen set,
// and adds surviving items to the set.
func deduplicateItems(items []ScoredItem, seen map[string]struct{}) []ScoredItem {
	result := make([]ScoredItem, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item.MediaItemID]; ok {
			continue
		}
		seen[item.MediaItemID] = struct{}{}
		result = append(result, item)
	}
	return result
}

// dropEmptyRows removes rows with no items.
func dropEmptyRows(rows []ForYouRow) []ForYouRow {
	result := make([]ForYouRow, 0, len(rows))
	for _, row := range rows {
		if len(row.Items) > 0 {
			result = append(result, row)
		}
	}
	return result
}

func trimRows(rows []ForYouRow, limit int) []ForYouRow {
	if limit <= 0 {
		return rows
	}
	for i := range rows {
		if len(rows[i].Items) > limit {
			rows[i].Items = rows[i].Items[:limit]
		}
	}
	return rows
}

func normalizeRecommendationLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > 20 {
		return 20
	}
	return limit
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
