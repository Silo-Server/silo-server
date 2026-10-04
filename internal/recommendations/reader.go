package recommendations

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
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

// ReadRefreshRequester queues a refresh for a profile whose cached rows a read
// found missing, without blocking the caller. *Worker limits these to one per
// profile per interval, because a row that came out empty looks the same to a
// read as a row that was never built.
type ReadRefreshRequester interface {
	RequestReadRefresh(ctx context.Context, userID int, profileID string)
	// ReadRefreshDue reports whether RequestReadRefresh would queue a
	// refresh for the profile now.
	ReadRefreshDue(userID int, profileID string) bool
}

// readerRepo is the part of *Repo the Reader reads.
type readerRepo interface {
	GetTasteProfileMeta(ctx context.Context, userID int, profileID string) (*TasteProfileMeta, error)
	GetTasteClusterMeta(ctx context.Context, userID int, profileID string) ([]TasteCluster, error)
	GetRecommendationCache(ctx context.Context, userID int, profileID, recType, sourceItemID string) ([]ScoredItem, error)
	ListCachedGenreSamplers(ctx context.Context) (map[string][]ScoredItem, error)
	GetTopGenres(ctx context.Context, limit int) ([]string, error)
	FilterAccessibleItemIDs(ctx context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]struct{}, error)
}

// Reader assembles recommendation rows from cache-backed data sources.
type Reader struct {
	repo        readerRepo
	ratingsRepo *catalog.RatingsRepo
	refresh     ReadRefreshRequester
	signals     *SignalReader
}

// NewReader creates a cache-backed recommendations reader.
func NewReader(repo *Repo, ratingsRepo *catalog.RatingsRepo, refresh ReadRefreshRequester, storeProvider userstore.UserStoreProvider) *Reader {
	return &Reader{
		repo:        repo,
		ratingsRepo: ratingsRepo,
		refresh:     refresh,
		signals:     NewSignalReader(repo, storeProvider),
	}
}

// WithUserStoreOutsidePostgres records whether the user store keeps watch
// progress, favorites and watchlist outside Postgres (the SQLite backend), and
// returns the reader. Signal checks then read the store.
func (r *Reader) WithUserStoreOutsidePostgres(outside bool) *Reader {
	if r != nil && r.signals != nil {
		r.signals.storeOutsidePostgres = outside
	}
	return r
}

// requestRefresh asks for the profile's cached rows to be rebuilt.
func (r *Reader) requestRefresh(ctx context.Context, userID int, profileID string) {
	if r.refresh != nil {
		r.refresh.RequestReadRefresh(ctx, userID, profileID)
	}
}

// requestRefreshIfSignals asks for a refresh of a profile that has no taste
// profile yet, when it has signals to build one from. Its first refresh
// request lives in one server's memory and can be lost; this lets a later
// read recover it. A profile with nothing to build from asks for nothing,
// and the signals are checked only when a refresh would be let through.
func (r *Reader) requestRefreshIfSignals(ctx context.Context, userID int, profileID string) {
	if r.refresh == nil || !r.refresh.ReadRefreshDue(userID, profileID) {
		return
	}
	has, err := r.signalReader().HasSignals(ctx, userID, profileID)
	if err != nil {
		slog.WarnContext(ctx, "checking a profile's recommendation signals failed", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", err)
		return
	}
	if has {
		r.refresh.RequestReadRefresh(ctx, userID, profileID)
	}
}

func (r *Reader) signalReader() *SignalReader {
	if r.signals != nil {
		return r.signals
	}
	repo, _ := r.repo.(signalRepo)
	return NewSignalReader(repo, nil)
}

// GetForYouMain returns the first row the recommendations page should display.
func (r *Reader) GetForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*ForYouRow, error) {
	return r.forYouMain(ctx, userID, profileID, normalizeRecommendationLimit(limit), filter)
}

func (r *Reader) forYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*ForYouRow, error) {
	rows, err := r.getForYouPageRows(ctx, userID, profileID, filter)
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
	rows, err := r.getForYouPageRows(ctx, userID, profileID, filter)
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, limit)
	if len(rows) <= 1 {
		return []ForYouRow{}, nil
	}
	return rows[1:], nil
}

