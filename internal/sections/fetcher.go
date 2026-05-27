package sections

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/overlays"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections/recipes"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// SectionWithItems is a resolved section populated with query results.
type SectionWithItems struct {
	ResolvedSection
	Items      []*models.MediaItem `json:"items"`
	TotalCount int                 `json:"total_count"`
	ItemMeta   map[string]SectionItemMeta
}

// SectionItemMeta carries optional per-item metadata for richer section UIs.
type SectionItemMeta struct {
	SeriesID          *string
	SeriesTitle       string
	SeasonNumber      *int
	EpisodeNumber     *int
	Badges            []string
	PositionSeconds   *float64
	DurationSeconds   *float64
	ProgressUpdatedAt *string
	ItemSource        string    // "in_progress" or "next_up"
	SortTimestamp     time.Time // when the preceding episode was completed (for ordering)
}

const recentSeasonPremiereBadgeWindowDays = 14
const editorialCandidateCacheTTL = 24 * time.Hour
const fetchAllMaxConcurrency = 4
const slowSectionFetchThreshold = 500 * time.Millisecond
const slowAggregateFetchThreshold = time.Second

type recommendationReader interface {
	GetForYouMain(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
	GetBecauseYouWatched(ctx context.Context, userID int, profileID, sourceItemID string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error)
	GetSimilarUsersLiked(ctx context.Context, userID int, profileID string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error)
	GetTasteMatchRow(ctx context.Context, userID int, profileID, genre string, limit int, filter catalog.AccessFilter) (*recommendations.ForYouRow, error)
}

// Fetcher runs section queries against the database.
type Fetcher struct {
	pool                 *pgxpool.Pool
	StoreProvider        userstore.UserStoreProvider
	CollectionRepo       *catalog.LibraryCollectionRepository
	RecommendationRepo   *recommendations.Repo // retained for non-reader call sites
	RecommendationReader recommendationReader
	NextUpRepo           *catalog.NextUpRepository
	candidateCacheMu     sync.Mutex
	candidateCache       *editorialCandidateCache
	candidateGroup       singleflight.Group

	// Clock returns the current time. Defaults to recipes.RealClock{}.
	// Tests inject recipes.FixedClock for deterministic seasonal/editorial behavior.
	Clock recipes.Clock
}

// NewFetcher creates a new section Fetcher.
func NewFetcher(pool *pgxpool.Pool) *Fetcher {
	return &Fetcher{pool: pool, Clock: recipes.RealClock{}}
}

type editorialCandidateLoader func(context.Context, string, *int, []int, catalog.AccessFilter) ([]string, error)

type editorialCandidateCache struct {
	mu      sync.RWMutex
	entries map[string]editorialCandidateCacheEntry
}

type editorialCandidateCacheEntry struct {
	candidates []string
	expiresAt  time.Time
}

func newEditorialCandidateCache() *editorialCandidateCache {
	return &editorialCandidateCache{entries: make(map[string]editorialCandidateCacheEntry)}
}

func (c *editorialCandidateCache) get(key string, now time.Time) ([]string, bool) {
	if c == nil {
		return nil, false
	}

	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok || !now.Before(entry.expiresAt) {
		return nil, false
	}
	return append([]string(nil), entry.candidates...), true
}

func (c *editorialCandidateCache) set(key string, candidates []string, expiresAt time.Time) {
	if c == nil {
		return
	}

	c.mu.Lock()
	c.entries[key] = editorialCandidateCacheEntry{
		candidates: append([]string(nil), candidates...),
		expiresAt:  expiresAt,
	}
	c.mu.Unlock()
}

func (f *Fetcher) ensureEditorialCandidateCache() *editorialCandidateCache {
	f.candidateCacheMu.Lock()
	defer f.candidateCacheMu.Unlock()
	if f.candidateCache == nil {
		f.candidateCache = newEditorialCandidateCache()
	}
	return f.candidateCache
}

func (f *Fetcher) now() time.Time {
	if f != nil && f.Clock != nil {
		return f.Clock.Now()
	}
	return time.Now()
}

func (f *Fetcher) cachedEditorialCandidates(ctx context.Context, subjectType string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter, ttl time.Duration, loader editorialCandidateLoader) ([]string, error) {
	if ttl <= 0 {
		return loader(ctx, subjectType, libraryID, libraryIDs, filter)
	}

	cache := f.ensureEditorialCandidateCache()
	key := editorialCandidateCacheKey(subjectType, libraryID, libraryIDs, filter)
	now := f.now()
	if candidates, ok := cache.get(key, now); ok {
		return candidates, nil
	}

	value, err, _ := f.candidateGroup.Do(key, func() (any, error) {
		now := f.now()
		if candidates, ok := cache.get(key, now); ok {
			return candidates, nil
		}
		candidates, err := loader(ctx, subjectType, libraryID, libraryIDs, filter)
		if err != nil {
			return nil, err
		}
		cache.set(key, candidates, now.Add(ttl))
		return append([]string(nil), candidates...), nil
	})
	if err != nil {
		return nil, err
	}
	candidates, _ := value.([]string)
	return append([]string(nil), candidates...), nil
}

func editorialCandidateCacheKey(subjectType string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) string {
	var b strings.Builder
	b.WriteString("subject=")
	b.WriteString(strings.ToLower(strings.TrimSpace(subjectType)))

	b.WriteString("|library=")
	if libraryID == nil {
		b.WriteString("all")
	} else {
		b.WriteString(strconv.Itoa(*libraryID))
	}

	b.WriteString("|libraries=")
	writeOptionalSortedInts(&b, libraryIDs)

	b.WriteString("|disabled=")
	writeSortedInts(&b, filter.DisabledLibraryIDs)

	b.WriteString("|rating=")
	b.WriteString(filter.MaxContentRating)
	return b.String()
}

func writeOptionalSortedInts(b *strings.Builder, values []int) {
	if values == nil {
		b.WriteString("<nil>")
		return
	}
	if len(values) == 0 {
		b.WriteString("<empty>")
		return
	}
	writeSortedInts(b, values)
}

func writeSortedInts(b *strings.Builder, values []int) {
	if len(values) == 0 {
		return
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	for i, value := range sorted {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(value))
	}
}

// FetchOne runs one section query and returns it with items.
func (f *Fetcher) FetchOne(ctx context.Context, resolved ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) (SectionWithItems, error) {
	start := time.Now()
	var result SectionWithItems
	var err error
	defer func() {
		f.logSlowSectionFetch(resolved, libraryID, libraryIDs, result, time.Since(start), err)
	}()

	if resolved.SectionType == SectionContinueWatching {
		result, err = f.fetchContinueWatchingSection(ctx, resolved, libraryID, libraryIDs, userID, profileID, filter)
		return result, err
	}
	if resolved.SectionType == SectionNextUp {
		result, err = f.fetchNextUpSection(ctx, resolved, libraryID, libraryIDs, userID, profileID, filter)
		return result, err
	}
	if resolved.SectionType == SectionEditorialSpotlight {
		var items []*models.MediaItem
		var total int
		var title string
		items, total, title, err = f.fetchEditorialSpotlightWithTitle(ctx, resolved, libraryID, libraryIDs, filter)
		if err != nil {
			return SectionWithItems{}, err
		}
		if title != "" {
			resolved.Title = title
		}
		result = SectionWithItems{
			ResolvedSection: resolved,
			Items:           items,
			TotalCount:      total,
		}
		return result, nil
	}

	var items []*models.MediaItem
	var total int
	items, total, err = f.fetchSection(ctx, resolved, libraryID, libraryIDs, userID, profileID, filter)
	if err != nil {
		return SectionWithItems{}, err
	}

	// Apply seasonal title override when the active theme has a custom name.
	// Done here (rather than inside fetchSeasonalThemed) so the section's
	// stored Title is preserved as the fallback used by callers that bypass
	// SectionWithItems construction.
	if resolved.SectionType == SectionSeasonalThemed && len(items) > 0 {
		if title := f.seasonalTitleOverride(resolved); title != "" {
			resolved.Title = title
		}
	}

	result = SectionWithItems{
		ResolvedSection: resolved,
		Items:           items,
		TotalCount:      total,
	}
	return result, nil
}

func (f *Fetcher) logSlowSectionFetch(resolved ResolvedSection, libraryID *int, libraryIDs []int, result SectionWithItems, duration time.Duration, err error) {
	if duration < slowSectionFetchThreshold {
		return
	}
	attrs := []any{
		"section_id", resolved.ID,
		"type", resolved.SectionType,
		"title", resolved.Title,
		"item_count", len(result.Items),
		"total_count", result.TotalCount,
		"duration_ms", duration.Milliseconds(),
	}
	if libraryID != nil {
		attrs = append(attrs, "library_id", *libraryID)
	}
	if libraryIDs != nil {
		attrs = append(attrs, "library_ids", append([]int(nil), libraryIDs...))
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	slog.Warn("slow section fetch", attrs...)
}

// seasonalTitleOverride returns the per-theme display name override for a
// seasonal section, or "" when no override applies.
func (f *Fetcher) seasonalTitleOverride(resolved ResolvedSection) string {
	if len(resolved.Config) == 0 {
		return ""
	}
	var p recipes.SeasonalThemedParams
	if err := json.Unmarshal(resolved.Config, &p); err != nil {
		return ""
	}
	if len(p.ThemeTitles) == 0 {
		return ""
	}
	now := time.Now()
	if f.Clock != nil {
		now = f.Clock.Now()
	}
	return recipes.SeasonalTitleOverride(p, now)
}

func (f *Fetcher) fetchContinueWatchingSection(ctx context.Context, resolved ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) (SectionWithItems, error) {
	if f.StoreProvider == nil || userID <= 0 || profileID == "" {
		return SectionWithItems{
			ResolvedSection: resolved,
			Items:           []*models.MediaItem{},
			TotalCount:      0,
			ItemMeta:        map[string]SectionItemMeta{},
		}, nil
	}

	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return SectionWithItems{}, fmt.Errorf("getting user store: %w", err)
	}

	limit := resolved.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	cfgFilters := ParseConfigFilters(resolved.Config)
	configLibraryIDs := cfgFilters.LibraryIDs()

	effectiveLibID := libraryID
	effectiveLibraryIDs := libraryIDs
	if effectiveLibID == nil && len(configLibraryIDs) > 0 {
		effectiveLibraryIDs = configLibraryIDs
	}

	progressEntries, err := store.ListProgress(ctx, profileID, "in_progress", limit, 0)
	if err != nil {
		return SectionWithItems{}, fmt.Errorf("listing progress: %w", err)
	}
	progressEntries = f.filterContinueWatchingDismissals(ctx, store, profileID, progressEntries)

	// Build in-progress items
	var orderedItems []*models.MediaItem
	itemMeta := make(map[string]SectionItemMeta)

	if len(progressEntries) > 0 {
		contentIDs := make([]string, 0, len(progressEntries))
		for _, entry := range progressEntries {
			contentIDs = append(contentIDs, entry.MediaItemID)
		}

		mediaItems, err := f.fetchItemsByContentIDs(ctx, contentIDs, effectiveLibID, effectiveLibraryIDs, filter)
		if err != nil {
			return SectionWithItems{}, err
		}

		episodeItems, episodeMeta, err := f.fetchEpisodeTargetsByContentIDs(ctx, contentIDs, effectiveLibID, effectiveLibraryIDs, filter)
		if err != nil {
			return SectionWithItems{}, err
		}

		itemByID := make(map[string]*models.MediaItem, len(mediaItems)+len(episodeItems))
		for _, item := range mediaItems {
			itemByID[item.ContentID] = item
		}
		for _, item := range episodeItems {
			itemByID[item.ContentID] = item
		}

		maps.Copy(itemMeta, episodeMeta)
		supersededEpisodeProgress, err := f.fetchSupersededEpisodeProgressIDs(ctx, store, profileID, progressEntries)
		if err != nil {
			return SectionWithItems{}, err
		}
		progressEntries = filterSupersededEpisodeProgressEntries(progressEntries, supersededEpisodeProgress)

		for _, entry := range progressEntries {
			meta := itemMeta[entry.MediaItemID]
			position := entry.PositionSeconds
			duration := entry.DurationSeconds
			meta.PositionSeconds = &position
			meta.DurationSeconds = &duration
			meta.ProgressUpdatedAt = &entry.UpdatedAt
			meta.ItemSource = "in_progress"
			if updatedAt, parseErr := time.Parse(time.RFC3339, entry.UpdatedAt); parseErr == nil {
				meta.SortTimestamp = updatedAt
			}
			itemMeta[entry.MediaItemID] = meta
		}

		orderedItems = make([]*models.MediaItem, 0, len(progressEntries))
		for _, entry := range progressEntries {
			item, ok := itemByID[entry.MediaItemID]
			if !ok {
				continue
			}
			orderedItems = append(orderedItems, item)
		}
	}

	// Fetch next-up items (combined mode by default)
	nextUpMode, _ := store.GetSetting(ctx, "next_up_mode")
	if nextUpMode == "" {
		nextUpMode = "combined"
	}

	if nextUpMode == "combined" {
		nextUpItems, nextUpMeta, nextUpErr := f.FetchNextUpItems(ctx, userID, profileID, effectiveLibID, effectiveLibraryIDs, filter, limit)
		if nextUpErr != nil {
			slog.Error("fetching next-up items", "error", nextUpErr)
		} else {
			orderedItems = append(orderedItems, nextUpItems...)
			maps.Copy(itemMeta, nextUpMeta)
		}
	}

	// Apply type filter after next-up items are appended
	if cfgFilters.FilterType != "" {
		filtered := make([]*models.MediaItem, 0, len(orderedItems))
		for _, item := range orderedItems {
			if (cfgFilters.FilterType == "movie" && item.Type == "movie") ||
				(cfgFilters.FilterType == "series" && item.Type == "episode") {
				filtered = append(filtered, item)
			}
		}
		orderedItems = filtered
	}

	if nextUpMode == "combined" && len(orderedItems) > 1 {
		orderedItems = collapseContinueWatchingSeriesCandidates(orderedItems, itemMeta)
	}

	if nextUpMode == "combined" && len(orderedItems) > 1 {
		sort.SliceStable(orderedItems, func(i, j int) bool {
			left := itemMeta[orderedItems[i].ContentID].SortTimestamp
			right := itemMeta[orderedItems[j].ContentID].SortTimestamp
			return left.After(right)
		})
	}

	if orderedItems == nil {
		orderedItems = []*models.MediaItem{}
	}

	return SectionWithItems{
		ResolvedSection: resolved,
		Items:           orderedItems,
		TotalCount:      len(orderedItems),
		ItemMeta:        itemMeta,
	}, nil
}

func (f *Fetcher) fetchNextUpSection(ctx context.Context, resolved ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) (SectionWithItems, error) {
	emptyResult := SectionWithItems{
		ResolvedSection: resolved,
		Items:           []*models.MediaItem{},
		TotalCount:      0,
		ItemMeta:        map[string]SectionItemMeta{},
	}

	if f.StoreProvider == nil || userID <= 0 || profileID == "" {
		return emptyResult, nil
	}

	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return SectionWithItems{}, fmt.Errorf("getting user store: %w", err)
	}

	// Only resolve if user preference is "separate"
	nextUpMode, _ := store.GetSetting(ctx, "next_up_mode")
	if nextUpMode != "separate" {
		return emptyResult, nil
	}

	limit := resolved.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	items, meta, err := f.FetchNextUpItems(ctx, userID, profileID, libraryID, libraryIDs, filter, limit)
	if err != nil {
		return SectionWithItems{}, err
	}
	if items == nil {
		items = []*models.MediaItem{}
	}
	if meta == nil {
		meta = map[string]SectionItemMeta{}
	}

	return SectionWithItems{
		ResolvedSection: resolved,
		Items:           items,
		TotalCount:      len(items),
		ItemMeta:        meta,
	}, nil
}

