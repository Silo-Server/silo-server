package usercollections

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/collectionutil"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ErrOwnerAccessUnavailable reports that a sync could not resolve what the
// collection's owner profile may access. The sync records it as the
// collection's failed status and leaves the members unchanged; it never fills
// the collection from the whole catalog instead.
var ErrOwnerAccessUnavailable = errors.New("couldn't check which titles this collection's owner can access, so the collection was not updated")

// Service performs sync runs for user-owned imported collections. The result
// of each sync is written into user_personal_collection_items via the
// per-user store. Members are limited to titles the collection's owner
// profile can access, so the item limit fills with titles the owner can see;
// catalog reads still apply each viewer's own access on top.
type Service struct {
	storeProvider userstore.UserStoreProvider
	items         *catalog.ItemRepository
	libraryItems  *catalog.LibraryItemRepository
	owners        catalog.PersonalCollectionAccess
	httpClient    *http.Client
	logger        *slog.Logger

	TMDBCollections    catalog.TMDBCollectionFetcher
	TMDBLists          catalog.TMDBListFetcher
	TraktCollections   catalog.TraktCollectionFetcher
	TraktTokenResolver catalog.TraktAccessTokenResolver

	// Collages builds the collage of a synced collection without an uploaded
	// or imported poster; nil when artwork storage is not configured.
	Collages *catalog.PersonalCollectionCollages
}

// NewService builds the sync service. owners resolves each collection
// owner's access; without it every sync fails with ErrOwnerAccessUnavailable.
func NewService(
	storeProvider userstore.UserStoreProvider,
	items *catalog.ItemRepository,
	libraryItems *catalog.LibraryItemRepository,
	owners catalog.PersonalCollectionAccess,
	httpClient *http.Client,
	logger *slog.Logger,
) *Service {
	httpClient = collectionutil.MDBListHTTPClient(httpClient)
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		storeProvider: storeProvider,
		items:         items,
		libraryItems:  libraryItems,
		owners:        owners,
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
	result, _, err := s.RunSync(ctx, userID, store, collection)
	return result, err
}

// sourceMatcher fetches a collection's source list and returns, for each
// entry in source order, the catalog content_id it names ("" when the
// catalog has no such title).
type sourceMatcher func(ctx context.Context, collection *userstore.Collection, cfg SourceConfig) ([]string, error)

// RunSync syncs an already-loaded collection of account userID. Handlers
// that have validated ownership pass the collection in to avoid a second
// GetCollection round trip. Returns both the sync result and the post-sync
// collection state so callers can render the updated row without an extra
// read.
func (s *Service) RunSync(ctx context.Context, userID int, store userstore.UserStore, collection *userstore.Collection) (*SyncResult, *userstore.Collection, error) {
	cfg, err := ParseSourceConfig(collection.SourceConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing source_config: %w", err)
	}
	var match sourceMatcher
	switch cfg.Mode {
	case SourceModeMDBList:
		match = s.matchMDBList
	case SourceModeTMDBPreset:
		match = s.matchTMDB
	case SourceModeTMDBList:
		match = s.matchTMDBList
	case SourceModeTraktPreset:
		match = s.matchTrakt
	default:
		return nil, nil, ErrSyncUnsupported
	}
	startedAt := time.Now().UTC()
	owner, err := s.ownerFilter(ctx, userID, store, collection)
	if err != nil {
		return nil, nil, err
	}
	contentIDs, err := match(ctx, collection, cfg)
	if err != nil {
		return nil, nil, err
	}
	members, scanned, unmatched, err := s.selectMembers(ctx, owner, cfg, contentIDs)
	if err != nil {
		return nil, nil, err
	}
	result, updated, err := s.applyResult(ctx, store, collection, startedAt, members, len(contentIDs), scanned, unmatched)
	if err == nil {
		s.refreshCollage(userID, store, updated, owner)
	}
	return result, updated, err
}

// refreshCollage builds, in the background, the collage a synced collection
// shows its owner when it has no uploaded or imported poster. A sync has no
// request, so the owner's content access stands in for the owner's own read;
// other viewers' collages are built when they first read the collection.
func (s *Service) refreshCollage(userID int, store userstore.UserStore, c *userstore.Collection, owner catalog.AccessFilter) {
	RefreshCollage(s.Collages, store, userID, c, owner)
}