// GetForYouPage returns every recommendations-page row, the main row first.
// It is GetForYouMain and GetForYouRows in one read.
func (r *Reader) GetForYouPage(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)
	rows, err := r.getForYouPageRows(ctx, userID, profileID, filter)
	if err != nil {
		return nil, err
	}
	return trimRows(rows, limit), nil
}

// GetSimilarUsersLiked returns the cached collaborative row for the profile.
func (r *Reader) GetSimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	return r.similarUsersLiked(ctx, userID, profileID, normalizeRecommendationLimit(limit), filter)
}

func (r *Reader) similarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ScoredItem, error) {
	items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
	if err != nil {
		return nil, err
	}
	if items == nil {
		// No row is cached. A built row may be empty (too few peer accounts),
		// and rebuilding it would not change that.
		r.requestRefresh(ctx, userID, profileID)
		return []ScoredItem{}, nil
	}
	if len(items) == 0 {
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

// Section reads serve home and library sections. Each returns a row's whole
// candidate pool, every cached candidate left after filtering (at most
// CacheCandidateLimit), rather than a page of the public limit: the section
// scopes the row to its libraries and trims it to its own item limit
// afterwards, and trimming first would leave a library's row short or empty.

// SectionForYouMain is GetForYouMain's row for a home or library section.
func (r *Reader) SectionForYouMain(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) (*ForYouRow, error) {
	return r.forYouMain(ctx, userID, profileID, CacheCandidateLimit, filter)
}

// SectionSimilarUsersLiked is GetSimilarUsersLiked's row for a home or
// library section.
func (r *Reader) SectionSimilarUsersLiked(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) ([]ScoredItem, error) {
	return r.similarUsersLiked(ctx, userID, profileID, CacheCandidateLimit, filter)
}

// SectionBecauseYouWatched returns a cached Because You Watched row for a
// home or library section and the anchor it was built from. It uses
// sourceItemID as the anchor when given, else the profile's most recent
// completed titles in turn: the first whose row keeps an item after
// filtering wins. When libraryIDs is not nil an item must also be in one of
// those libraries, so a section picks an anchor with recommendations in its
// own scope. A caller must access-check the anchor before displaying it.
func (r *Reader) SectionBecauseYouWatched(ctx context.Context, userID int, profileID, sourceItemID string, libraryIDs []int, filter catalog.AccessFilter) ([]ScoredItem, string, error) {
	sourceIDs := []string{}
	if sourceItemID != "" {
		sourceIDs = append(sourceIDs, sourceItemID)
	} else {
		recentCompleted, err := r.signalReader().RecentCompletedItemIDs(ctx, userID, profileID, 3)
		if err != nil {
			return nil, "", err
		}
		sourceIDs = append(sourceIDs, recentCompleted...)
	}

	read := r.newRowRead(userID, profileID, scopeToLibraries(filter, libraryIDs))
	cached := false
	for _, sourceID := range sourceIDs {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, sourceID)
		if err != nil {
			return nil, "", err
		}
		if len(items) == 0 {
			continue
		}
		cached = true
		rows, err := read.filter(ctx, []ForYouRow{{
			Type:  RecTypeBecauseWatched,
			Label: "Because You Watched",
			Items: items,
		}})
		if err != nil {
			return nil, "", err
		}
		if len(rows) == 0 {
			// Everything this anchor recommends is filtered out or out of
			// scope; the next anchor may still fill the row.
			continue
		}
		rows = trimRows(rows, CacheCandidateLimit)
		return rows[0].Items, sourceID, nil
	}

	// Rows are built only for completed titles, so a profile with none has
	// nothing a refresh could add, and a refresh does not bring back what
	// filtering removed from a cached row.
	if len(sourceIDs) > 0 && !cached {
		r.requestRefresh(ctx, userID, profileID)
	}
	return []ScoredItem{}, "", nil
}

// scopeToLibraries narrows filter so an item must also be in one of
// libraryIDs, unless libraryIDs is nil. An empty libraryIDs allows nothing.
func scopeToLibraries(filter catalog.AccessFilter, libraryIDs []int) catalog.AccessFilter {
	if libraryIDs == nil {
		return filter
	}
	scoped := filter
	if filter.AllowedLibraryIDs == nil {
		scoped.AllowedLibraryIDs = slices.Clone(libraryIDs)
		return scoped
	}
	scoped.AllowedLibraryIDs = make([]int, 0, len(libraryIDs))
	for _, id := range libraryIDs {
		if slices.Contains(filter.AllowedLibraryIDs, id) {
			scoped.AllowedLibraryIDs = append(scoped.AllowedLibraryIDs, id)
		}
	}
	return scoped
}

// SectionTasteMatchRow returns the strongest matching personalized cluster
// row for a genre for a home or library section, falling back to the global
// genre sampler when no personalized cluster matches. An empty genre
// auto-picks: every taste cluster qualifies (strongest first), and the global
// fallback uses the server-wide top genre.
func (r *Reader) SectionTasteMatchRow(ctx context.Context, userID int, profileID, genre string, filter catalog.AccessFilter) (*ForYouRow, error) {
	clusters, err := r.repo.GetTasteClusterMeta(ctx, userID, profileID)
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

	read := r.newRowRead(userID, profileID, filter)
	for _, cluster := range matching {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouClusterPrefix+itoa(cluster.ClusterIdx), "")
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			// A row cached empty was built; only a missing one needs a
			// refresh.
			if items == nil {
				r.requestRefresh(ctx, userID, profileID)
			}
			continue
		}

		rows, err := read.filter(ctx, []ForYouRow{clusterRow(cluster, items)})
		if err != nil {
			return nil, err
		}
		rows = trimRows(rows, CacheCandidateLimit)
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
	rows, err := read.filter(ctx, []ForYouRow{{
		Type:  "genre_sampler",
		Label: "Top " + genre,
		Items: items,
	}})
	if err != nil {
		return nil, err
	}
	rows = trimRows(rows, CacheCandidateLimit)
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// getForYouPageRows merges the profile's cached personal rows with the global
// rows by its cold-start level. A profile with no positive title, or no taste
// profile, is level 0 and gets only the global rows, even when personal rows
// from an earlier taste profile are still cached.
func (r *Reader) getForYouPageRows(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) ([]ForYouRow, error) {
	return r.newRowRead(userID, profileID, filter).forYouPageRows(ctx)
}

func (rr *rowRead) forYouPageRows(ctx context.Context) ([]ForYouRow, error) {
	r, userID, profileID := rr.reader, rr.userID, rr.profileID
	meta, err := r.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	level := coldStartLevelOf(meta)

	globalRows, err := r.getGlobalRows(ctx)
	if err != nil {
		return nil, err
	}

	clusterRows, missingClusters, err := r.getClusterRows(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}

	personalRows := make([]ForYouRow, 0, 1+len(clusterRows))
	mainItems, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
	if err != nil {
		return nil, err
	}
	// Positive signals without personal rows ask for them even at level 0:
	// titles that had no embedding at the last refresh may have one now.
	missingPersonalized := positiveSignalCount(meta) > 0 && (len(mainItems) == 0 || missingClusters)
	if len(mainItems) > 0 {
		personalRows = append(personalRows, ForYouRow{
			Type:  clusterRowType,
			Label: "For You",
			Items: mainItems,
		})
	}
	personalRows = append(personalRows, clusterRows...)

	rows := mergePersonalizedAndColdStart(personalRows, globalRows, level)
	rows, err = rr.filter(ctx, rows)
	if err != nil {
		return nil, err
	}

	switch {
	case missingPersonalized:
		r.requestRefresh(ctx, userID, profileID)
	case meta == nil:
		r.requestRefreshIfSignals(ctx, userID, profileID)
	}

	return rows, nil
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
	clusters, err := r.repo.GetTasteClusterMeta(ctx, userID, profileID)
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
			// A row cached empty (the main row took its titles) was built;
			// only a missing one is.
			missing = missing || items == nil
			continue
		}
		rows = append(rows, clusterRow(cluster, items))
	}
	return rows, missing, nil
}