// FetchNextUpItems returns the next unwatched episode per series for the given user profile.
// It delegates to NextUpRepository for the optimized LATERAL JOIN query, then resolves
// the full MediaItem objects via fetchEpisodeTargetsByContentIDs.
func (f *Fetcher) FetchNextUpItems(ctx context.Context, userID int, profileID string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter, limit int) ([]*models.MediaItem, map[string]SectionItemMeta, error) {
	if f.NextUpRepo == nil {
		return nil, nil, nil
	}

	results, err := f.NextUpRepo.ListNextUp(ctx, catalog.NextUpQuery{
		UserID:     userID,
		ProfileID:  profileID,
		LibraryID:  libraryID,
		LibraryIDs: libraryIDs,
		Limit:      limit,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(results) == 0 {
		return nil, nil, nil
	}
	results = f.filterNextUpDismissals(ctx, userID, profileID, results)
	if len(results) == 0 {
		return nil, nil, nil
	}

	contentIDs := make([]string, len(results))
	resultByID := make(map[string]catalog.NextUpResult, len(results))
	for i, res := range results {
		contentIDs[i] = res.ContentID
		resultByID[res.ContentID] = res
	}

	episodeItems, episodeMeta, err := f.fetchEpisodeTargetsByContentIDs(ctx, contentIDs, libraryID, libraryIDs, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching episode targets for next-up: %w", err)
	}

	itemByID := make(map[string]*models.MediaItem, len(episodeItems))
	for _, item := range episodeItems {
		itemByID[item.ContentID] = item
	}

	meta := make(map[string]SectionItemMeta, len(results))
	orderedItems := make([]*models.MediaItem, 0, len(results))
	for _, res := range results {
		m, ok := episodeMeta[res.ContentID]
		if !ok {
			continue
		}
		m.ItemSource = "next_up"
		m.SortTimestamp = res.CompletedAt
		meta[res.ContentID] = m

		item, ok := itemByID[res.ContentID]
		if !ok {
			continue
		}
		orderedItems = append(orderedItems, item)
	}

	return orderedItems, meta, nil
}

type progressLister interface {
	ListProgress(ctx context.Context, profileID, status string, limit, offset int) ([]userstore.WatchProgress, error)
}

const supersededProgressPageSize = 500

type progressSnapshot struct {
	ContentID string
	UpdatedAt time.Time
}

func (f *Fetcher) fetchSupersededEpisodeProgressIDs(ctx context.Context, store progressLister, profileID string, entries []userstore.WatchProgress) (map[string]struct{}, error) {
	inProgress := progressSnapshots(entries)
	if len(inProgress) == 0 {
		return map[string]struct{}{}, nil
	}

	completed, err := completedProgressSnapshots(ctx, store, profileID)
	if err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return map[string]struct{}{}, nil
	}

	inProgressIDs, inProgressUpdatedAts := splitProgressSnapshots(inProgress)
	completedIDs, completedUpdatedAts := splitProgressSnapshots(completed)
	query := buildSupersededEpisodeProgressQuery()
	rows, err := f.pool.Query(ctx, query, inProgressIDs, inProgressUpdatedAts, completedIDs, completedUpdatedAts)
	if err != nil {
		return nil, fmt.Errorf("querying superseded episode progress: %w", err)
	}
	defer rows.Close()

	superseded := make(map[string]struct{})
	for rows.Next() {
		var mediaItemID string
		if err := rows.Scan(&mediaItemID); err != nil {
			return nil, fmt.Errorf("scanning superseded episode progress: %w", err)
		}
		superseded[mediaItemID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating superseded episode progress: %w", err)
	}
	return superseded, nil
}

func completedProgressSnapshots(ctx context.Context, store progressLister, profileID string) ([]progressSnapshot, error) {
	seen := make(map[string]struct{})
	snapshots := make([]progressSnapshot, 0)

	for offset := 0; ; offset += supersededProgressPageSize {
		entries, err := store.ListProgress(ctx, profileID, "completed", supersededProgressPageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("listing completed progress for superseded episodes: %w", err)
		}

		for _, snapshot := range progressSnapshots(entries) {
			contentID := snapshot.ContentID
			if _, ok := seen[contentID]; ok {
				continue
			}
			seen[contentID] = struct{}{}
			snapshots = append(snapshots, snapshot)
		}

		if len(entries) < supersededProgressPageSize {
			return snapshots, nil
		}
	}
}

func progressSnapshots(entries []userstore.WatchProgress) []progressSnapshot {
	snapshots := make([]progressSnapshot, 0, len(entries))
	for _, entry := range entries {
		contentID := strings.TrimSpace(entry.MediaItemID)
		if contentID == "" {
			continue
		}
		updatedAt, err := time.Parse(time.RFC3339, entry.UpdatedAt)
		if err != nil || updatedAt.IsZero() {
			continue
		}
		snapshots = append(snapshots, progressSnapshot{
			ContentID: contentID,
			UpdatedAt: updatedAt.UTC(),
		})
	}
	return snapshots
}

func splitProgressSnapshots(snapshots []progressSnapshot) ([]string, []time.Time) {
	contentIDs := make([]string, len(snapshots))
	updatedAts := make([]time.Time, len(snapshots))
	for i, snapshot := range snapshots {
		contentIDs[i] = snapshot.ContentID
		updatedAts[i] = snapshot.UpdatedAt
	}
	return contentIDs, updatedAts
}

func buildSupersededEpisodeProgressQuery() string {
	return `
		WITH in_progress(content_id, updated_at) AS (
			SELECT * FROM unnest($1::text[], $2::timestamptz[])
		),
		completed(content_id, updated_at) AS (
			SELECT * FROM unnest($3::text[], $4::timestamptz[])
		)
		SELECT DISTINCT ip.content_id
		FROM in_progress ip_progress
		JOIN episodes ip ON ip.content_id = ip_progress.content_id
		JOIN episodes done
		  ON done.series_id = ip.series_id
		 AND (done.season_number, done.episode_number) > (ip.season_number, ip.episode_number)
		JOIN completed done_progress
		  ON done_progress.content_id = done.content_id
		WHERE done_progress.updated_at > ip_progress.updated_at`
}

func filterSupersededEpisodeProgressEntries(entries []userstore.WatchProgress, superseded map[string]struct{}) []userstore.WatchProgress {
	if len(entries) == 0 || len(superseded) == 0 {
		return entries
	}

	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		if _, ok := superseded[entry.MediaItemID]; ok {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func collapseContinueWatchingSeriesCandidates(items []*models.MediaItem, meta map[string]SectionItemMeta) []*models.MediaItem {
	selectedBySeries := make(map[string]int)
	result := make([]*models.MediaItem, 0, len(items))

	for _, item := range items {
		if item == nil {
			continue
		}

		itemMeta := meta[item.ContentID]
		if itemMeta.SeriesID == nil || *itemMeta.SeriesID == "" {
			result = append(result, item)
			continue
		}

		seriesID := *itemMeta.SeriesID
		selectedIndex, ok := selectedBySeries[seriesID]
		if !ok {
			selectedBySeries[seriesID] = len(result)
			result = append(result, item)
			continue
		}

		current := result[selectedIndex]
		if continueWatchingCandidatePreferred(itemMeta, meta[current.ContentID]) {
			result[selectedIndex] = item
		}
	}

	return result
}

func continueWatchingCandidatePreferred(candidate, current SectionItemMeta) bool {
	candidateInProgress := candidate.ItemSource == "in_progress"
	currentInProgress := current.ItemSource == "in_progress"
	if candidateInProgress != currentInProgress {
		return candidateInProgress
	}

	if candidateInProgress {
		if !candidate.SortTimestamp.Equal(current.SortTimestamp) {
			return candidate.SortTimestamp.After(current.SortTimestamp)
		}
		return episodeOrdinal(candidate) > episodeOrdinal(current)
	}

	return candidate.SortTimestamp.After(current.SortTimestamp)
}

func episodeOrdinal(meta SectionItemMeta) int {
	season := 0
	if meta.SeasonNumber != nil {
		season = *meta.SeasonNumber
	}
	episode := 0
	if meta.EpisodeNumber != nil {
		episode = *meta.EpisodeNumber
	}
	return season*100000 + episode
}

func (f *Fetcher) filterContinueWatchingDismissals(ctx context.Context, store userstore.UserStore, profileID string, entries []userstore.WatchProgress) []userstore.WatchProgress {
	if len(entries) == 0 {
		return entries
	}

	dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceContinueWatching)
	if err != nil {
		slog.Error("listing continue watching dismissals", "profile_id", profileID, "error", err)
		return entries
	}
	if len(dismissals) == 0 {
		return entries
	}

	dismissalByItemID := make(map[string]userstore.HomeItemDismissal, len(dismissals))
	for _, dismissal := range dismissals {
		dismissalByItemID[dismissal.MediaItemID] = dismissal
	}

	filtered := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		dismissal, ok := dismissalByItemID[entry.MediaItemID]
		if !ok || dismissal.ProgressUpdatedAt == nil || *dismissal.ProgressUpdatedAt != entry.UpdatedAt {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func (f *Fetcher) filterNextUpDismissals(ctx context.Context, userID int, profileID string, results []catalog.NextUpResult) []catalog.NextUpResult {
	if len(results) == 0 || f.StoreProvider == nil || userID <= 0 || profileID == "" {
		return results
	}

	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		slog.Error("getting user store for next-up dismissals", "profile_id", profileID, "error", err)
		return results
	}

	dismissals, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceNextUp)
	if err != nil {
		slog.Error("listing next-up dismissals", "profile_id", profileID, "error", err)
		return results
	}
	if len(dismissals) == 0 {
		return results
	}

	dismissedIDs := make(map[string]struct{}, len(dismissals))
	for _, dismissal := range dismissals {
		dismissedIDs[dismissal.MediaItemID] = struct{}{}
	}

	filtered := make([]catalog.NextUpResult, 0, len(results))
	for _, result := range results {
		if _, dismissed := dismissedIDs[result.ContentID]; dismissed {
			continue
		}
		filtered = append(filtered, result)
	}
	return filtered
}

// FetchAll runs all section queries in parallel and returns sections with items.
func (f *Fetcher) FetchAll(ctx context.Context, resolved []ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) []SectionWithItems {
	start := time.Now()
	results := fetchAllWithRunner(ctx, resolved, fetchAllMaxConcurrency, func(ctx context.Context, sec ResolvedSection) (SectionWithItems, error) {
		return f.FetchOne(ctx, sec, libraryID, libraryIDs, userID, profileID, filter)
	})
	duration := time.Since(start)
	if duration >= slowAggregateFetchThreshold {
		attrs := []any{
			"section_count", len(resolved),
			"duration_ms", duration.Milliseconds(),
		}
		if libraryID != nil {
			attrs = append(attrs, "library_id", *libraryID)
		}
		if libraryIDs != nil {
			attrs = append(attrs, "library_ids", append([]int(nil), libraryIDs...))
		}
		slog.Warn("slow aggregate section fetch", attrs...)
	}
	return results
}

type sectionFetchRunner func(context.Context, ResolvedSection) (SectionWithItems, error)

func fetchAllWithRunner(ctx context.Context, resolved []ResolvedSection, maxConcurrency int, runner sectionFetchRunner) []SectionWithItems {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}

	results := make([]SectionWithItems, len(resolved))
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup

	for i, sec := range resolved {
		i, sec := i, sec
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := runner(ctx, sec)
			if err != nil {
				slog.Error("fetching section items", "section_id", sec.ID, "type", sec.SectionType, "error", err)
				result = SectionWithItems{
					ResolvedSection: sec,
					Items:           []*models.MediaItem{},
				}
			}
			results[i] = result
		}()
	}

	wg.Wait()
	return results
}

// FetchItemsByContentIDs resolves content IDs to full MediaItem objects,
// applying the provided access filter across libraries.
func (f *Fetcher) FetchItemsByContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	return f.fetchItemsByContentIDs(ctx, contentIDs, nil, nil, filter)
}

