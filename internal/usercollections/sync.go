package usercollections

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Service performs sync runs for user-owned imported collections. The result
// of each sync is written into user_personal_collection_items via the
// per-user store; downstream catalog reads enforce profile-level access
// filtering, so this service resolves against the entire catalog regardless
// of who owns the collection.
type Service struct {
	storeProvider userstore.UserStoreProvider
	items         *catalog.ItemRepository
	libraryItems  *catalog.LibraryItemRepository
	httpClient    *http.Client
	logger        *slog.Logger

	TMDBCollections    catalog.TMDBCollectionFetcher
	TraktCollections   catalog.TraktCollectionFetcher
	TraktTokenResolver catalog.TraktAccessTokenResolver
}

func NewService(
	storeProvider userstore.UserStoreProvider,
	items *catalog.ItemRepository,
	libraryItems *catalog.LibraryItemRepository,
	httpClient *http.Client,
	logger *slog.Logger,
) *Service {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		storeProvider: storeProvider,
		items:         items,
		libraryItems:  libraryItems,
		httpClient:    httpClient,
		logger:        logger,
	}
}

// SyncResult summarizes the outcome of one sync run.
type SyncResult struct {
	Status         string    `json:"status"`
	Message        string    `json:"message"`
	ItemsMatched   int       `json:"items_matched"`
	ItemsUnmatched int       `json:"items_unmatched"`
	StartedAt      time.Time `json:"started_at"`
	CompletedAt    time.Time `json:"completed_at"`
}

// SyncCollection loads the collection by id and dispatches to the right
// per-source sync implementation.
func (s *Service) SyncCollection(ctx context.Context, userID int, collectionID string) (*SyncResult, error) {
	store, err := s.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("opening user store: %w", err)
	}
	collection, err := store.GetCollection(ctx, collectionID)
	if err != nil {
		return nil, err
	}
	result, _, err := s.RunSync(ctx, store, collection)
	return result, err
}

// RunSync syncs an already-loaded collection. Handlers that have validated
// ownership pass the collection in to avoid a second GetCollection round
// trip. Returns both the sync result and the post-sync collection state so
// callers can render the updated row without an extra read.
func (s *Service) RunSync(ctx context.Context, store userstore.UserStore, collection *userstore.Collection) (*SyncResult, *userstore.Collection, error) {
	cfg, err := ParseSourceConfig(collection.SourceConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing source_config: %w", err)
	}
	startedAt := time.Now().UTC()
	switch cfg.Mode {
	case SourceModeMDBList:
		return s.syncMDBList(ctx, store, collection, cfg, startedAt)
	case SourceModeTMDBPreset:
		return s.syncTMDB(ctx, store, collection, cfg, startedAt)
	case SourceModeTraktPreset:
		return s.syncTrakt(ctx, store, collection, cfg, startedAt)
	default:
		return nil, nil, ErrSyncUnsupported
	}
}

// ── MDBList ──────────────────────────────────────────────────────────────────

type mdblistEntry struct {
	ID          int    `json:"id"`
	Rank        int    `json:"rank"`
	TVDBID      *int   `json:"tvdbid"`
	IMDbID      string `json:"imdb_id"`
	MediaType   string `json:"mediatype"`
	Title       string `json:"title"`
	ReleaseYear int    `json:"release_year"`
}

func (s *Service) syncMDBList(ctx context.Context, store userstore.UserStore, collection *userstore.Collection, cfg SourceConfig, startedAt time.Time) (*SyncResult, *userstore.Collection, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, nil, fmt.Errorf("mdblist sync: url is required")
	}

	entries, err := s.fetchMDBListEntries(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	if fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit); fetchLimit > 0 && len(entries) > fetchLimit {
		entries = entries[:fetchLimit]
	}

	var movieBatch, seriesBatch catalog.ExternalIDBatch
	for _, entry := range entries {
		batch := &movieBatch
		if mdbListItemType(entry) == "series" {
			batch = &seriesBatch
		}
		if entry.ID > 0 {
			batch.TMDBIDs = append(batch.TMDBIDs, fmt.Sprintf("%d", entry.ID))
		}
		if entry.IMDbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, entry.IMDbID)
		}
		if entry.TVDBID != nil && *entry.TVDBID > 0 && batch == &seriesBatch {
			batch.TVDBIDs = append(batch.TVDBIDs, fmt.Sprintf("%d", *entry.TVDBID))
		}
	}

	movieLookup, err := s.items.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, nil, err
	}

	resolveLimit := collectionResolveLimit(cfg)
	matched, unmatched, scanned := resolveMatchedWithLimit(len(entries), resolveLimit, func(i int) string {
		entry := entries[i]
		itemType := mdbListItemType(entry)
		lookup := movieLookup
		if itemType == "series" {
			lookup = seriesLookup
		}
		var tvdb string
		if entry.TVDBID != nil && *entry.TVDBID > 0 {
			tvdb = fmt.Sprintf("%d", *entry.TVDBID)
		}
		var tmdb string
		if entry.ID > 0 {
			tmdb = fmt.Sprintf("%d", entry.ID)
		}
		return resolveCandidate(lookup, itemType, tvdb, tmdb, entry.IMDbID)
	})
	matched, droppedByLib, err := s.filterByLibraries(ctx, matched, cfg.LibraryIDs)
	if err != nil {
		return nil, nil, err
	}
	matched = limitCollectionItems(matched, cfg.Limit)
	return s.applyResult(ctx, store, collection, startedAt, matched, len(entries), scanned, unmatched+droppedByLib)
}