// clusterRow titles a cached cluster row from its items, which carry the title
// of the build that cached them; the cluster table may have been rebuilt since.
// Items cached without a cluster title fall back to the cluster's label.
func clusterRow(cluster TasteCluster, items []ScoredItem) ForYouRow {
	title := clusterTitle(cluster.Label)
	if len(items) > 0 {
		if label, ok := strings.CutPrefix(items[0].Reason, clusterTitlePrefix); ok && label != "" {
			title = items[0].Reason
		}
	}
	return ForYouRow{
		Type:         clusterRowType,
		Label:        title,
		ClusterIndex: cluster.ClusterIdx,
		Items:        items,
	}
}

// filterRows drops from rows the items the profile cannot access, those in its
// recommendation exclusion set (watched and favorited titles), and those it
// rated 2 or lower, then drops the rows left empty. Rows cached before a title
// was watched or favorited are cleaned here.
func (r *Reader) filterRows(ctx context.Context, userID int, profileID string, rows []ForYouRow, filter catalog.AccessFilter) ([]ForYouRow, error) {
	return r.newRowRead(userID, profileID, filter).filter(ctx, rows)
}

// rowRead filters the rows one request reads for a profile. The profile's
// exclusion set is loaded once, on first use, however many rows, anchors or
// clusters the request goes through.
type rowRead struct {
	reader    *Reader
	userID    int
	profileID string
	access    catalog.AccessFilter
	excluded  map[string]struct{}
}