// ownerFilter resolves the access of the collection's owner profile, which
// limits its members. A scheduled sync has no request, so the owner is the
// only profile whose access can apply. When it cannot be resolved, the sync
// fails: the failure is recorded on the collection, so the owner sees it
// whether the sync ran on a schedule or by hand, and its members stay as
// they were.
func (s *Service) ownerFilter(ctx context.Context, userID int, store userstore.UserStore, collection *userstore.Collection) (catalog.AccessFilter, error) {
	var (
		filter catalog.AccessFilter
		err    error
	)
	switch {
	case s.owners == nil:
		err = errors.New("owner access is not configured")
	case collection.CreatorProfileID == "":
		err = errors.New("the collection has no owner profile")
	default:
		filter, err = s.owners.OwnerFilter(ctx, userID, collection.CreatorProfileID)
	}
	if err == nil {
		return filter, nil
	}
	s.logger.ErrorContext(ctx, "user collection sync: resolving owner access failed",
		"user_id", userID,
		"collection_id", collection.ID,
		"owner_profile_id", collection.CreatorProfileID,
		"error", err,
	)
	if stateErr := store.UpdateCollectionSyncState(ctx, userstore.UpdateCollectionSyncStateInput{
		ID:                collection.ID,
		Status:            "failed",
		Message:           ErrOwnerAccessUnavailable.Error(),
		ItemCount:         collection.ItemCount,
		LastSyncAt:        time.Now().UTC(),
		NextSyncAt:        collection.NextSyncAt,
		ScheduleAtStart:   collection.SyncSchedule,
		NextSyncAtAtStart: collection.NextSyncAt,
	}); stateErr != nil {
		s.logger.ErrorContext(ctx, "user collection sync: recording the failed sync failed",
			"user_id", userID,
			"collection_id", collection.ID,
			"error", stateErr,
		)
	}
	return catalog.AccessFilter{}, ErrOwnerAccessUnavailable
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

func (s *Service) matchMDBList(ctx context.Context, collection *userstore.Collection, cfg SourceConfig) ([]string, error) {
	urls := collectionutil.MDBListURLCandidates(cfg.URL, collection.SourceURL)
	if len(urls) == 0 {
		return nil, fmt.Errorf("mdblist sync: url is required")
	}

	fetchLimit := collectionutil.SourceFetchLimit(cfg.Limit)
	entries, err := collectionutil.FetchMDBListWithFallback(urls, func(url string) ([]mdblistEntry, error) {
		return s.fetchMDBListEntries(ctx, url, fetchLimit)
	})
	if err != nil {
		return nil, err
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
		return nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}

	return matchEntries(len(entries), func(i int) string {
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
	}), nil
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

// matchEntries resolves each of total source entries to its catalog
// content_id, in source order.
func matchEntries(total int, resolve func(i int) string) []string {
	contentIDs := make([]string, total)
	for i := range contentIDs {
		contentIDs[i] = resolve(i)
	}
	return contentIDs
}

// selectMembers picks the collection's members from the matched source
// entries: titles the owner can access and, when the source config names
// libraries, that are in one of them. All matches are checked before the item
// limit applies, so titles the owner can't access never use up the limit.
func (s *Service) selectMembers(ctx context.Context, owner catalog.AccessFilter, cfg SourceConfig, contentIDs []string) ([]userstore.CollectionItemReplacement, int, int, error) {
	ids := make([]string, 0, len(contentIDs))
	seen := make(map[string]struct{}, len(contentIDs))
	for _, id := range contentIDs {
		if _, dup := seen[id]; id == "" || dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	allowed, err := s.items.EnsureAccessibleIDs(ctx, ids, owner)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("checking the owner's access to matched titles: %w", err)
	}
	if len(cfg.LibraryIDs) > 0 && s.libraryItems != nil {
		// A title must sit in a chosen library the owner can also access, not
		// pass each check through a different library.
		libraries := cfg.LibraryIDs
		if owner.AllowedLibraryIDs != nil {
			libraries = slices.DeleteFunc(slices.Clone(libraries), func(id int) bool {
				return !slices.Contains(owner.AllowedLibraryIDs, id)
			})
		}
		inLibraries := map[string]bool{}
		if len(libraries) > 0 {
			inLibraries, err = s.libraryItems.GetItemsInFolders(ctx, ids, libraries)
			if err != nil {
				return nil, 0, 0, err
			}
		}
		for id := range allowed {
			allowed[id] = inLibraries[id]
		}
	}
	members, scanned, unmatched := pickMembers(contentIDs, allowed, cfg.Limit)
	return members, scanned, unmatched, nil
}

// pickMembers walks the matched source entries in order and keeps each
// allowed title once, until limit members are kept. It returns the members,
// the number of entries scanned, and how many of those were unmatched. An
// entry whose title is not allowed counts as unmatched, exactly like one the
// catalog lacks, so the sync summary never reveals titles outside the
// owner's access.
func pickMembers(contentIDs []string, allowed map[string]bool, limit *int) ([]userstore.CollectionItemReplacement, int, int) {
	capacity := len(contentIDs)
	if limit != nil && *limit > 0 && *limit < capacity {
		capacity = *limit
	}
	members := make([]userstore.CollectionItemReplacement, 0, capacity)
	seen := make(map[string]struct{}, capacity)
	scanned, unmatched := 0, 0
	for _, id := range contentIDs {
		scanned++
		if !allowed[id] {
			unmatched++
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		members = append(members, userstore.CollectionItemReplacement{MediaItemID: id, Position: len(members)})
		if collectionutil.ItemLimitReached(len(members), limit) {
			break
		}
	}
	return members, scanned, unmatched
}

func (s *Service) fetchMDBListEntries(ctx context.Context, url string, maxEntries int) ([]mdblistEntry, error) {
	return collectionutil.FetchMDBListJSON[mdblistEntry](ctx, s.httpClient, url, maxEntries)
}

// ── TMDB presets ─────────────────────────────────────────────────────────────

func (s *Service) matchTMDB(ctx context.Context, _ *userstore.Collection, cfg SourceConfig) ([]string, error) {
	if s.TMDBCollections == nil {
		return nil, fmt.Errorf("TMDB sync requires configured TMDB access")
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
		// The error text reaches the collection's stored sync message, and a
		// transport error embeds the request URL, which carries the API key.
		return nil, fmt.Errorf("fetching TMDB preset: %w", logredact.SanitizeURLError(err))
	}
	return s.matchTMDBEntries(ctx, results)
}

// ── TMDB lists ───────────────────────────────────────────────────────────────

func (s *Service) matchTMDBList(ctx context.Context, collection *userstore.Collection, cfg SourceConfig) ([]string, error) {
	if s.TMDBLists == nil {
		return nil, fmt.Errorf("TMDB list sync requires configured TMDB access")
	}
	listURL := cfg.URL
	if strings.TrimSpace(listURL) == "" {
		listURL = collection.SourceURL
	}
	listID, err := collectionutil.ParseTMDBListURL(listURL)
	if err != nil {
		return nil, fmt.Errorf("TMDB list sync: %w", err)
	}
	results, err := s.TMDBLists.GetList(ctx, listID, collectionutil.SourceFetchLimit(cfg.Limit))
	if err != nil {
		return nil, fmt.Errorf("fetching TMDB list %d: %w", listID, logredact.SanitizeURLError(err))
	}
	return s.matchTMDBEntries(ctx, results)
}

// matchTMDBEntries matches fetched TMDB entries against the catalog in source
// order. Shared by the TMDB preset and list sources.
func (s *Service) matchTMDBEntries(ctx context.Context, results []catalog.TMDBCollectionEntry) ([]string, error) {
	// TMDB returns mixed-media-type results (the "trending all" preset and
	// user lists can emit both movie and tv). Batch by item type so each gets
	// a single catalog lookup instead of N round-trips through GetByExternalID.
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
		return nil, err
	}
	seriesLookup, err := s.items.GetByExternalIDs(ctx, seriesBatch, "series")
	if err != nil {
		return nil, err
	}

	return matchEntries(len(results), func(i int) string {
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
	}), nil
}

// ── Trakt presets ────────────────────────────────────────────────────────────

func (s *Service) matchTrakt(ctx context.Context, collection *userstore.Collection, cfg SourceConfig) ([]string, error) {
	if s.TraktCollections == nil {
		return nil, errors.New("Trakt sync requires configured Trakt access") //nolint:staticcheck // ST1005: user-facing sync status, matches library collection wording
	}
	preset := strings.TrimSpace(cfg.Preset)
	mediaType := strings.TrimSpace(cfg.MediaType)
	if mediaType == "" {
		mediaType = "movie"
	}
	if preset != "trending" && preset != "popular" && preset != "recommended" {
		return nil, fmt.Errorf("unsupported Trakt preset: %s", preset)
	}

	accessToken := ""
	if preset == "recommended" {
		profileID := strings.TrimSpace(cfg.ProfileID)
		if profileID == "" {
			profileID = collection.CreatorProfileID
		}
		if profileID == "" || s.TraktTokenResolver == nil {
			return nil, errors.New("Trakt recommendations require a profile binding") //nolint:staticcheck // ST1005: user-facing sync status, matches library collection wording
		}
		token, err := s.TraktTokenResolver.ResolveTraktAccessToken(ctx, profileID)
		if err != nil {
			return nil, fmt.Errorf("resolving Trakt access token: %w", err)
		}
		accessToken = token
	}

	limit := collectionutil.SourceFetchLimit(cfg.Limit)
	results, err := s.TraktCollections.GetCollectionPreset(ctx, preset, mediaType, limit, accessToken)
	if err != nil {
		return nil, fmt.Errorf("fetching Trakt preset: %w", err)
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
		return nil, err
	}

	return matchEntries(len(results), func(i int) string {
		entry := results[i]
		var tmdb, tvdb string
		if entry.TMDBID > 0 {
			tmdb = fmt.Sprintf("%d", entry.TMDBID)
		}
		if entry.TVDBID > 0 {
			tvdb = fmt.Sprintf("%d", entry.TVDBID)
		}
		return resolveCandidate(lookup, itemType, tvdb, tmdb, entry.IMDbID)
	}), nil
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
		ID:                collection.ID,
		Status:            status,
		Message:           message,
		ItemCount:         len(matched),
		LastSyncAt:        completedAt,
		NextSyncAt:        nextSyncAt,
		ScheduleAtStart:   collection.SyncSchedule,
		NextSyncAtAtStart: collection.NextSyncAt,
	}); err != nil {
		return nil, nil, err
	}

	// Read the row back: a schedule edited while the sync ran, on any node,
	// kept its own next run, and the caller renders what was stored.
	updated, err := store.GetCollection(ctx, collection.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading the synced collection: %w", err)
	}

	s.logger.InfoContext(ctx, "user collection synced",
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
	}, updated, nil
}
