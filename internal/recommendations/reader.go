package recommendations

import (
	"cmp"
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
	FilterRecommendableItemIDs(ctx context.Context, itemIDs []string, filter catalog.AccessFilter) (map[string]struct{}, error)
	ListDefaultRowItems(ctx context.Context, filter catalog.AccessFilter, kind string, exclude []string, limit int) ([]ScoredItem, error)
}

// Reader assembles recommendation rows from cache-backed data sources.
type Reader struct {
	repo readerRepo
	// ratingsRepo reads the profile's ratings; nil leaves rated titles in.
	ratingsRepo itemRatingReader
	refresh     ReadRefreshRequester
	signals     *SignalReader
	// now is the clock personal rows are rotated by; nil is time.Now. Its
	// location sets the day a rotation lasts.
	now func() time.Time
	// personalOff serves no personal rows (see WithPersonalRows).
	personalOff bool
}

// NewReader creates a cache-backed recommendations reader.
func NewReader(repo *Repo, ratingsRepo *catalog.RatingsRepo, refresh ReadRefreshRequester, storeProvider userstore.UserStoreProvider) *Reader {
	return &Reader{
		repo:        repo,
		ratingsRepo: ratingReader(ratingsRepo),
		refresh:     refresh,
		signals:     NewSignalReader(repo, storeProvider),
	}
}

// ratingReader is repo as an itemRatingReader, nil when repo is nil.
func ratingReader(repo *catalog.RatingsRepo) itemRatingReader {
	if repo == nil {
		return nil
	}
	return repo
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

// WithPersonalRows sets whether the reader serves personal rows (the main
// For You row, cluster rows, Because You Watched and Similar Users) and
// returns it. Recommendations are disabled without them: reads then serve
// the global and default rows only, ignoring personal rows still cached from
// before.
func (r *Reader) WithPersonalRows(enabled bool) *Reader {
	if r != nil {
		r.personalOff = !enabled
	}
	return r
}

// today is the time a read's rotation is for, in the server's time zone.
func (r *Reader) today() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
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
	limit = normalizeRecommendationLimit(limit)
	return r.forYouMain(ctx, userID, profileID, limit, limit, filter)
}

// forYouMain returns the page's first row, rotated for a window of served
// items and trimmed to limit.
func (r *Reader) forYouMain(ctx context.Context, userID int, profileID string, served, limit int, filter catalog.AccessFilter) (*ForYouRow, error) {
	rows, err := r.getForYouPageRows(ctx, userID, profileID, served, filter)
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
	rows, err := r.getForYouPageRows(ctx, userID, profileID, limit, filter)
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
	rows, err := r.getForYouPageRows(ctx, userID, profileID, limit, filter)
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
	if r.personalOff {
		return []ScoredItem{}, nil
	}
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
		Type:  RecTypeSimilarUsersLiked,
		Label: similarUsersLabel,
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

// SectionForYouMain is GetForYouMain's row for a home or library section,
// rotated like a page of the default size.
func (r *Reader) SectionForYouMain(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) (*ForYouRow, error) {
	return r.forYouMain(ctx, userID, profileID, ServedRowSize, CacheCandidateLimit, filter)
}

// SectionForYouFill returns what a library's For You section serves after
// the main row's titles in that library. The main row ranks every title the
// profile can see, so a library holding a small share of them can be left
// with few or none, while a taste cluster or a watched title of the profile
// sits in it. The fill is the profile's other personal rows in one list: the
// taste-cluster rows, heaviest cluster first, then the Because You Watched
// rows of its anchors (see anchorItemIDs), most recent first, then Similar
// Users. Each row keeps its rank order, each item appears once at its first
// place, and the list is filtered for the viewer like every row. A missing
// row asks for no refresh: reading the main row already does. With personal
// rows off the fill is empty.
func (r *Reader) SectionForYouFill(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) ([]ScoredItem, error) {
	if r.personalOff {
		return []ScoredItem{}, nil
	}
	clusters, rows, _, err := r.getClusterRows(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	weights := make(map[int]float64, len(clusters))
	for _, cluster := range clusters {
		weights[cluster.ClusterIdx] = cluster.TotalWeight
	}
	slices.SortStableFunc(rows, func(a, b ForYouRow) int {
		return cmp.Compare(weights[b.ClusterIndex], weights[a.ClusterIndex])
	})

	anchors, err := anchorItemIDs(ctx, r.signalReader(), r.ratingsRepo, userID, profileID, becauseYouWatchedAnchors)
	if err != nil {
		return nil, err
	}
	for _, anchor := range anchors {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, anchor)
		if err != nil {
			return nil, err
		}
		rows = append(rows, ForYouRow{Type: RecTypeBecauseWatched, Items: items})
	}
	similar, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, "")
	if err != nil {
		return nil, err
	}
	rows = append(rows, ForYouRow{Type: RecTypeSimilarUsersLiked, Items: similar})

	rows, err = r.filterRows(ctx, userID, profileID, rows, filter)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	fill := []ScoredItem{}
	for _, row := range rows {
		fill = append(fill, deduplicateItems(row.Items, seen, 0)...)
	}
	return fill, nil
}