func (r *Reader) newRowRead(userID int, profileID string, access catalog.AccessFilter) *rowRead {
	return &rowRead{reader: r, userID: userID, profileID: profileID, access: access}
}

// exclusions returns the profile's recommendation exclusion set, loading it on
// the first call.
func (rr *rowRead) exclusions(ctx context.Context) (map[string]struct{}, error) {
	if rr.excluded != nil {
		return rr.excluded, nil
	}
	excluded, err := rr.reader.signalReader().RecommendationExclusionSet(ctx, rr.userID, rr.profileID)
	if err != nil {
		return nil, err
	}
	if excluded == nil {
		// A non-nil set records that it was loaded.
		excluded = map[string]struct{}{}
	}
	rr.excluded = excluded
	return excluded, nil
}

// filter is filterRows for this read.
func (rr *rowRead) filter(ctx context.Context, rows []ForYouRow) ([]ForYouRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	r, userID, profileID := rr.reader, rr.userID, rr.profileID

	excluded, err := rr.exclusions(ctx)
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
		accessible, err = r.repo.FilterAccessibleItemIDs(ctx, itemIDs, rr.access)
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
			if _, skip := excluded[item.MediaItemID]; skip {
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
	read := r.newRowRead(userID, profileID, filter)

	// 1. For-you rows (personalized + cold-start blended, already filtered).
	forYouRows, err := read.forYouPageRows(ctx)
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
		clusters, clusterErr := r.repo.GetTasteClusterMeta(ctx, userID, profileID)
		if clusterErr != nil {
			slog.WarnContext(ctx, "GetDiscoverRows: failed to load taste clusters for genre exclusion", "component", "recommendations", "user_id", userID, "profile_id", profileID, "error", clusterErr)
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

	// Filter extra rows (excluded + low-rated) and deduplicate across all rows.
	extraRows, err = read.filter(ctx, extraRows)
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
		return &ForYouRow{Type: clusterRowType, Label: "For You", Items: items}, nil

	case SectionKindCluster:
		idx, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("invalid cluster index %q: %w", key, err)
		}
		clusters, err := r.repo.GetTasteClusterMeta(ctx, userID, profileID)
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

// clusterRowType is the type the API reports for the main row and every
// taste-cluster row.
const clusterRowType = "cluster"

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

// Public row reads answer defaultRecommendationLimit items per row unless
// asked for a positive limit, and at most maxRecommendationLimit, the v2
// limit parameter's maximum.
const (
	defaultRecommendationLimit = ServedRowSize
	maxRecommendationLimit     = 50
)

func normalizeRecommendationLimit(limit int) int {
	if limit <= 0 {
		return defaultRecommendationLimit
	}
	return min(limit, maxRecommendationLimit)
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