// FetchEpisodesByContentIDs resolves episode content IDs to MediaItem objects
// with their parent series metadata. Returns the items and per-episode metadata
// (series title, season/episode numbers). Non-episode content IDs are silently ignored.
func (f *Fetcher) FetchEpisodesByContentIDs(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) ([]*models.MediaItem, map[string]SectionItemMeta, error) {
	return f.fetchEpisodeTargetsByContentIDs(ctx, contentIDs, nil, nil, filter)
}

// ListOverlaySummaries batches file lookups for section cards and derives the
// compact overlay summary per content ID.
func (f *Fetcher) ListOverlaySummaries(ctx context.Context, contentIDs []string, filter catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	summaries := make(map[string]*models.OverlaySummary, len(contentIDs))
	if len(contentIDs) == 0 {
		return summaries, nil
	}

	rows, err := f.pool.Query(ctx, `
		SELECT content_id, episode_id, file_path, resolution, codec_audio, audio_tracks, hdr, video_tracks,
		       codec_video, audio_channels, container, subtitle_tracks, external_subtitles, edition_key
		FROM media_files
		WHERE (content_id = ANY($1) OR episode_id = ANY($1)) AND missing_since IS NULL
		ORDER BY content_id ASC, episode_id ASC, id ASC
	`, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("querying overlay summaries: %w", err)
	}
	defer rows.Close()

	requested := make(map[string]struct{}, len(contentIDs))
	for _, contentID := range contentIDs {
		requested[contentID] = struct{}{}
	}

	grouped := make(map[string][]*models.MediaFile, len(contentIDs))
	for rows.Next() {
		var contentID string
		var episodeID *string
		var filePath string
		var resolution *string
		var codecAudio *string
		var audioTracksJSON []byte
		var hdr bool
		var videoTracksJSON []byte
		var codecVideo *string
		var audioChannels *int
		var container *string
		var subtitleTracksJSON []byte
		var externalSubtitlesJSON []byte
		var editionKey *string

		if err := rows.Scan(
			&contentID, &episodeID, &filePath, &resolution, &codecAudio, &audioTracksJSON, &hdr, &videoTracksJSON,
			&codecVideo, &audioChannels, &container, &subtitleTracksJSON, &externalSubtitlesJSON, &editionKey,
		); err != nil {
			return nil, fmt.Errorf("scanning overlay summary row: %w", err)
		}

		file := &models.MediaFile{
			ContentID: contentID,
			FilePath:  filePath,
			HDR:       hdr,
		}
		if episodeID != nil {
			file.EpisodeID = *episodeID
		}
		if resolution != nil {
			file.Resolution = *resolution
		}
		if codecAudio != nil {
			file.CodecAudio = *codecAudio
		}
		if codecVideo != nil {
			file.CodecVideo = *codecVideo
		}
		if audioChannels != nil {
			file.AudioChannels = *audioChannels
		}
		if container != nil {
			file.Container = *container
		}
		if editionKey != nil {
			file.EditionKey = *editionKey
		}
		if len(audioTracksJSON) > 0 {
			if err := json.Unmarshal(audioTracksJSON, &file.AudioTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay audio tracks: %w", err)
			}
		}
		if len(videoTracksJSON) > 0 {
			if err := json.Unmarshal(videoTracksJSON, &file.VideoTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay video tracks: %w", err)
			}
		}
		if len(subtitleTracksJSON) > 0 {
			if err := json.Unmarshal(subtitleTracksJSON, &file.SubtitleTracks); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay subtitle tracks: %w", err)
			}
		}
		if len(externalSubtitlesJSON) > 0 {
			if err := json.Unmarshal(externalSubtitlesJSON, &file.ExternalSubtitles); err != nil {
				return nil, fmt.Errorf("unmarshaling overlay external subtitles: %w", err)
			}
		}

		groupKey := contentID
		if episodeID != nil {
			if _, ok := requested[*episodeID]; ok {
				groupKey = *episodeID
			}
		}
		grouped[groupKey] = append(grouped[groupKey], file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating overlay summary rows: %w", err)
	}

	for contentID, files := range grouped {
		files = catalog.FilterMediaFilesByAccess(files, filter)
		if summary := overlays.BuildSummary(files); summary != nil {
			summaries[contentID] = summary
		}
	}

	return summaries, nil
}