// SectionSimilarUsersLiked is GetSimilarUsersLiked's row for a home or
// library section.
func (r *Reader) SectionSimilarUsersLiked(ctx context.Context, userID int, profileID string, filter catalog.AccessFilter) ([]ScoredItem, error) {
	return r.similarUsersLiked(ctx, userID, profileID, CacheCandidateLimit, filter)
}

// SectionBecauseYouWatched returns a cached Because You Watched row for a
// home or library section and the anchor it was built from. It uses
// sourceItemID as the anchor when given, else the profile's anchors (see
// anchorItemIDs) in turn: the first whose row keeps an item after filtering
// wins. When libraryIDs is not nil an item must also be in one of
// those libraries, so a section picks an anchor with recommendations in its
// own scope. A caller must access-check the anchor before displaying it.
func (r *Reader) SectionBecauseYouWatched(ctx context.Context, userID int, profileID, sourceItemID string, libraryIDs []int, filter catalog.AccessFilter) ([]ScoredItem, string, error) {
	rows, err := r.becauseYouWatchedRows(ctx, userID, profileID, sourceItemID, 1, scopeToLibraries(filter, libraryIDs))
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 0 {
		return []ScoredItem{}, "", nil
	}
	row := trimRows(rows, CacheCandidateLimit)[0]
	return row.Items, row.AnchorItemID, nil
}

// GetBecauseYouWatchedRows returns up to maxRows cached Because You Watched
// rows filtered for the viewer, one per anchor, the most recent anchor first,
// each holding at most limit items. A row names its anchor in AnchorItemID; a
// caller must access-check the anchor before displaying it.
func (r *Reader) GetBecauseYouWatchedRows(ctx context.Context, userID int, profileID string, maxRows, limit int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	rows, err := r.becauseYouWatchedRows(ctx, userID, profileID, "", maxRows, filter)
	if err != nil {
		return nil, err
	}
	return trimRows(rows, normalizeRecommendationLimit(limit)), nil
}