func mdbListItemType(entry mdblistEntry) string {
	switch strings.ToLower(entry.MediaType) {
	case "show", "tv", "series":
		return "series"
	default:
		return "movie"
	}
}

// resolveCandidate picks a content_id from a batch lookup using the standard
// TVDB → TMDB → IMDb priority shared with admin sync.
func resolveCandidate(lookup *catalog.ExternalIDLookup, itemType, tvdbID, tmdbID, imdbID string) string {
	if lookup == nil {
		return ""
	}
	if itemType == "series" && tvdbID != "" {
		if id := lookup.ByTVDB[tvdbID]; id != "" {
			return id
		}
	}
	if tmdbID != "" {
		if id := lookup.ByTMDB[tmdbID]; id != "" {
			return id
		}
	}
	if imdbID != "" {
		if id := lookup.ByIMDb[imdbID]; id != "" {
			return id
		}
	}
	return ""
}

// filterByLibraries drops matched items that are not present in any of the
// supplied libraries. Returns the surviving items (with positions
// recompacted) plus a count of items removed by the filter, which the caller
// rolls into the unmatched count so the user sees an honest match summary.
func (s *Service) filterByLibraries(ctx context.Context, matched []userstore.CollectionItemReplacement, libraryIDs []int) ([]userstore.CollectionItemReplacement, int, error) {
	if len(libraryIDs) == 0 || len(matched) == 0 || s.libraryItems == nil {
		return matched, 0, nil
	}
	ids := make([]string, len(matched))
	for i, m := range matched {
		ids[i] = m.MediaItemID
	}
	membership, err := s.libraryItems.GetItemsInFolders(ctx, ids, libraryIDs)
	if err != nil {
		return nil, 0, err
	}
	kept := make([]userstore.CollectionItemReplacement, 0, len(matched))
	for _, m := range matched {
		if !membership[m.MediaItemID] {
			continue
		}
		m.Position = len(kept)
		kept = append(kept, m)
	}
	return kept, len(matched) - len(kept), nil
}

// resolveMatched walks `total` entries, calls `resolve` to get the candidate
// content_id for each, and produces deduped, position-numbered replacements
// plus an unmatched count. Shared by all three source backends.
func resolveMatchedWithLimit(total int, limit *int, resolve func(i int) string) ([]userstore.CollectionItemReplacement, int, int) {
	capacity := total
	if limit != nil && *limit > 0 && *limit < total {
		capacity = *limit
	}
	matched := make([]userstore.CollectionItemReplacement, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	unmatched := 0
	scanned := 0
	for i := 0; i < total; i++ {
		scanned = i + 1
		contentID := resolve(i)
		if contentID == "" {
			unmatched++
			continue
		}
		if _, dup := seen[contentID]; dup {
			continue
		}
		seen[contentID] = struct{}{}
		matched = append(matched, userstore.CollectionItemReplacement{
			MediaItemID: contentID,
			Position:    len(matched),
		})
		if collectionutil.ItemLimitReached(len(matched), limit) {
			break
		}
	}
	return matched, unmatched, scanned
}

// collectionResolveLimit returns the limit to pass into resolveMatchedWithLimit.
//
// When LibraryIDs is set we cannot break early on cfg.Limit during the resolve
// pass: filterByLibraries runs after resolve and may drop most items, so an
// early break would leave us short of cfg.Limit final items. Returning nil
// disables the inline break; limitCollectionItems then truncates to cfg.Limit
// after filtering. This trades a wider GetItemsInFolders IN-array (bounded by
// the source-fetch cap) for a correct result count when the library filter is
// selective.
func collectionResolveLimit(cfg SourceConfig) *int {
	if len(cfg.LibraryIDs) > 0 {
		return nil
	}
	return cfg.Limit
}

func limitCollectionItems(items []userstore.CollectionItemReplacement, limit *int) []userstore.CollectionItemReplacement {
	if limit == nil || *limit <= 0 || len(items) <= *limit {
		return items
	}
	items = items[:*limit]
	for i := range items {
		items[i].Position = i
	}
	return items
}

func (s *Service) fetchMDBListEntries(ctx context.Context, url string) ([]mdblistEntry, error) {
	url = NormalizeMDBListURL(url)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating mdblist request: %w", err)
	}
	res, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching mdblist list: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("mdblist request failed with status %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("reading mdblist response: %w", err)
	}
	var entries []mdblistEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parsing mdblist response: %w", err)
	}
	return entries, nil
}