func (f *Fetcher) fetchSection(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	switch s.SectionType {
	case SectionRecentlyAdded:
		return f.fetchRecentlyAdded(ctx, s, libraryID, libraryIDs, filter)
	case SectionRecentlyReleased:
		return f.fetchRecentlyReleased(ctx, s, libraryID, libraryIDs, filter)
	case SectionGenre, SectionCustomFilter:
		return f.fetchFiltered(ctx, s, libraryID, libraryIDs, filter)
	case SectionRandom:
		return f.fetchRandom(ctx, s, libraryID, libraryIDs, filter)
	case SectionCollection:
		return f.fetchCollection(ctx, s, libraryID, libraryIDs, userID, profileID, filter)
	case SectionRecommendedForYou, SectionBecauseYouWatched, SectionSimilarUsersLiked, SectionTasteMatch:
		return f.fetchRecommendationSection(ctx, s, libraryID, libraryIDs, userID, profileID, filter)
	case SectionHiddenGems:
		return f.fetchHiddenGems(ctx, s, libraryID, libraryIDs, userID, profileID, filter)
	case SectionCriticallyAcclaimed:
		return f.fetchCriticallyAcclaimed(ctx, s, libraryID, libraryIDs, filter)
	case SectionAwardWinners:
		return f.fetchAwardWinners(ctx, s)
	case SectionForgottenFavorites:
		return f.fetchForgottenFavorites(ctx, s, libraryID, libraryIDs, userID, profileID, filter)
	case SectionFormatShowcase:
		return f.fetchFormatShowcase(ctx, s, libraryID, libraryIDs, filter)
	case SectionEditorialSpotlight:
		return f.fetchEditorialSpotlight(ctx, s, libraryID, libraryIDs, filter)
	case SectionSeasonalThemed:
		return f.fetchSeasonalThemed(ctx, s, libraryID, libraryIDs, filter)
	case SectionMoodCollection:
		return f.fetchMoodCollection(ctx, s, libraryID, libraryIDs, filter)
	case SectionTrendingOnServer:
		return f.fetchTrending(ctx, s, libraryID, libraryIDs, filter)
	case SectionProfileActivityFeed:
		return f.fetchProfileActivityFeed(ctx, s, libraryID, libraryIDs, profileID, filter)
	case SectionNewToLibrary:
		return f.fetchNewToLibrary(ctx, s, libraryID, libraryIDs, filter)
	case SectionMostWatched:
		return f.fetchMostWatched(ctx, s, libraryID, libraryIDs, filter)
	case SectionAdminCuratedList:
		return f.fetchAdminCuratedList(ctx, s, libraryID, libraryIDs, filter)
	default:
		// Profile-scoped types (continue_watching, watchlist, favorites)
		// will be wired later when user store integration is added.
		return nil, 0, fmt.Errorf("unsupported section type: %s", s.SectionType)
	}
}

func (f *Fetcher) fetchCollection(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	cfg := ParseCollectionConfig(s.Config)

	// User collection path: resolve from the user's personal store.
	if userCollID := strings.TrimSpace(cfg.UserCollectionID); userCollID != "" {
		return f.fetchUserCollection(ctx, s, libraryID, libraryIDs, userID, profileID, filter, userCollID)
	}

	// Library collection path (existing behaviour).
	if f.CollectionRepo == nil {
		return nil, 0, fmt.Errorf("collection sections require a collection repository")
	}

	if strings.TrimSpace(cfg.LibraryCollectionID) == "" {
		return []*models.MediaItem{}, 0, nil
	}

	if _, err := f.CollectionRepo.GetByID(ctx, cfg.LibraryCollectionID); err != nil {
		return nil, 0, fmt.Errorf("loading library collection: %w", err)
	}

	collectionItems, err := f.CollectionRepo.ListItems(ctx, cfg.LibraryCollectionID)
	if err != nil {
		return nil, 0, fmt.Errorf("listing collection items: %w", err)
	}
	if len(collectionItems) == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	limit := s.ItemLimit
	if limit <= 0 || limit > len(collectionItems) {
		limit = len(collectionItems)
	}

	contentIDs := make([]string, 0, limit)
	for _, item := range collectionItems[:limit] {
		contentIDs = append(contentIDs, item.MediaItemID)
	}

	items, err := f.fetchItemsByContentIDs(ctx, contentIDs, libraryID, libraryIDs, filter)
	if err != nil {
		return nil, 0, err
	}

	itemByID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		itemByID[item.ContentID] = item
	}

	orderedItems := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		item, ok := itemByID[contentID]
		if !ok {
			continue
		}
		orderedItems = append(orderedItems, item)
	}

	return orderedItems, len(orderedItems), nil
}

// fetchUserCollection resolves a personal (profile-scoped) user collection.
// For smart collections it delegates to fetchFiltered; for manual ones it
// fetches the stored item list and looks up the media items by content ID.
func (f *Fetcher) fetchUserCollection(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter, collectionID string) ([]*models.MediaItem, int, error) {
	if f.StoreProvider == nil || userID <= 0 || profileID == "" {
		return []*models.MediaItem{}, 0, nil
	}

	store, err := f.StoreProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, 0, fmt.Errorf("getting user store for collection: %w", err)
	}

	collection, err := store.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, 0, fmt.Errorf("loading user collection: %w", err)
	}

	// Verify the requesting profile can access this collection.
	if collection.ProfileID != profileID && collection.CreatorProfileID != profileID {
		canAccess := false
		for _, allowed := range collection.AllowedProfileIDs {
			if allowed == profileID {
				canAccess = true
				break
			}
		}
		if !canAccess {
			return []*models.MediaItem{}, 0, nil
		}
	}

	// Smart / live-query collection: parse the query definition and use the
	// filtered fetch path, mirroring resolveUserCollectionSource in catalog_resolver.
	isSmartType := strings.EqualFold(strings.TrimSpace(collection.CollectionType), "smart")
	hasQueryDef := len(strings.TrimSpace(collection.QueryDefinition)) > 0 &&
		strings.TrimSpace(collection.QueryDefinition) != "{}" &&
		strings.TrimSpace(collection.QueryDefinition) != "null"
	if isSmartType || hasQueryDef {
		var qd catalog.QueryDefinition
		if err := json.Unmarshal([]byte(collection.QueryDefinition), &qd); err != nil {
			return nil, 0, fmt.Errorf("parsing user collection query_definition: %w", err)
		}
		qd = qd.Normalize()
		// Build a synthetic resolved section with the query definition as config.
		cfgBytes, _ := json.Marshal(qd)
		synth := ResolvedSection{
			ID:          s.ID,
			SectionType: SectionCustomFilter,
			Title:       s.Title,
			Featured:    s.Featured,
			ItemLimit:   s.ItemLimit,
			Config:      cfgBytes,
			Position:    s.Position,
		}
		return f.fetchFiltered(ctx, synth, libraryID, libraryIDs, filter)
	}

	// Manual collection: fetch stored items.
	collectionItems, err := store.ListCollectionItems(ctx, collectionID)
	if err != nil {
		return nil, 0, fmt.Errorf("listing user collection items: %w", err)
	}
	if len(collectionItems) == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	limit := s.ItemLimit
	if limit <= 0 || limit > len(collectionItems) {
		limit = len(collectionItems)
	}

	contentIDs := make([]string, 0, limit)
	for _, item := range collectionItems[:limit] {
		contentIDs = append(contentIDs, item.MediaItemID)
	}

	items, err := f.fetchItemsByContentIDs(ctx, contentIDs, libraryID, libraryIDs, filter)
	if err != nil {
		return nil, 0, err
	}

	itemByID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		itemByID[item.ContentID] = item
	}

	orderedItems := make([]*models.MediaItem, 0, len(contentIDs))
	for _, contentID := range contentIDs {
		item, ok := itemByID[contentID]
		if !ok {
			continue
		}
		orderedItems = append(orderedItems, item)
	}

	return orderedItems, len(orderedItems), nil
}

func (f *Fetcher) fetchRecommendationSection(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	if f.RecommendationReader == nil {
		return []*models.MediaItem{}, 0, nil // graceful degradation
	}

	cfg := parseRecommendationSectionConfig(s.Config)
	var scoredItems []recommendations.ScoredItem
	switch s.SectionType {
	case SectionRecommendedForYou:
		row, err := f.RecommendationReader.GetForYouMain(ctx, userID, profileID, s.ItemLimit, filter)
		if err != nil || row == nil {
			return []*models.MediaItem{}, 0, err
		}
		scoredItems = row.Items
	case SectionBecauseYouWatched:
		items, err := f.RecommendationReader.GetBecauseYouWatched(ctx, userID, profileID, cfg.SourceItemID, s.ItemLimit, filter)
		if err != nil {
			return []*models.MediaItem{}, 0, err
		}
		scoredItems = items
	case SectionSimilarUsersLiked:
		items, err := f.RecommendationReader.GetSimilarUsersLiked(ctx, userID, profileID, s.ItemLimit, filter)
		if err != nil {
			return []*models.MediaItem{}, 0, err
		}
		scoredItems = items
	case SectionTasteMatch:
		if strings.TrimSpace(cfg.Genre) == "" {
			return []*models.MediaItem{}, 0, nil
		}
		row, err := f.RecommendationReader.GetTasteMatchRow(ctx, userID, profileID, cfg.Genre, s.ItemLimit, filter)
		if err != nil || row == nil {
			return []*models.MediaItem{}, 0, err
		}
		scoredItems = row.Items
	default:
		return []*models.MediaItem{}, 0, nil
	}

	if len(scoredItems) == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	// Resolve item IDs to full MediaItem objects
	itemIDs := make([]string, len(scoredItems))
	for i, item := range scoredItems {
		itemIDs[i] = item.MediaItemID
	}

	mediaItems, err := f.fetchItemsByContentIDs(ctx, itemIDs, libraryID, libraryIDs, filter)
	if err != nil {
		return nil, 0, err
	}
	orderedItems := orderMediaItems(mediaItems, itemIDs)
	return orderedItems, len(orderedItems), nil
}