// becauseYouWatchedRows reads the cached Because You Watched rows of up to
// maxRows anchors, filtered with filter. It uses sourceItemID as the anchor
// when given, else the profile's anchors (see anchorItemIDs) in turn,
// passing over an anchor whose row filters empty.
func (r *Reader) becauseYouWatchedRows(ctx context.Context, userID int, profileID, sourceItemID string, maxRows int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	if maxRows <= 0 || r.personalOff {
		return nil, nil
	}
	sourceIDs := []string{}
	if sourceItemID != "" {
		sourceIDs = append(sourceIDs, sourceItemID)
	} else {
		anchors, err := anchorItemIDs(ctx, r.signalReader(), r.ratingsRepo, userID, profileID, becauseYouWatchedAnchors)
		if err != nil {
			return nil, err
		}
		sourceIDs = append(sourceIDs, anchors...)
	}

	read := r.newRowRead(userID, profileID, filter)
	cached := false
	var rows []ForYouRow
	for _, sourceID := range sourceIDs {
		if len(rows) >= maxRows {
			break
		}
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeBecauseWatched, sourceID)
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			continue
		}
		cached = true
		filtered, err := read.filter(ctx, []ForYouRow{{
			Type:         RecTypeBecauseWatched,
			Label:        becauseYouWatchedRowLabel,
			Items:        items,
			AnchorItemID: sourceID,
		}})
		if err != nil {
			return nil, err
		}
		// A row filtered empty, everything it recommends out of reach or out
		// of scope, gives way to the next anchor.
		rows = append(rows, filtered...)
	}

	// Rows are built only for completed titles, so a profile with none has
	// nothing a refresh could add, and a refresh does not bring back what
	// filtering removed from a cached row.
	if len(sourceIDs) > 0 && !cached {
		r.requestRefresh(ctx, userID, profileID)
	}
	return rows, nil
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
	var clusters []TasteCluster
	if !r.personalOff {
		var err error
		if clusters, err = r.repo.GetTasteClusterMeta(ctx, userID, profileID); err != nil {
			return nil, err
		}
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
		rows = trimRows(read.rotatePersonalRows(rows, ServedRowSize), CacheCandidateLimit)
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
		Type:  genreSamplerRowType,
		Label: genreRowLabel(genre),
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
// from an earlier taste profile are still cached. The personal rows are
// rotated for a window of served items (see rotateTail), and a cluster row
// whose title a heavier one already has is told apart or hidden (see
// distinguishClusterRows). The rows are not trimmed.
func (r *Reader) getForYouPageRows(ctx context.Context, userID int, profileID string, served int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	return r.newRowRead(userID, profileID, filter).forYouPageRows(ctx, false, served)
}

// forYouPageRows is getForYouPageRows for this read. With liveDefaults the
// global rows are the cached Popular row and the live default rows (see
// defaultRows) rather than the cached Recently Added row.
func (rr *rowRead) forYouPageRows(ctx context.Context, liveDefaults bool, served int) ([]ForYouRow, error) {
	r, userID, profileID := rr.reader, rr.userID, rr.profileID
	if r.personalOff {
		globalRows, err := rr.globalRows(ctx, liveDefaults)
		if err != nil {
			return nil, err
		}
		return rr.filter(ctx, globalRows)
	}
	meta, err := r.repo.GetTasteProfileMeta(ctx, userID, profileID)
	if err != nil {
		return nil, err
	}
	level := coldStartLevelOf(meta)

	globalRows, err := rr.globalRows(ctx, liveDefaults)
	if err != nil {
		return nil, err
	}

	clusters, clusterRows, missingClusters, err := r.getClusterRows(ctx, userID, profileID)
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
			Type:        clusterRowType,
			Label:       ForYouLabel,
			Items:       mainItems,
			personalKey: RecTypeForYouMain,
		})
	}
	personalRows = append(personalRows, clusterRows...)

	rows := mergePersonalizedAndColdStart(personalRows, globalRows, level)
	rows, err = rr.filter(ctx, rows)
	if err != nil {
		return nil, err
	}
	// A hidden cluster row was built, so it does not count as missing.
	rows = distinguishClusterRows(rr.rotatePersonalRows(rows, served), clusters, served)

	switch {
	case missingPersonalized:
		r.requestRefresh(ctx, userID, profileID)
	case meta == nil:
		r.requestRefreshIfSignals(ctx, userID, profileID)
	}

	return rows, nil
}