// ── TMDB presets ─────────────────────────────────────────────────────────────

func (s *Service) syncTMDB(ctx context.Context, store userstore.UserStore, collection *userstore.Collection, cfg SourceConfig, startedAt time.Time) (*SyncResult, *userstore.Collection, error) {
	if s.TMDBCollections == nil {
		return nil, nil, fmt.Errorf("TMDB sync requires configured TMDB access")
	}
	preset := cfg.Preset
	mediaType := cfg.MediaType
	timeWindow := cfg.TimeWindow
	if timeWindow == "" && preset == "trending" {
		timeWindow = "day"
	}
	limit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TMDBCollections.GetCollectionPreset(ctx, preset, mediaType, timeWindow, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching TMDB preset: %w", err)
	}

	// TMDB returns mixed-media-type results (the "trending all" preset can
	// emit both movie and tv). Batch by item type so each gets a single
	// catalog lookup instead of N round-trips through GetByExternalID.
	var movieBatch, seriesBatch catalog.ExternalIDBatch
	for _, entry := range results {
		batch := &movieBatch
		if entry.MediaType == "tv" {
			batch = &seriesBatch
		}
		if entry.ID > 0 {
			batch.TMDBIDs = append(batch.TMDBIDs, fmt.Sprintf("%d", entry.ID))
		}
		if entry.IMDbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, entry.IMDbID)
		}
		if entry.TVDBID > 0 && entry.MediaType == "tv" {
			batch.TVDBIDs = append(batch.TVDBIDs, fmt.Sprintf("%d", entry.TVDBID))
		}
	}
	movieLookup, err := s.items.GetByExternalIDs(ctx, movieBatch, "movie")
	if err != nil {
		return nil, nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, nil, err
	}

	resolveLimit := collectionResolveLimit(cfg)
	matched, unmatched, scanned := resolveMatchedWithLimit(len(results), resolveLimit, func(i int) string {
		entry := results[i]
		itemType := "movie"
		lookup := movieLookup
		if entry.MediaType == "tv" {
			itemType = "series"
			lookup = seriesLookup
		}
		var tmdb string
		if entry.ID > 0 {
			tmdb = fmt.Sprintf("%d", entry.ID)
		}
		var tvdb string
		if entry.TVDBID > 0 {
			tvdb = fmt.Sprintf("%d", entry.TVDBID)
		}
		return resolveCandidate(lookup, itemType, tvdb, tmdb, entry.IMDbID)
	})
	matched, droppedByLib, err := s.filterByLibraries(ctx, matched, cfg.LibraryIDs)
	if err != nil {
		return nil, nil, err
	}
	matched = limitCollectionItems(matched, cfg.Limit)
	return s.applyResult(ctx, store, collection, startedAt, matched, len(results), scanned, unmatched+droppedByLib)
}

// ── Trakt presets ────────────────────────────────────────────────────────────