func (f *Fetcher) fetchHiddenGems(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.HiddenGemsParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	if p.MinRating == 0 {
		p.MinRating = 7.5
	}

	// Require a valid user/profile to exclude watched items.
	if userID <= 0 || profileID == "" {
		return []*models.MediaItem{}, 0, nil
	}

	repo := catalog.NewDiscoveryRepository(f.pool)
	items, err := repo.ListUnplayedHighRated(ctx, catalog.UnplayedFilter{
		MinRating: p.MinRating,
		Limit:     s.ItemLimit,
		UserID:    userID,
		ProfileID: profileID,
		Filter:    filter,
	})
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchCriticallyAcclaimed(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.CriticallyAcclaimedParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	if p.MinScore == 0 {
		p.MinScore = 8.0
	}

	repo := catalog.NewDiscoveryRepository(f.pool)
	items, err := repo.ListByRatingThreshold(ctx, catalog.RatingFilter{
		Min:       p.MinScore,
		Limit:     s.ItemLimit,
		LibraryID: libraryID,
		Filter:    filter,
	})
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchAwardWinners(_ context.Context, s ResolvedSection) ([]*models.MediaItem, int, error) {
	// TODO(awards-data): wire up once award metadata exists
	return []*models.MediaItem{}, 0, nil
}

func (f *Fetcher) fetchForgottenFavorites(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, userID int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.ForgottenFavoritesParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	if p.LookbackDays <= 0 {
		p.LookbackDays = 365
	}

	// Require a valid user/profile to check watch history.
	if userID <= 0 || profileID == "" {
		return []*models.MediaItem{}, 0, nil
	}

	repo := catalog.NewDiscoveryRepository(f.pool)
	items, err := repo.ListForgottenFavorites(ctx, catalog.ForgottenFavoritesFilter{
		LookbackDays: p.LookbackDays,
		Limit:        s.ItemLimit,
		UserID:       userID,
		ProfileID:    profileID,
		Filter:       filter,
	})
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

// fetchFormatShowcase returns items that have at least one media file matching the
// requested stream format.  Filtering is done against media_files columns:
//   - 4k:           resolution IN ('4k','uhd','2160p')
//   - dolby_vision:  EXISTS a video_tracks element with a non-empty dolby_vision field
//   - hdr:           hdr = true
//
// When no format is supplied all items with any of the above formats are returned.
func (f *Fetcher) fetchFormatShowcase(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.FormatShowcaseParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}

	// Build the per-format EXISTS subquery condition against media_files.
	var formatCond string
	switch p.Format {
	case "4k":
		formatCond = `EXISTS (
			SELECT 1 FROM media_files mf
			WHERE (mf.content_id = mi.content_id OR mf.episode_id = mi.content_id)
			  AND mf.missing_since IS NULL
			  AND LOWER(mf.resolution) IN ('4k','uhd','2160p')
		)`
	case "dolby_vision":
		formatCond = `EXISTS (
			SELECT 1 FROM media_files mf
			WHERE (mf.content_id = mi.content_id OR mf.episode_id = mi.content_id)
			  AND mf.missing_since IS NULL
			  AND EXISTS (
				SELECT 1 FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
				WHERE vt->>'dolby_vision' IS NOT NULL AND vt->>'dolby_vision' != ''
			  )
		)`
	case "hdr":
		formatCond = `EXISTS (
			SELECT 1 FROM media_files mf
			WHERE (mf.content_id = mi.content_id OR mf.episode_id = mi.content_id)
			  AND mf.missing_since IS NULL
			  AND mf.hdr = true
		)`
	default:
		// No format specified: return items matching any of the above formats.
		formatCond = `EXISTS (
			SELECT 1 FROM media_files mf
			WHERE (mf.content_id = mi.content_id OR mf.episode_id = mi.content_id)
			  AND mf.missing_since IS NULL
			  AND (
				LOWER(mf.resolution) IN ('4k','uhd','2160p')
				OR mf.hdr = true
				OR EXISTS (
					SELECT 1 FROM jsonb_array_elements(COALESCE(mf.video_tracks, '[]'::jsonb)) AS vt
					WHERE vt->>'dolby_vision' IS NOT NULL AND vt->>'dolby_vision' != ''
				)
			  )
		)`
	}

	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, formatCond)

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	query := fmt.Sprintf(
		`SELECT %s FROM %s %s ORDER BY mi.rating_imdb DESC NULLS LAST, mi.content_id ASC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, limit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching format showcase: %w", err)
	}
	defer rows.Close()

	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchAdminCuratedList(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.AdminCuratedListParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	if len(p.ItemIDs) == 0 {
		return []*models.MediaItem{}, 0, nil
	}

	// Build conditions: content_id IN curated set + library scope + access filter.
	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("mi.content_id = ANY($%d)", argIdx))
	args = append(args, p.ItemIDs)
	argIdx++

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	// Order by position in the curated array. Reuse the item-IDs parameter
	// index (1) for array_position; do NOT pass the slice twice.
	query := fmt.Sprintf(
		`SELECT %s FROM %s %s ORDER BY array_position($1::text[], mi.content_id)`,
		itemColumns("mi"), fromClause, whereClause,
	)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching admin curated list: %w", err)
	}
	defer rows.Close()

	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

// fetchEditorialSpotlight returns items for an editorial_spotlight section.
// When auto_rotate is true it selects a subject from a candidate list using a
// deterministic ISO-week hash; otherwise it uses the configured subject directly.
func (f *Fetcher) fetchEditorialSpotlight(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	items, total, _, err := f.fetchEditorialSpotlightWithTitle(ctx, s, libraryID, libraryIDs, filter)
	return items, total, err
}

func (f *Fetcher) fetchEditorialSpotlightWithTitle(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, string, error) {
	var p recipes.EditorialSpotlightParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	if p.SubjectType == "" {
		return []*models.MediaItem{}, 0, "", nil
	}

	subject := p.Subject
	if p.AutoRotate {
		cands, err := f.cachedEditorialCandidates(ctx, p.SubjectType, libraryID, libraryIDs, filter, editorialCandidateCacheTTL, f.editorialCandidates)
		if err != nil {
			return nil, 0, "", fmt.Errorf("editorial_spotlight candidates: %w", err)
		}
		if len(cands) == 0 {
			return []*models.MediaItem{}, 0, "", nil
		}
		days := editorialCadenceDays(p.RotationCadence)
		// Build a value-based rotation key. Using `%v` on the *int would print
		// the pointer address, which changes on every process restart and
		// would break the deterministic-rotation contract.
		libKey := "all"
		if libraryID != nil {
			libKey = strconv.Itoa(*libraryID)
		}
		key := fmt.Sprintf("%s|%s", p.SubjectType, libKey)
		idx := recipes.RotationIndex(f.now(), key, len(cands), days)
		subject = cands[idx]
	}

	if subject == "" {
		return []*models.MediaItem{}, 0, "", nil
	}

	// Build a QueryDefinition with the subject filter and run it via the executor.
	def, err := editorialBuildQueryDef(p.SubjectType, subject)
	if err != nil {
		return nil, 0, "", fmt.Errorf("editorial_spotlight build query: %w", err)
	}

	switch {
	case libraryID != nil:
		def.LibraryIDs = []int{*libraryID}
	case libraryIDs != nil:
		if len(def.LibraryIDs) == 0 {
			def.LibraryIDs = append([]int(nil), libraryIDs...)
		} else {
			def.LibraryIDs = intersectLibraryIDs(def.LibraryIDs, libraryIDs)
		}
	}

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}
	def.Limit = &limit

	executor := &catalog.QueryExecutor{Pool: f.pool}
	items, _, _, err := executor.PreviewPage(ctx, def, filter, limit, 0, false)
	if err != nil {
		return nil, 0, "", fmt.Errorf("editorial_spotlight query: %w", err)
	}
	return items, len(items), editorialSpotlightDisplayTitle(s.Title, subject), nil
}

func editorialSpotlightDisplayTitle(baseTitle, subject string) string {
	baseTitle = strings.TrimSpace(baseTitle)
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return baseTitle
	}
	if baseTitle == "" || strings.EqualFold(baseTitle, subject) {
		return subject
	}
	suffix := " - " + subject
	if strings.HasSuffix(baseTitle, suffix) {
		return baseTitle
	}
	return baseTitle + suffix
}

// editorialCadenceDays converts a RotationCadence to a number of days for RotationIndex.
func editorialCadenceDays(c recipes.RotationCadence) int {
	switch c {
	case recipes.CadenceDaily:
		return 1
	case recipes.CadenceMonthly:
		return 30
	default: // weekly or empty
		return 7
	}
}

// editorialBuildQueryDef builds a QueryDefinition for the given subject type and subject value.
func editorialBuildQueryDef(subjectType, subject string) (catalog.QueryDefinition, error) {
	var rule catalog.QueryRule
	switch subjectType {
	case "director":
		rule = catalog.QueryRule{Field: "director", Op: "is", Value: subject}
	case "actor":
		rule = catalog.QueryRule{Field: "actor", Op: "is", Value: subject}
	case "studio":
		rule = catalog.QueryRule{Field: "studio", Op: "is", Value: subject}
	case "era":
		startYear, endYear, err := eraToYearRange(subject)
		if err != nil {
			return catalog.QueryDefinition{}, err
		}
		// release_date between uses string dates ("YYYY-01-01", "YYYY-12-31").
		startISO := fmt.Sprintf("%d-01-01", startYear)
		endISO := fmt.Sprintf("%d-12-31", endYear)
		rule = catalog.QueryRule{Field: "release_date", Op: "between", Value: []any{startISO, endISO}}
	default:
		return catalog.QueryDefinition{}, fmt.Errorf("editorial_spotlight: unsupported subject_type %q", subjectType)
	}

	def := catalog.QueryDefinition{
		Match: "all",
		Groups: []catalog.QueryGroup{
			{Match: "all", Rules: []catalog.QueryRule{rule}},
		},
	}
	return def.Normalize(), nil
}

// eraToYearRange converts an era label like "1980s" to its start and end years.
func eraToYearRange(era string) (int, int, error) {
	switch era {
	case "1970s":
		return 1970, 1979, nil
	case "1980s":
		return 1980, 1989, nil
	case "1990s":
		return 1990, 1999, nil
	case "2000s":
		return 2000, 2009, nil
	case "2010s":
		return 2010, 2019, nil
	case "2020s":
		return 2020, 2029, nil
	default:
		return 0, 0, fmt.Errorf("editorial_spotlight: unknown era %q", era)
	}
}

// editorialCandidates returns the top subject names for auto-rotation, applying
// library scope and access filter so results are scoped to the user's libraries.
func (f *Fetcher) editorialCandidates(ctx context.Context, subjectType string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]string, error) {
	switch subjectType {
	case "director":
		return f.topPersonCandidates(ctx, 2 /* PersonKindDirector */, libraryID, libraryIDs, filter)
	case "actor":
		return f.topPersonCandidates(ctx, 1 /* PersonKindActor */, libraryID, libraryIDs, filter)
	case "studio":
		return f.topStudioCandidates(ctx, libraryID, libraryIDs, filter)
	case "era":
		// Auto-rotate excludes the current decade — incomplete data + stylistic skew.
		// "2020s" is still accepted as a manually-pinned subject via eraToYearRange.
		return []string{"1970s", "1980s", "1990s", "2000s", "2010s"}, nil
	case "franchise":
		// TODO: franchise data not yet available; auto-rotate returns empty.
		return nil, nil
	default:
		return nil, fmt.Errorf("editorial_spotlight: unsupported subject_type %q", subjectType)
	}
}

const editorialCandidateLimit = 50

// topPersonCandidates returns the top N person names by item count for the given kind.
func (f *Fetcher) topPersonCandidates(ctx context.Context, kind int, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]string, error) {
	var conditions []string
	var args []any
	argIdx := 1

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := "WHERE 1=1"
	if len(conditions) > 0 {
		whereClause += " AND " + strings.Join(conditions, " AND ")
	}

	args = append(args, kind, editorialCandidateLimit)
	// Weighted billing: SUM(1/(sort_order+1)) favors top-billed credits
	// (TMDB convention: 0 = lead). HAVING MIN(sort_order) <= 5 excludes
	// people who only ever appear in deep supporting roles.
	query := fmt.Sprintf(`
		SELECT p.name
		FROM people p
		JOIN item_people ip ON ip.person_id = p.id AND ip.kind = $%d
		JOIN %s ON mi.content_id = ip.content_id
		%s
		GROUP BY p.name
		HAVING MIN(ip.sort_order) <= 5
		ORDER BY SUM(1.0 / (ip.sort_order + 1)) DESC, COUNT(*) DESC
		LIMIT $%d
	`, argIdx, fromClause, whereClause, argIdx+1)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying top persons (kind=%d): %w", kind, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning person name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// topStudioCandidates returns the top N studio names by item count.
func (f *Fetcher) topStudioCandidates(ctx context.Context, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]string, error) {
	var conditions []string
	var args []any
	argIdx := 1

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "AND " + strings.Join(conditions, " AND ")
	}

	args = append(args, editorialCandidateLimit)
	query := fmt.Sprintf(`
		SELECT studio
		FROM (
			SELECT unnest(mi.studios) AS studio
			FROM %s
			WHERE mi.studios IS NOT NULL
			%s
		) sub
		GROUP BY studio
		ORDER BY COUNT(*) DESC
		LIMIT $%d
	`, fromClause, whereClause, argIdx)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying top studios: %w", err)
	}
	defer rows.Close()

	var studios []string
	for rows.Next() {
		var studio string
		if err := rows.Scan(&studio); err != nil {
			return nil, fmt.Errorf("scanning studio name: %w", err)
		}
		studios = append(studios, studio)
	}
	return studios, rows.Err()
}

type recommendationSectionConfig struct {
	SourceItemID string `json:"source_item_id"`
	Genre        string `json:"genre"`
}

func parseRecommendationSectionConfig(config json.RawMessage) recommendationSectionConfig {
	var cfg recommendationSectionConfig
	if len(config) == 0 {
		return cfg
	}
	_ = json.Unmarshal(config, &cfg)
	return cfg
}

func orderMediaItems(items []*models.MediaItem, orderedIDs []string) []*models.MediaItem {
	if len(items) <= 1 {
		return items
	}
	byID := make(map[string]*models.MediaItem, len(items))
	for _, item := range items {
		byID[item.ContentID] = item
	}
	ordered := make([]*models.MediaItem, 0, len(items))
	for _, id := range orderedIDs {
		item, ok := byID[id]
		if !ok {
			continue
		}
		ordered = append(ordered, item)
	}
	return ordered
}

func (f *Fetcher) fetchRecentlyAdded(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	query, args := buildRecentlyAddedQuery(s, libraryID, libraryIDs, filter)
	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanMediaItems(rows)
	return items, len(items), err
}

type sectionQuery struct {
	sql  string
	args []any
}

func buildRecentlyAddedQuery(s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) (string, []any) {
	cfgFilters := ParseConfigFilters(s.Config)

	// Backwards compat: support legacy "types" config field
	var legacyCfg struct {
		Types []string `json:"types"`
	}
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &legacyCfg)
	}
	if cfgFilters.FilterType == "" && len(legacyCfg.Types) > 0 {
		cfgFilters.FilterType = legacyCfg.Types[0]
	}

	if query, ok := buildRecentlyAddedSingleLibraryQuery(s, cfgFilters, libraryID, libraryIDs, filter); ok {
		return query.sql, query.args
	}

	var conditions []string
	var args []any
	argIdx := 1

	applyConfigTypeFilter("mi", cfgFilters.FilterType, &conditions, &args, &argIdx)

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, cfgFilters.LibraryIDs(), filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(
		`SELECT %s FROM %s %s ORDER BY mi.created_at DESC, mi.content_id ASC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, s.ItemLimit)
	return query, args
}

func buildRecentlyAddedSingleLibraryQuery(s ResolvedSection, cfgFilters SectionConfigFilters, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) (sectionQuery, bool) {
	configLibraryIDs := cfgFilters.LibraryIDs()
	var singleLibraryID int
	switch {
	case libraryID != nil:
		singleLibraryID = *libraryID
	case len(configLibraryIDs) == 1:
		singleLibraryID = configLibraryIDs[0]
	default:
		return sectionQuery{}, false
	}
	if singleLibraryID <= 0 {
		return sectionQuery{}, false
	}

	var conditions []string
	var args []any
	argIdx := 1

	conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
	args = append(args, singleLibraryID)
	argIdx++

	if libraryIDs != nil {
		if len(libraryIDs) == 0 {
			conditions = append(conditions, "1 = 0")
		} else {
			placeholders := make([]string, len(libraryIDs))
			for i, id := range libraryIDs {
				placeholders[i] = fmt.Sprintf("$%d", argIdx)
				args = append(args, id)
				argIdx++
			}
			conditions = append(conditions, fmt.Sprintf("mil.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
		}
	}

	if len(filter.DisabledLibraryIDs) > 0 {
		placeholders := make([]string, len(filter.DisabledLibraryIDs))
		for i, id := range filter.DisabledLibraryIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id NOT IN (%s)", strings.Join(placeholders, ", ")))
	}

	applyConfigTypeFilter("mi", cfgFilters.FilterType, &conditions, &args, &argIdx)
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := "WHERE " + strings.Join(conditions, " AND ")
	query := fmt.Sprintf(
		`SELECT %s FROM media_item_libraries mil JOIN media_items mi ON mi.content_id = mil.content_id %s ORDER BY mil.first_seen_at DESC, mil.content_id ASC LIMIT $%d`,
		itemColumns("mi"), whereClause, argIdx,
	)
	args = append(args, s.ItemLimit)
	return sectionQuery{sql: query, args: args}, true
}

func (f *Fetcher) fetchRecentlyReleased(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	cfgFilters := ParseConfigFilters(s.Config)

	var conditions []string
	var args []any
	argIdx := 1

	applyConfigTypeFilter("mi", cfgFilters.FilterType, &conditions, &args, &argIdx)

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, cfgFilters.LibraryIDs(), filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(
		`SELECT %s FROM %s %s ORDER BY mi.year DESC, mi.created_at DESC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, s.ItemLimit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	items, err := scanMediaItems(rows)
	return items, len(items), err
}

func (f *Fetcher) fetchFiltered(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	def, err := ParseQueryDefinition(s.Config)
	if err != nil {
		return nil, 0, fmt.Errorf("parsing query definition: %w", err)
	}

	switch {
	case libraryID != nil:
		def.LibraryIDs = []int{*libraryID}
	case libraryIDs != nil:
		if len(def.LibraryIDs) == 0 {
			def.LibraryIDs = append([]int(nil), libraryIDs...)
		} else {
			def.LibraryIDs = intersectLibraryIDs(def.LibraryIDs, libraryIDs)
		}
	}

	if s.ItemLimit > 0 {
		limit := s.ItemLimit
		def.Limit = &limit
	}

	executor := &catalog.QueryExecutor{Pool: f.pool}
	items, _, hasMore, err := executor.PreviewPage(ctx, def, filter, s.ItemLimit, 0, false)
	if err != nil {
		return nil, 0, err
	}

	total := len(items)
	if hasMore && s.ItemLimit > 0 && total <= s.ItemLimit {
		total = s.ItemLimit + 1
	}

	return items, total, nil
}

func (f *Fetcher) fetchRandom(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	cfgFilters := ParseConfigFilters(s.Config)

	var conditions []string
	var args []any
	argIdx := 1

	applyConfigTypeFilter("mi", cfgFilters.FilterType, &conditions, &args, &argIdx)

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, cfgFilters.LibraryIDs(), filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}
	queryLimit := limit + 1

	query := fmt.Sprintf(
		`SELECT candidate.content_id
		FROM (
			SELECT DISTINCT mi.content_id
			FROM %s
			%s
		) candidate
		ORDER BY RANDOM()
		LIMIT $%d`,
		fromClause, whereClause, argIdx,
	)
	args = append(args, queryLimit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var contentIDs []string
	for rows.Next() {
		var contentID string
		if err := rows.Scan(&contentID); err != nil {
			return nil, 0, fmt.Errorf("scanning random section content id: %w", err)
		}
		contentIDs = append(contentIDs, contentID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	hasMore := len(contentIDs) > limit
	if hasMore {
		contentIDs = contentIDs[:limit]
	}

	items, err := f.fetchItemsByContentIDs(ctx, contentIDs, libraryID, libraryIDs, filter)
	if err != nil {
		return nil, 0, err
	}

	ordered := orderMediaItems(items, contentIDs)
	total := len(ordered)
	if hasMore && total <= limit {
		total = limit + 1
	}

	return ordered, total, nil
}

func (f *Fetcher) fetchItemsByContentIDs(ctx context.Context, contentIDs []string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, nil
	}

	var conditions []string
	var args []any
	argIdx := 1

	placeholders := make([]string, len(contentIDs))
	for i, contentID := range contentIDs {
		placeholders[i] = fmt.Sprintf("$%d", argIdx)
		args = append(args, contentID)
		argIdx++
	}
	conditions = append(conditions, fmt.Sprintf("mi.content_id IN (%s)", strings.Join(placeholders, ", ")))

	effectiveLibraryIDs := effectiveFetchLibraryIDs(libraryIDs, filter)
	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, effectiveLibraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	query := fmt.Sprintf(
		`SELECT %s FROM %s WHERE %s`,
		itemColumns("mi"), fromClause, strings.Join(conditions, " AND "),
	)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanMediaItems(rows)
}

func (f *Fetcher) fetchEpisodeTargetsByContentIDs(ctx context.Context, contentIDs []string, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, map[string]SectionItemMeta, error) {
	if len(contentIDs) == 0 {
		return []*models.MediaItem{}, map[string]SectionItemMeta{}, nil
	}

	var conditions []string
	var args []any
	argIdx := 1

	placeholders := make([]string, len(contentIDs))
	for i, contentID := range contentIDs {
		placeholders[i] = fmt.Sprintf("$%d", argIdx)
		args = append(args, contentID)
		argIdx++
	}
	conditions = append(conditions, fmt.Sprintf("e.content_id IN (%s)", strings.Join(placeholders, ", ")))

	effectiveLibraryIDs := effectiveFetchLibraryIDs(libraryIDs, filter)

	fromClause := "episodes e JOIN media_items si ON e.series_id = si.content_id LEFT JOIN seasons s ON s.content_id = e.season_id"
	if libraryID != nil || effectiveLibraryIDs != nil {
		fromClause += " JOIN media_item_libraries mil ON si.content_id = mil.content_id"
	}

	if libraryID != nil {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, *libraryID)
		argIdx++
	}

	if effectiveLibraryIDs != nil {
		if len(effectiveLibraryIDs) == 0 {
			return []*models.MediaItem{}, map[string]SectionItemMeta{}, nil
		}
		placeholders = make([]string, len(effectiveLibraryIDs))
		for i, id := range effectiveLibraryIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
	}

	catalog.ApplySectionAccessFilter("si", filter, &conditions, &args, &argIdx)

	query := fmt.Sprintf(`
		SELECT
			e.content_id,
			e.series_id,
			e.title,
			e.overview,
			e.runtime,
			e.rating_imdb,
			COALESCE(NULLIF(s.poster_path, ''), NULLIF(si.poster_path, ''), NULLIF(e.still_path, ''), '') AS poster_path,
			COALESCE(NULLIF(s.poster_thumbhash, ''), NULLIF(si.poster_thumbhash, ''), NULLIF(e.still_thumbhash, ''), '') AS poster_thumbhash,
			e.season_number,
			e.episode_number,
			e.air_date,
			si.title,
			si.genres,
			si.content_rating,
			COALESCE(NULLIF(e.still_path, ''), NULLIF(si.backdrop_path, ''), '') AS backdrop_path,
			COALESCE(NULLIF(e.still_thumbhash, ''), NULLIF(si.backdrop_thumbhash, ''), '') AS backdrop_thumbhash,
			si.logo_path,
			si.status
		FROM %s
		WHERE %s
	`, fromClause, strings.Join(conditions, " AND "))

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	items := []*models.MediaItem{}
	itemMeta := map[string]SectionItemMeta{}
	for rows.Next() {
		var (
			item          models.MediaItem
			seriesID      string
			seasonNumber  int
			episodeNumber int
			seriesTitle   string
			airDate       *time.Time
		)
		item.Type = "episode"
		err := rows.Scan(
			&item.ContentID,
			&seriesID,
			&item.Title,
			&item.Overview,
			&item.Runtime,
			&item.RatingIMDB,
			&item.PosterPath,
			&item.PosterThumbhash,
			&seasonNumber,
			&episodeNumber,
			&airDate,
			&seriesTitle,
			&item.Genres,
			&item.ContentRating,
			&item.BackdropPath,
			&item.BackdropThumbhash,
			&item.LogoPath,
			&item.Status,
		)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning episode section item: %w", err)
		}
		items = append(items, &item)
		itemMeta[item.ContentID] = SectionItemMeta{
			SeriesID:      &seriesID,
			SeriesTitle:   seriesTitle,
			SeasonNumber:  &seasonNumber,
			EpisodeNumber: &episodeNumber,
			Badges:        recentSeasonPremiereBadges(seasonNumber, episodeNumber, airDate),
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return items, itemMeta, nil
}

func effectiveFetchLibraryIDs(libraryIDs []int, filter catalog.AccessFilter) []int {
	if libraryIDs != nil {
		return libraryIDs
	}
	if filter.AllowedLibraryIDs != nil {
		return filter.AllowedLibraryIDs
	}
	return nil
}

func recentSeasonPremiereBadges(seasonNumber, episodeNumber int, airDate *time.Time) []string {
	if seasonNumber <= 1 || episodeNumber != 1 || airDate == nil {
		return nil
	}

	nowDate := utcDay(time.Now().UTC())
	premiereDate := utcDay(airDate.UTC())
	windowStart := nowDate.AddDate(0, 0, -(recentSeasonPremiereBadgeWindowDays - 1))
	if premiereDate.Before(windowStart) || premiereDate.After(nowDate) {
		return nil
	}

	return []string{"season_premiere"}
}

func utcDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// itemColumns returns the SELECT column list matching scanMediaItems.
// Mirrors catalog.browseItemColumns.
func itemColumns(alias string) string {
	cols := []string{
		"content_id", "type", "title", "sort_title", "original_title", "year", "genres",
		"content_rating", "runtime", "overview", "tagline",
		"rating_imdb", "rating_tmdb", "rating_rt_critic", "rating_rt_audience",
		"imdb_id", "tmdb_id", "tvdb_id",
		"poster_path", "poster_thumbhash", "backdrop_path", "backdrop_thumbhash", "logo_path",
		"metadata_s3_path", "metadata_etag", "season_count",
		"studios", "networks", "countries", "first_air_date", "last_air_date",
		"show_status",
		"matched_at", "status", "created_at", "updated_at",
	}
	prefixed := make([]string, len(cols))
	for i, c := range cols {
		prefixed[i] = alias + "." + c
	}
	return strings.Join(prefixed, ", ")
}

// scanMediaItems scans rows into MediaItem slices. Must match itemColumns order.
func scanMediaItems(rows pgx.Rows) ([]*models.MediaItem, error) {
	var items []*models.MediaItem
	for rows.Next() {
		var item models.MediaItem
		err := rows.Scan(
			&item.ContentID, &item.Type, &item.Title, &item.SortTitle, &item.OriginalTitle,
			&item.Year, &item.Genres, &item.ContentRating, &item.Runtime, &item.Overview, &item.Tagline,
			&item.RatingIMDB, &item.RatingTMDB, &item.RatingRTCritic, &item.RatingRTAudience,
			&item.ImdbID, &item.TmdbID, &item.TvdbID,
			&item.PosterPath, &item.PosterThumbhash, &item.BackdropPath, &item.BackdropThumbhash, &item.LogoPath,
			&item.MetadataS3Path, &item.MetadataEtag, &item.SeasonCount,
			&item.Studios, &item.Networks, &item.Countries, &item.FirstAirDate, &item.LastAirDate,
			&item.ShowStatus,
			&item.MatchedAt, &item.Status, &item.CreatedAt, &item.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning item: %w", err)
		}
		items = append(items, &item)
	}
	return items, rows.Err()
}

func (f *Fetcher) fetchTrending(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.TrendingParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	interval := "7 days"
	switch p.Window {
	case "24h":
		interval = "24 hours"
	case "7d", "":
		interval = "7 days"
	case "30d":
		interval = "30 days"
	}

	var conditions []string
	var args []any
	argIdx := 1

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	conditions = append(conditions, fmt.Sprintf("uwh.watched_at > NOW() - $%d::interval", argIdx))
	args = append(args, interval)
	argIdx++

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	query := fmt.Sprintf(
		`SELECT %s FROM %s JOIN user_watch_history uwh ON uwh.media_item_id = mi.content_id %s GROUP BY mi.content_id ORDER BY COUNT(DISTINCT uwh.profile_id) DESC, COUNT(*) DESC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, limit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching trending: %w", err)
	}
	defer rows.Close()
	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchProfileActivityFeed(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, profileID string, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.ProfileActivityFeedParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	target := p.ProfileID

	// Household mode (target == "") leaks all history when caller is unauthenticated.
	if target == "" && profileID == "" {
		return []*models.MediaItem{}, 0, nil
	}

	var args []any
	argIdx := 1

	// CTE deduplicates per media_item_id and keeps the most-recent watched_at,
	// so each item appears once and is ordered by latest-watch DESC.
	var cteCond string
	var cteWindow string
	if target == "" {
		cteCond = fmt.Sprintf("profile_id <> $%d", argIdx)
		args = append(args, profileID)
		argIdx++
		cteWindow = "INTERVAL '7 days'"
	} else {
		cteCond = fmt.Sprintf("profile_id = $%d", argIdx)
		args = append(args, target)
		argIdx++
		cteWindow = "INTERVAL '30 days'"
	}

	var conditions []string
	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(
		`WITH most_recent AS (
			SELECT media_item_id, MAX(watched_at) AS latest
			FROM user_watch_history
			WHERE %s AND watched_at > NOW() - %s
			GROUP BY media_item_id
		)
		SELECT %s
		FROM %s
		JOIN most_recent mr ON mr.media_item_id = mi.content_id
		%s
		ORDER BY mr.latest DESC
		LIMIT $%d`,
		cteCond, cteWindow,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, limit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching profile activity feed: %w", err)
	}
	defer rows.Close()
	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchNewToLibrary(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.NewToLibraryParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	days := p.LookbackDays
	if days <= 0 {
		days = 30
	}

	var conditions []string
	var args []any
	argIdx := 1

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	conditions = append(conditions, fmt.Sprintf("mi.created_at > NOW() - ($%d || ' days')::interval", argIdx))
	args = append(args, days)
	argIdx++

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	query := fmt.Sprintf(
		`SELECT %s FROM %s %s ORDER BY mi.created_at DESC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, limit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching new to library: %w", err)
	}
	defer rows.Close()
	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func (f *Fetcher) fetchMostWatched(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.MostWatchedParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}
	interval := "7 days"
	if p.Window == "month" {
		interval = "30 days"
	}

	var conditions []string
	var args []any
	argIdx := 1

	fromClause, libConditions, libArgs, newArgIdx := buildLibraryScope(libraryID, libraryIDs, nil, filter.DisabledLibraryIDs, argIdx)
	conditions = append(conditions, libConditions...)
	args = append(args, libArgs...)
	argIdx = newArgIdx
	catalog.ApplySectionAccessFilter("mi", filter, &conditions, &args, &argIdx)

	conditions = append(conditions, fmt.Sprintf("uwh.watched_at > NOW() - $%d::interval", argIdx))
	args = append(args, interval)
	argIdx++

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}

	whereClause := "WHERE " + strings.Join(conditions, " AND ")

	query := fmt.Sprintf(
		`SELECT %s FROM %s JOIN user_watch_history uwh ON uwh.media_item_id = mi.content_id %s GROUP BY mi.content_id ORDER BY COUNT(*) DESC LIMIT $%d`,
		itemColumns("mi"), fromClause, whereClause, argIdx,
	)
	args = append(args, limit)

	rows, err := f.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching most watched: %w", err)
	}
	defer rows.Close()
	items, err := scanMediaItems(rows)
	if err != nil {
		return nil, 0, err
	}
	return items, len(items), nil
}

func buildLibraryScope(libraryID *int, libraryIDs []int, configLibraryIDs []int, disabledLibraryIDs []int, argIdx int) (string, []string, []any, int) {
	fromClause := "media_items mi"
	var conditions []string
	var args []any

	needsJoin := libraryID != nil || libraryIDs != nil || len(configLibraryIDs) > 0 || len(disabledLibraryIDs) > 0

	if needsJoin {
		fromClause = "media_items mi JOIN media_item_libraries mil ON mi.content_id = mil.content_id"
	}

	if libraryID != nil {
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id = $%d", argIdx))
		args = append(args, *libraryID)
		argIdx++
	}

	if len(configLibraryIDs) > 0 && libraryID == nil {
		placeholders := make([]string, len(configLibraryIDs))
		for i, id := range configLibraryIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
	}

	if libraryIDs != nil {
		if len(libraryIDs) == 0 {
			conditions = append(conditions, "1 = 0")
			return fromClause, conditions, args, argIdx
		}
		placeholders := make([]string, len(libraryIDs))
		for i, id := range libraryIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id IN (%s)", strings.Join(placeholders, ", ")))
	}

	if len(disabledLibraryIDs) > 0 {
		placeholders := make([]string, len(disabledLibraryIDs))
		for i, id := range disabledLibraryIDs {
			placeholders[i] = fmt.Sprintf("$%d", argIdx)
			args = append(args, id)
			argIdx++
		}
		conditions = append(conditions, fmt.Sprintf("mil.media_folder_id NOT IN (%s)", strings.Join(placeholders, ", ")))
	}

	return fromClause, conditions, args, argIdx
}

func fetchSortClause(sort, order string) string {
	dir := "DESC"
	if order == "asc" {
		dir = "ASC"
	}
	switch sort {
	case "rating":
		return fmt.Sprintf("ORDER BY mi.rating_imdb %s NULLS LAST", dir)
	case "year":
		return fmt.Sprintf("ORDER BY mi.year %s", dir)
	case "title":
		return fmt.Sprintf("ORDER BY mi.sort_title %s", dir)
	default:
		return fmt.Sprintf("ORDER BY mi.created_at %s", dir)
	}
}

func intersectLibraryIDs(a, b []int) []int {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	allowed := make(map[int]struct{}, len(b))
	for _, value := range b {
		allowed[value] = struct{}{}
	}
	var result []int
	seen := make(map[int]struct{})
	for _, value := range a {
		if _, ok := allowed[value]; !ok {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// applyConfigTypeFilter adds a WHERE condition for the config's filter_type.
func applyConfigTypeFilter(alias string, filterType string, conditions *[]string, args *[]any, argIdx *int) {
	if filterType == "" {
		return
	}
	*conditions = append(*conditions, fmt.Sprintf("%s.type = $%d", alias, *argIdx))
	*args = append(*args, filterType)
	*argIdx++
}

// fetchSeasonalThemed returns items for a seasonal_themed section.
//
// Multi-theme mode (EnabledThemes set): picks the highest-priority enabled
// theme whose predicate matches now and queries items for that theme. Off-
// season (no enabled theme matches) the section is empty and gets suppressed
// by the API dispatcher.
//
// Legacy single-theme mode (Theme set, EnabledThemes empty): mode controls
// suppression — "auto" hides off-season, "pinned" always renders.
func (f *Fetcher) fetchSeasonalThemed(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.SeasonalThemedParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}

	now := time.Now()
	if f.Clock != nil {
		now = f.Clock.Now()
	}

	// Resolve which theme to use. Multi-theme mode wins when populated.
	var theme string
	switch {
	case len(p.EnabledThemes) > 0:
		theme = recipes.ActiveSeasonalTheme(p.EnabledThemes, now)
		if theme == "" {
			// No enabled theme is currently in season — hide the section.
			return []*models.MediaItem{}, 0, nil
		}
	case p.Theme != "":
		// Legacy single-theme path with mode-based suppression.
		pred, ok := recipes.SeasonalPredicates[p.Theme]
		if !ok {
			return []*models.MediaItem{}, 0, nil
		}
		mode := p.Mode
		if mode == "" {
			mode = "auto"
		}
		if mode == "auto" && !pred(now) {
			return []*models.MediaItem{}, 0, nil
		}
		theme = p.Theme
	default:
		return []*models.MediaItem{}, 0, nil
	}

	def, hasQuery := seasonalQueryDef(theme)
	if !hasQuery {
		// Theme exists but has no executable genre query yet (christmas,
		// st_patricks, thanksgiving need keyword/tag column support).
		// Return empty until that data lands.
		return []*models.MediaItem{}, 0, nil
	}

	// Apply library scope the same way fetchFiltered / fetchEditorialSpotlight do.
	switch {
	case libraryID != nil:
		def.LibraryIDs = []int{*libraryID}
	case libraryIDs != nil:
		if len(def.LibraryIDs) == 0 {
			def.LibraryIDs = append([]int(nil), libraryIDs...)
		} else {
			def.LibraryIDs = intersectLibraryIDs(def.LibraryIDs, libraryIDs)
		}
	}

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}
	def.Limit = &limit

	executor := &catalog.QueryExecutor{Pool: f.pool}
	items, _, _, err := executor.PreviewPage(ctx, def, filter, limit, 0, false)
	if err != nil {
		return nil, 0, fmt.Errorf("seasonal_themed query: %w", err)
	}
	return items, len(items), nil
}

// seasonalQueryDef returns a catalog.QueryDefinition for the given theme and
// whether an executable query exists. Themes that require a keyword/tag column
// (christmas, st_patricks, thanksgiving) return false until that data lands.
func seasonalQueryDef(theme string) (catalog.QueryDefinition, bool) {
	newDef := func(rules ...catalog.QueryRule) catalog.QueryDefinition {
		return catalog.QueryDefinition{
			Match: "all",
			Groups: []catalog.QueryGroup{
				{Match: "any", Rules: rules},
			},
		}
	}
	rule := func(field, op, value string) catalog.QueryRule {
		return catalog.QueryRule{Field: field, Op: op, Value: value}
	}

	switch theme {
	case "halloween":
		return newDef(
			rule("genre", "contains", "Horror"),
			rule("genre", "contains", "Thriller"),
		), true
	case "valentines":
		return newDef(
			rule("genre", "contains", "Romance"),
		), true
	case "summer", "summer_blockbuster":
		return newDef(
			rule("genre", "contains", "Action"),
			rule("genre", "contains", "Adventure"),
		), true
	case "saturday_morning":
		return newDef(
			rule("genre", "contains", "Animation"),
			rule("genre", "contains", "Family"),
		), true
	case "christmas", "st_patricks", "thanksgiving":
		// TODO: needs keyword/tag column to filter by theme-specific keywords.
		return catalog.QueryDefinition{}, false
	default:
		return catalog.QueryDefinition{}, false
	}
}

// fetchMoodCollection returns items for a mood_collection section.
func (f *Fetcher) fetchMoodCollection(ctx context.Context, s ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error) {
	var p recipes.MoodCollectionParams
	if len(s.Config) > 0 {
		_ = json.Unmarshal(s.Config, &p)
	}

	info, ok := recipes.MoodByKey(p.Mood)
	if !ok {
		return []*models.MediaItem{}, 0, nil
	}

	// Build genre rules (OR'd together).
	genreRules := make([]catalog.QueryRule, 0, len(info.GenresAny))
	for _, genre := range info.GenresAny {
		genreRules = append(genreRules, catalog.QueryRule{Field: "genre", Op: "contains", Value: genre})
	}

	def := catalog.QueryDefinition{
		Match: "all",
		Groups: []catalog.QueryGroup{
			{Match: "any", Rules: genreRules},
			{Match: "all", Rules: []catalog.QueryRule{
				{Field: "rating_imdb", Op: "gte", Value: info.MinRating},
			}},
		},
	}

	switch {
	case libraryID != nil:
		def.LibraryIDs = []int{*libraryID}
	case libraryIDs != nil:
		if len(def.LibraryIDs) == 0 {
			def.LibraryIDs = append([]int(nil), libraryIDs...)
		} else {
			def.LibraryIDs = intersectLibraryIDs(def.LibraryIDs, libraryIDs)
		}
	}

	limit := s.ItemLimit
	if limit <= 0 {
		limit = 20
	}
	def.Limit = &limit

	executor := &catalog.QueryExecutor{Pool: f.pool}
	items, _, _, err := executor.PreviewPage(ctx, def, filter, limit, 0, false)
	if err != nil {
		return nil, 0, fmt.Errorf("mood_collection query: %w", err)
	}
	return items, len(items), nil
}