// globalRows reads the rows every profile is offered: the cached Popular
// row, then the cached Recently Added row or, with liveDefaults, the live
// default rows. The rows are not yet filtered for the viewer.
func (rr *rowRead) globalRows(ctx context.Context, liveDefaults bool) ([]ForYouRow, error) {
	repo := rr.reader.repo
	popular, err := repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
	if err != nil {
		return nil, err
	}
	if liveDefaults {
		defaults, err := rr.defaultRows(ctx, CacheCandidateLimit)
		if err != nil {
			return nil, err
		}
		return append(buildColdStartRows(popular, nil, nil), defaults...), nil
	}
	recentlyAdded, err := repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeRecentlyAdded, "")
	if err != nil {
		return nil, err
	}
	return buildColdStartRows(popular, recentlyAdded, nil), nil
}

// The live default rows are titles the viewer can see and has not watched or
// favorited, queried at read time rather than cached, so they show on a
// fresh server, with recommendations disabled, and for a restricted profile
// the global cache holds little for. Discover and the section pages serve
// them; home and library sections do not, as a library has its own shelves.
// defaultRowKinds lists them in display order: a quality ranking first, since
// a freshly imported library's additions are in scan order.
var defaultRowKinds = []string{RecTypeTopRated, RecTypeRecentlyAdded}