func (s *Service) syncTrakt(ctx context.Context, store userstore.UserStore, collection *userstore.Collection, cfg SourceConfig, startedAt time.Time) (*SyncResult, *userstore.Collection, error) {
	if s.TraktCollections == nil {
		return nil, nil, fmt.Errorf("Trakt sync requires configured Trakt access")
	}
	preset := strings.TrimSpace(cfg.Preset)
	mediaType := strings.TrimSpace(cfg.MediaType)
	if mediaType == "" {
		mediaType = "movie"
	}
	if preset != "trending" && preset != "popular" && preset != "recommended" {
		return nil, nil, fmt.Errorf("unsupported Trakt preset: %s", preset)
	}

	accessToken := ""
	if preset == "recommended" {
		profileID := strings.TrimSpace(cfg.ProfileID)
		if profileID == "" {
			profileID = collection.CreatorProfileID
		}
		if profileID == "" || s.TraktTokenResolver == nil {
			return nil, nil, fmt.Errorf("Trakt recommendations require a profile binding")
		}
		token, err := s.TraktTokenResolver.ResolveTraktAccessToken(ctx, profileID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolving Trakt access token: %w", err)
		}
		accessToken = token
	}

	limit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TraktCollections.GetCollectionPreset(ctx, preset, mediaType, limit, accessToken)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching Trakt preset: %w", err)
	}

	itemType := "movie"
	if mediaType == "tv" {
		itemType = "series"
	}
	var batch catalog.ExternalIDBatch
	for _, entry := range results {
		if entry.TMDBID > 0 {
			batch.TMDBIDs = append(batch.TMDBIDs, fmt.Sprintf("%d", entry.TMDBID))
		}
		if entry.IMDbID != "" {
			batch.IMDbIDs = append(batch.IMDbIDs, entry.IMDbID)
		}
		if entry.TVDBID > 0 && itemType == "series" {
			batch.TVDBIDs = append(batch.TVDBIDs, fmt.Sprintf("%d", entry.TVDBID))
		}
	}
	lookup, err := s.items.GetByExternalIDs(ctx, batch, itemType)
	if err != nil {
		return nil, nil, err
	}

	resolveLimit := collectionResolveLimit(cfg)
	matched, unmatched, scanned := resolveMatchedWithLimit(len(results), resolveLimit, func(i int) string {
		entry := results[i]
		var tmdb, tvdb string
		if entry.TMDBID > 0 {
			tmdb = fmt.Sprintf("%d", entry.TMDBID)
		}
		if entry.TVDBID > 0 {
			tvdb = fmt.Sprintf("%d", entry.TVDBID)
		}
		return resolveCandidate(lookup, itemType, tvdb, tmdb, entry.IMDbID)
	})
	matched, droppedByLib, err := s.filterByLibraries(ctx, matched, cfg.LibraryIDs)
	if err != nil {
		return nil, nil, err
	}
	matched = limitCollectionItems(matched, cfg.Limit)
	return s.applyResult(ctx, store, collection, startedAt, matched, len(results), scanned, unmatched+droppedByLib)
}

// ── Result application ───────────────────────────────────────────────────────

func (s *Service) applyResult(
	ctx context.Context,
	store userstore.UserStore,
	collection *userstore.Collection,
	startedAt time.Time,
	matched []userstore.CollectionItemReplacement,
	sourceTotal int,
	scanned int,
	unmatched int,
) (*SyncResult, *userstore.Collection, error) {
	if err := store.ReplaceCollectionItems(ctx, collection.ID, matched); err != nil {
		return nil, nil, err
	}
	completedAt := time.Now().UTC()

	status := "success"
	if unmatched > 0 {
		status = "warning"
	}
	// Report the full source size as the denominator so users see
	// "Matched 10 of 200" rather than "Matched 10 of 10" when the limit
	// truncated mid-source; the trailing clause exposes the actual scan depth.
	message := fmt.Sprintf("Matched %d of %d entries", len(matched), sourceTotal)
	if scanned < sourceTotal {
		message = fmt.Sprintf("%s (item limit reached after %d scanned)", message, scanned)
	}

	var nextSyncAt *time.Time
	if collection.SyncSchedule != nil && *collection.SyncSchedule != "" {
		nextSyncAt = catalog.ComputeNextSyncAtFrom(*collection.SyncSchedule, completedAt)
	}

	if err := store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
		ID:         collection.ID,
		Status:     status,
		Message:    message,
		ItemCount:  len(matched),
		LastSyncAt: completedAt,
		NextSyncAt: nextSyncAt,
	}); err != nil {
		return nil, nil, err
	}

	updated := *collection
	updated.LastSyncAt = &completedAt
	updated.LastSyncStatus = status
	updated.LastSyncMessage = message
	updated.ItemCount = len(matched)
	updated.NextSyncAt = nextSyncAt

	s.logger.Info("user collection synced",
		"collection_id", collection.ID,
		"status", status,
		"matched", len(matched),
		"unmatched", unmatched,
		"scanned", scanned,
		"total", sourceTotal,
		"duration", completedAt.Sub(startedAt).Round(time.Millisecond),
	)

	return &SyncResult{
		Status:         status,
		Message:        message,
		ItemsMatched:   len(matched),
		ItemsUnmatched: unmatched,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	}, &updated, nil
}