// defaultRows reads the live default rows of up to limit items each,
// dropping empty ones. Like the cached rows, they are not yet filtered.
func (rr *rowRead) defaultRows(ctx context.Context, limit int) ([]ForYouRow, error) {
	rows := make([]ForYouRow, 0, len(defaultRowKinds))
	for _, kind := range defaultRowKinds {
		row, err := rr.defaultRow(ctx, kind, limit)
		if err != nil {
			return nil, err
		}
		if len(row.Items) > 0 {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// defaultRow reads one live default row, RecTypeTopRated or
// RecTypeRecentlyAdded, leaving out the profile's exclusion set in the query
// so the row fills past it.
func (rr *rowRead) defaultRow(ctx context.Context, kind string, limit int) (ForYouRow, error) {
	excluded, err := rr.exclusionSet(ctx)
	if err != nil {
		return ForYouRow{}, err
	}
	if rr.excludedIDs == nil {
		rr.excludedIDs = make([]string, 0, len(excluded))
		for id := range excluded {
			rr.excludedIDs = append(rr.excludedIDs, id)
		}
	}
	items, err := rr.reader.repo.ListDefaultRowItems(ctx, rr.access, kind, rr.excludedIDs, limit)
	if err != nil {
		return ForYouRow{}, err
	}
	label := recentlyAddedLabel
	if kind == RecTypeTopRated {
		label = highlyRatedLabel
	}
	return ForYouRow{Type: kind, Label: label, Items: items}, nil
}

// getClusterRows reads the profile's clusters and their cached rows, and
// reports whether a cluster has no cached row.
func (r *Reader) getClusterRows(ctx context.Context, userID int, profileID string) ([]TasteCluster, []ForYouRow, bool, error) {
	clusters, err := r.repo.GetTasteClusterMeta(ctx, userID, profileID)
	if err != nil {
		return nil, nil, false, err
	}
	if len(clusters) == 0 {
		return nil, []ForYouRow{}, false, nil
	}

	rows := make([]ForYouRow, 0, len(clusters))
	missing := false
	for _, cluster := range clusters {
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouClusterPrefix+itoa(cluster.ClusterIdx), "")
		if err != nil {
			return nil, nil, false, err
		}
		if len(items) == 0 {
			// A row cached empty (the main row took its titles) was built;
			// only a missing one is.
			missing = missing || items == nil
			continue
		}
		rows = append(rows, clusterRow(cluster, items))
	}
	return clusters, rows, missing, nil
}

// clusterRow titles a cached cluster row from its items, which carry the title
// of the build that cached them; the cluster table may have been rebuilt since.
// Items cached without a cluster title fall back to the cluster's label.
func clusterRow(cluster TasteCluster, items []ScoredItem) ForYouRow {
	title := clusterTitle(cluster.Label)
	if len(items) > 0 {
		if _, ok := clusterTitleLabel(items[0].Reason); ok {
			title = items[0].Reason
		}
	}
	return ForYouRow{
		Type:         clusterRowType,
		Label:        title,
		ClusterIndex: cluster.ClusterIdx,
		Items:        items,
		Subject:      clusterSubject(title),
		personalKey:  RecTypeForYouClusterPrefix + itoa(cluster.ClusterIdx),
	}
}

// clusterSubject is the genre label a cluster row titled title is about: the
// title without its prefix. It is "" for a cluster whose label names no
// genre.
func clusterSubject(title string) string {
	label, _ := clusterTitleLabel(title)
	return label
}

// clusterRepeatJaccard is the overlap of two same-titled cluster rows' served
// items, as a Jaccard index, above which the lighter row is a repeat.
const clusterRepeatJaccard = 0.5

// distinguishClusterRows keeps two cluster rows from sharing a title. Rows
// are visited heaviest cluster first; a row titled like a heavier one is
// hidden when their first served items overlap by more than
// clusterRepeatJaccard, and otherwise renamed with the first of its
// cluster's dominant genres its title does not name yet. A row whose cluster
// has no such genre, or whose cached title its cluster no longer has (the
// clusters were rebuilt after the row was cached), keeps its title. Other
// rows, and the order of every row kept, are unchanged.
func distinguishClusterRows(rows []ForYouRow, clusters []TasteCluster, served int) []ForYouRow {
	byIndex := make(map[int]TasteCluster, len(clusters))
	for _, c := range clusters {
		byIndex[c.ClusterIdx] = c
	}
	var order []int
	for i, row := range rows {
		if strings.HasPrefix(row.personalKey, RecTypeForYouClusterPrefix) {
			order = append(order, i)
		}
	}
	if len(order) < 2 {
		return rows
	}
	slices.SortStableFunc(order, func(a, b int) int {
		return cmp.Compare(byIndex[rows[b].ClusterIndex].TotalWeight, byIndex[rows[a].ClusterIndex].TotalWeight)
	})

	hidden := make(map[int]bool)
	var kept []int
	for _, i := range order {
		same := slices.IndexFunc(kept, func(k int) bool { return rows[k].Label == rows[i].Label })
		if same >= 0 {
			if servedJaccard(rows[i].Items, rows[kept[same]].Items, served) > clusterRepeatJaccard {
				hidden[i] = true
				continue
			}
			renameClusterRow(&rows[i], byIndex[rows[i].ClusterIndex], rows, kept)
		}
		kept = append(kept, i)
	}
	if len(hidden) == 0 {
		return rows
	}
	out := make([]ForYouRow, 0, len(rows)-len(hidden))
	for i, row := range rows {
		if !hidden[i] {
			out = append(out, row)
		}
	}
	return out
}

// renameClusterRow adds to row's title the first of cluster's dominant
// genres the title does not name yet that leaves it unlike every title in
// kept, when there is one. The items' reasons take the new title too.
func renameClusterRow(row *ForYouRow, cluster TasteCluster, rows []ForYouRow, kept []int) {
	label, ok := clusterTitleLabel(row.Label)
	if !ok || label == "" || clusterTitle(cluster.Label) != row.Label {
		return
	}
	named := strings.Split(label, clusterLabelSeparator)
	for _, genre := range cluster.DominantGenres {
		if slices.Contains(named, genre) {
			continue
		}
		title := clusterTitle(label + clusterLabelSeparator + genre)
		if slices.ContainsFunc(kept, func(k int) bool { return rows[k].Label == title }) {
			continue
		}
		row.Label, row.Subject = title, clusterSubject(title)
		items := slices.Clone(row.Items)
		for i := range items {
			items[i].Reason = title
		}
		row.Items = items
		return
	}
}

// asServed gives a personal row read for its see-all page the title and
// order Discover and the For You rows give it: distinguishClusterRows may
// rename it by the profile's other cluster rows, and the day's rotation
// reorders its served window, so the cached row alone does not say. A row
// those reads do not show, such as one hidden as a repeat, keeps its cached
// title and order.
func (rr *rowRead) asServed(ctx context.Context, row *ForYouRow) error {
	// The page rows as Discover reads them, a row of ServedRowSize items;
	// the default rows it adds do not change a personal row.
	rows, err := rr.forYouPageRows(ctx, false, ServedRowSize)
	if err != nil {
		return err
	}
	for _, served := range rows {
		if served.personalKey == row.personalKey {
			row.Label, row.Subject, row.Items = served.Label, served.Subject, served.Items
			break
		}
	}
	return nil
}

// servedJaccard is the Jaccard index of the first served items of a and b.
func servedJaccard(a, b []ScoredItem, served int) float64 {
	a, b = a[:min(len(a), served)], b[:min(len(b), served)]
	in := make(map[string]struct{}, len(a))
	for _, item := range a {
		in[item.MediaItemID] = struct{}{}
	}
	shared := 0
	for _, item := range b {
		if _, ok := in[item.MediaItemID]; ok {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// filterRows drops from rows the items the profile cannot access, those not of
// recommendableMediaTypes, those in its recommendation exclusion set (watched
// and favorited titles), and those it rated 2 or lower, then drops the rows
// left empty. Rows cached before a title was watched or favorited, or before
// rows were kept to recommendableMediaTypes, are cleaned here.
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
	// excludedIDs is excluded as a list, built when a live row needs it.
	excludedIDs []string
}

func (r *Reader) newRowRead(userID int, profileID string, access catalog.AccessFilter) *rowRead {
	return &rowRead{reader: r, userID: userID, profileID: profileID, access: access}
}

// exclusionSet is the profile's recommendation exclusion set, loaded on
// first use.
func (rr *rowRead) exclusionSet(ctx context.Context) (map[string]struct{}, error) {
	if rr.excluded == nil {
		excluded, err := rr.reader.signalReader().RecommendationExclusionSet(ctx, rr.userID, rr.profileID)
		if err != nil {
			return nil, err
		}
		if excluded == nil {
			excluded = map[string]struct{}{}
		}
		rr.excluded = excluded
	}
	return rr.excluded, nil
}

// filter is filterRows for this read.
func (rr *rowRead) filter(ctx context.Context, rows []ForYouRow) ([]ForYouRow, error) {
	if len(rows) == 0 {
		return rows, nil
	}
	r, userID, profileID := rr.reader, rr.userID, rr.profileID

	excluded, err := rr.exclusionSet(ctx)
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
		accessible, err = r.repo.FilterRecommendableItemIDs(ctx, itemIDs, rr.access)
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
			if rating, rated := lowRatings[item.MediaItemID]; rated && rating <= DislikedRatingMax {
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
// It combines personalized for-you rows, the live default rows,
// similar-users, and daily genre rows. Every row but the default rows is read
// from cache. A row shows an item no earlier row shows.
func (r *Reader) GetDiscoverRows(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]ForYouRow, error) {
	limit = normalizeRecommendationLimit(limit)
	read := r.newRowRead(userID, profileID, filter)

	// 1. For-you rows (personalized + cold-start blended, already filtered).
	forYouRows, err := read.forYouPageRows(ctx, true, limit)
	if err != nil {
		return nil, err
	}

	// Track shown items for cross-row deduplication.
	seen := make(map[string]struct{})
	for i := range forYouRows {
		forYouRows[i].Items = deduplicateItems(forYouRows[i].Items, seen, limit)
	}
	// Drop rows emptied by dedup.
	forYouRows = dropEmptyRows(forYouRows)

	// 2. Similar users row.
	var extraRows []ForYouRow
	var similarItems []ScoredItem
	if !r.personalOff {
		if similarItems, err = r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeSimilarUsersLiked, ""); err != nil {
			return nil, err
		}
	}
	if len(similarItems) > 0 {
		extraRows = append(extraRows, ForYouRow{
			Type:  RecTypeSimilarUsersLiked,
			Label: similarUsersLabel,
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
				Type:  genreSamplerRowType,
				Label: genreRowLabel(genre),
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
		extraRows[i].Items = deduplicateItems(extraRows[i].Items, seen, limit)
	}
	extraRows = dropEmptyRows(extraRows)

	// Interleave: for-you rows first, then genre rows woven after every 2 for-you rows,
	// similar-users at the end.
	var result []ForYouRow
	var genreRows []ForYouRow
	var similarRow *ForYouRow
	for i := range extraRows {
		if extraRows[i].Type == RecTypeSimilarUsersLiked {
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
	read := r.newRowRead(userID, profileID, filter)

	var row *ForYouRow
	var err error
	switch kind {
	case SectionKindTopRated, SectionKindRecentlyAdded:
		// The live default rows, as Discover shows them.
		recType := RecTypeRecentlyAdded
		if kind == SectionKindTopRated {
			recType = RecTypeTopRated
		}
		var live ForYouRow
		live, err = read.defaultRow(ctx, recType, limit)
		row = &live
	case SectionKindCluster, SectionKindForYouMain:
		row, err = r.loadSectionRow(ctx, userID, profileID, kind, key)
		if err == nil && row != nil {
			err = read.asServed(ctx, row)
		}
	default:
		row, err = r.loadSectionRow(ctx, userID, profileID, kind, key)
	}
	if err != nil || row == nil {
		return nil, err
	}

	rows, err := read.filter(ctx, []ForYouRow{*row})
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
	if r.personalOff {
		switch kind {
		case SectionKindForYouMain, SectionKindCluster, SectionKindSimilarUsers:
			return nil, nil
		}
	}
	switch kind {
	case SectionKindForYouMain:
		items, err := r.repo.GetRecommendationCache(ctx, userID, profileID, RecTypeForYouMain, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: clusterRowType, Label: ForYouLabel, Items: items, personalKey: RecTypeForYouMain}, nil

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
			Type:  RecTypeSimilarUsersLiked,
			Label: similarUsersLabel,
			Items: items,
		}, nil

	case SectionKindPopular:
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypePopular, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: RecTypePopular, Label: popularLabel, Items: items}, nil

	case SectionKindGenre:
		if key == "" {
			return nil, fmt.Errorf("genre section requires a key")
		}
		items, err := r.repo.GetRecommendationCache(ctx, GlobalCacheUserID, GlobalCacheProfileID, RecTypeGenreSamplerPrefix+key, "")
		if err != nil || len(items) == 0 {
			return nil, err
		}
		return &ForYouRow{Type: genreSamplerRowType, Label: genreRowLabel(key), Items: items}, nil
	}

	return nil, nil
}

// Row types the API reports. clusterRowType is the type of the main row and
// of every taste-cluster row; genreSamplerRowType of a genre row.
const (
	clusterRowType      = "cluster"
	genreSamplerRowType = "genre_sampler"
)

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

// deduplicateItems keeps, in order, up to limit items whose MediaItemID is
// not in the seen set (every one when limit is not positive), and adds only
// the kept items to the set: items a row does not show stay free for later
// rows.
func deduplicateItems(items []ScoredItem, seen map[string]struct{}, limit int) []ScoredItem {
	capacity := len(items)
	if limit > 0 {
		capacity = min(capacity, limit)
	}
	result := make([]ScoredItem, 0, capacity)
	for _, item := range items {
		if limit > 0 && len(result) == limit {
			break
		}
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
