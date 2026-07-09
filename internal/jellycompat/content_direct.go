package jellycompat

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// AccessFilterResolver resolves catalog access constraints for a compat user.
type AccessFilterResolver func(ctx context.Context, userID int, profileID string) catalog.AccessFilter

// LibraryPosterPresigner generates presigned URLs for library poster S3 keys.
type LibraryPosterPresigner interface {
	PresignGetURL(ctx context.Context, bucket, key string, expiry time.Duration) (string, error)
	Bucket() string
}

// browseSource is the subset of *catalog.BrowseRepository that
// directContentService relies on. Defined as an interface so tests can
// substitute a stub without standing up a Postgres pool.
type browseSource interface {
	BrowsePage(ctx context.Context, filters catalog.BrowseFilters, includeTotal bool) (*catalog.BrowseResult, error)
	BrowseRecentlyAddedAcrossLibraries(ctx context.Context, base catalog.BrowseFilters, libraryIDs []int) (*catalog.BrowseResult, error)
	ListGenres(ctx context.Context, filters catalog.BrowseFilters) ([]string, error)
}

const (
	compatBrowseChunkLimit = 100
	compatBrowseMaxLimit   = 1000
	// compatDefaultBrowseLimit is the page size used when a client omits Limit.
	// Shared by BrowseItems and the /Items/Latest section fast path so the two
	// paths always agree on the default page size.
	compatDefaultBrowseLimit = 24
)

// compatVideoTypes is the media_items type scope the Jellyfin compat surface
// exposes when a request carries no explicit type filter. Silo serves
// audiobooks and podcasts through the audiobookshelf-compat API instead, so
// those rows must never leak into jellycompat responses.
const compatVideoTypes = "movie,series,episode"

// compatVideoTypeList is compatVideoTypes as a slice for APIs that take
// []string (e.g. ItemRepository.Search).
var compatVideoTypeList = strings.Split(compatVideoTypes, ",")

// compatExcludedMediaTypes lists the media_items.type values the Jellyfin
// compat surface never exposes. Injected into every catalog.AccessFilter the
// compat layer resolves (see withCompatAccessExclusions) so access-filtered
// queries — search, favorites, recommendations, detail, batch hydration —
// inherit the exclusion without per-call-site guards.
var compatExcludedMediaTypes = []string{"audiobook", "podcast"}

func isCompatExcludedMediaType(mediaType string) bool {
	for _, excluded := range compatExcludedMediaTypes {
		if strings.EqualFold(strings.TrimSpace(mediaType), excluded) {
			return true
		}
	}
	return false
}

// isCompatHiddenLibraryType reports whether a media folder type marks a
// library the Jellyfin compat surface hides. Audiobook ('audiobooks'
// canonical, 'audiobook' legacy — mirrors internal/audiobooks/media_store.go)
// and podcast libraries are served by the ABS-compat API instead.
func isCompatHiddenLibraryType(folderType string) bool {
	switch strings.ToLower(strings.TrimSpace(folderType)) {
	case "audiobooks", "audiobook", "podcasts", "podcast":
		return true
	}
	return false
}

// withCompatAccessExclusions merges the compat surface's media-type
// exclusions into a resolved access filter, preserving any exclusions the
// base resolver already supplied. Centralizing the exclusion here lets
// catalog query builders enforce it everywhere an AccessFilter flows.
func withCompatAccessExclusions(filter catalog.AccessFilter) catalog.AccessFilter {
	merged := make([]string, 0, len(filter.ExcludedMediaTypes)+len(compatExcludedMediaTypes))
	merged = append(merged, filter.ExcludedMediaTypes...)
	for _, excluded := range compatExcludedMediaTypes {
		present := false
		for _, existing := range merged {
			if strings.EqualFold(existing, excluded) {
				present = true
				break
			}
		}
		if !present {
			merged = append(merged, excluded)
		}
	}
	filter.ExcludedMediaTypes = merged
	return filter
}

// compatAccessFilterResolver wraps an AccessFilterResolver so every filter it
// returns carries the compat media-type exclusions. A nil base resolver still
// yields exclusion-only filters.
func compatAccessFilterResolver(base AccessFilterResolver) AccessFilterResolver {
	return func(ctx context.Context, userID int, profileID string) catalog.AccessFilter {
		if base == nil {
			return withCompatAccessExclusions(catalog.AccessFilter{})
		}
		return withCompatAccessExclusions(base(ctx, userID, profileID))
	}
}

// compatNoMatchType is a sentinel media type that matches no media_items row.
// Used when a caller's explicit type filter reduces to nothing exposable so
// the query returns empty instead of silently broadening to all video types.
const compatNoMatchType = "__compat_none__"

// compatAllowedTypeSet is the closed set of catalog types an explicit compat
// type filter may pass through ("season" matches no media_items row but is
// kept so season-typed filters keep their empty-result semantics).
var compatAllowedTypeSet = map[string]struct{}{
	"movie":   {},
	"series":  {},
	"episode": {},
	"season":  {},
}

// compatScopedSearchTypes clamps an item-type filter to the types the compat
// surface exposes: empty defaults to all video types, explicit filters keep
// only allowlisted entries, and a filter that reduces to nothing returns the
// no-match sentinel.
func compatScopedSearchTypes(itemTypes []string) []string {
	if len(itemTypes) == 0 {
		return compatVideoTypeList
	}
	kept := make([]string, 0, len(itemTypes))
	for _, t := range itemTypes {
		t = strings.ToLower(strings.TrimSpace(t))
		if _, ok := compatAllowedTypeSet[t]; !ok {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == 0 {
		return []string{compatNoMatchType}
	}
	return kept
}

// compatScopedTypes is compatScopedSearchTypes for comma-separated filters.
func compatScopedTypes(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return compatVideoTypes
	}
	return strings.Join(compatScopedSearchTypes(strings.Split(raw, ",")), ",")
}

// itemAccessSource is the subset of *catalog.ItemRepository that
// directContentService season/episode handlers rely on. Defined as an
// interface so tests can substitute a stub without standing up a Postgres
// pool.
type itemAccessSource interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
	Search(ctx context.Context, query string, itemTypes []string, limit, offset int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error)
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
}

// folderListSource is the subset of *catalog.FolderRepository that
// directContentService relies on. Defined as an interface so tests can
// substitute a stub without standing up a Postgres pool.
type folderListSource interface {
	GetEnabled(ctx context.Context) ([]*models.MediaFolder, error)
	ListByIDs(ctx context.Context, ids []int) ([]*models.MediaFolder, error)
}

// seasonListSource is the subset of *catalog.SeasonRepository that
// directContentService relies on.
type seasonListSource interface {
	ListBySeries(ctx context.Context, seriesID string) ([]*models.Season, error)
	GetBySeriesAndNumber(ctx context.Context, seriesID string, seasonNum int) (*models.Season, error)
	GetByID(ctx context.Context, contentID string) (*models.Season, error)
}

// episodeListSource is the subset of *catalog.EpisodeRepository that
// directContentService relies on. ListBySeriesGroupedBySeason replaces the
// previous N+1 per-season ListBySeason loop in ListSeasons (audit
// 2026-05-01 §2.2).
type episodeListSource interface {
	ListBySeason(ctx context.Context, seriesID string, seasonNum int) ([]*models.Episode, error)
	ListBySeriesGroupedBySeason(ctx context.Context, seriesID string) (map[int][]*models.Episode, error)
	ListBySeriesIDs(ctx context.Context, seriesIDs []string) (map[string][]*models.Episode, error)
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.Episode, error)
}

// directContentService implements ContentService by calling catalog repos directly.
type directContentService struct {
	browseRepo      browseSource
	itemRepo        itemAccessSource
	searchProvider  catalog.CatalogSearchProvider
	seasonRepo      seasonListSource
	episodeRepo     episodeListSource
	detailSvc       *catalog.DetailService
	folderRepo      folderListSource
	storeProvider   userstore.UserStoreProvider
	accessFilter    AccessFilterResolver
	posterPresigner LibraryPosterPresigner
	presignTTL      time.Duration
}

func newDirectContentService(
	browseRepo *catalog.BrowseRepository,
	itemRepo *catalog.ItemRepository,
	seasonRepo *catalog.SeasonRepository,
	episodeRepo *catalog.EpisodeRepository,
	detailSvc *catalog.DetailService,
	folderRepo folderListSource,
	storeProvider userstore.UserStoreProvider,
	accessFilter AccessFilterResolver,
	searchProvider catalog.CatalogSearchProvider,
) *directContentService {
	return &directContentService{
		browseRepo:     browseRepo,
		itemRepo:       itemRepo,
		searchProvider: searchProvider,
		seasonRepo:     seasonRepo,
		episodeRepo:    episodeRepo,
		detailSvc:      detailSvc,
		folderRepo:     folderRepo,
		storeProvider:  storeProvider,
		accessFilter:   accessFilter,
	}
}

func (s *directContentService) resolveFilter(ctx context.Context, session *Session) catalog.AccessFilter {
	if s.accessFilter != nil {
		return withCompatAccessExclusions(s.accessFilter(ctx, session.StreamAppUserID, session.ProfileID))
	}
	return withCompatAccessExclusions(catalog.AccessFilter{})
}

func applyCompatPresentationLibrary(filter catalog.AccessFilter, libraryID *int) catalog.AccessFilter {
	if libraryID != nil && *libraryID > 0 {
		filter.PresentationLibraryID = libraryID
	}
	return filter
}

func clampMaxContentRating(existing, requested string) string {
	existing = strings.TrimSpace(existing)
	requested = strings.TrimSpace(requested)
	switch {
	case existing == "":
		return requested
	case requested == "":
		return existing
	case access.RatingAllowed(existing, requested):
		return existing
	case access.RatingAllowed(requested, existing):
		return requested
	default:
		return existing
	}
}

func (s *directContentService) ListUserLibraries(ctx context.Context, session *Session) ([]upstreamUserLibrary, error) {
	filter := s.resolveFilter(ctx, session)

	folders, err := s.accessibleFolders(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}

	disabled := make(map[int]struct{}, len(filter.DisabledLibraryIDs))
	for _, id := range filter.DisabledLibraryIDs {
		disabled[id] = struct{}{}
	}

	libraries := make([]upstreamUserLibrary, 0, len(folders))
	for _, f := range folders {
		if _, ok := disabled[f.ID]; ok {
			continue
		}
		// Audiobook and podcast libraries are served by the ABS-compat API,
		// never here.
		if isCompatHiddenLibraryType(f.Type) {
			continue
		}
		lib := upstreamUserLibrary{
			ID:         f.ID,
			Name:       f.Name,
			Type:       f.Type,
			PosterPath: f.PosterPath,
		}
		if f.PosterPath != "" && s.posterPresigner != nil {
			ttl := s.presignTTL
			if ttl <= 0 {
				ttl = 4 * time.Hour
			}
			if u, err := s.posterPresigner.PresignGetURL(ctx, s.posterPresigner.Bucket(), f.PosterPath, ttl); err == nil {
				lib.PosterURL = u
			}
		}
		libraries = append(libraries, lib)
	}
	return libraries, nil
}

// accessibleFolders resolves the media folders the viewer may browse, honoring
// the allowlist semantics shared by the library and browse endpoints. A nil
// AllowedLibraryIDs means unrestricted (all enabled libraries); a non-nil but
// empty allowlist grants access to no libraries at all.
func (s *directContentService) accessibleFolders(ctx context.Context, filter catalog.AccessFilter) ([]*models.MediaFolder, error) {
	if filter.AllowedLibraryIDs != nil {
		if len(filter.AllowedLibraryIDs) == 0 {
			return nil, nil
		}
		return s.folderRepo.ListByIDs(ctx, filter.AllowedLibraryIDs)
	}
	return s.folderRepo.GetEnabled(ctx)
}

// accessibleLibraryIDs resolves the library IDs the viewer may browse, applying
// the same allowlist/disabled-list semantics as ListUserLibraries. It is used to
// fan a no-parentId recently_added browse into one fast single-library query per
// library.
func (s *directContentService) accessibleLibraryIDs(ctx context.Context, filter catalog.AccessFilter) ([]int, error) {
	folders, err := s.accessibleFolders(ctx, filter)
	if err != nil {
		return nil, err
	}
	disabled := make(map[int]struct{}, len(filter.DisabledLibraryIDs))
	for _, id := range filter.DisabledLibraryIDs {
		disabled[id] = struct{}{}
	}
	ids := make([]int, 0, len(folders))
	for _, f := range folders {
		if _, ok := disabled[f.ID]; ok {
			continue
		}
		// Audiobook and podcast libraries are served by the ABS-compat API and
		// are never exposed on the Jellyfin surface. ListUserLibraries skips
		// them; skip them here too so the recently_added fan-out can't pull
		// compat-hidden libraries into /Items/Latest and so we don't waste a
		// per-library query on folders that yield no Jellyfin-visible rows.
		if isCompatHiddenLibraryType(f.Type) {
			continue
		}
		ids = append(ids, f.ID)
	}
	return ids, nil
}

func (s *directContentService) BrowseItems(ctx context.Context, session *Session, params url.Values) (*upstreamBrowseResponse, error) {
	filter := s.resolveFilter(ctx, session)
	isPlayedFilter := params.Get("is_played") // "", "true", or "false"
	contentIDs := parseContentIDParam(params.Get("content_ids"))
	includeTotal := parseBool(params.Get("include_total"), true)

	requestedLimit := catalog.ParseIntParam(params.Get("limit"))
	if requestedLimit <= 0 {
		requestedLimit = compatDefaultBrowseLimit
	}
	if requestedLimit > compatBrowseMaxLimit {
		requestedLimit = compatBrowseMaxLimit
	}
	requestedOffset := max(catalog.ParseIntParam(params.Get("offset")), 0)

	fetchLimit := requestedLimit
	if isPlayedFilter != "" {
		// Played status is a profile overlay, so browse over-fetches bounded
		// chunks and filters them locally instead of asking catalog for every
		// row in a large library.
		fetchLimit = min(max(requestedLimit*3, compatBrowseChunkLimit), compatBrowseMaxLimit)
	}

	filters := catalog.BrowseFilters{
		Type:               compatScopedTypes(params.Get("type")),
		Genre:              params.Get("genre"),
		NamePrefix:         params.Get("name_prefix"),
		ContentIDs:         contentIDs,
		LibraryID:          catalog.ParseIntParam(params.Get("library_id")),
		LibraryIDs:         filter.AllowedLibraryIDs,
		DisabledLibraryIDs: filter.DisabledLibraryIDs,
		MaxContentRating:   clampMaxContentRating(filter.MaxContentRating, params.Get("max_content_rating")),
		PersonID:           catalog.ParseInt64Param(params.Get("person_id")),
		Sort:               params.Get("sort"),
		Order:              params.Get("order"),
		Limit:              fetchLimit,
		MaxLimit:           compatBrowseMaxLimit,
		Offset:             requestedOffset,
		RequireBackdrop:    parseBool(params.Get("require_backdrop"), false),
	}

	// A no-parentId recently_added browse (the /Items/Latest hot path) would
	// otherwise run a multi-library MIN(first_seen_at) + GROUP BY scan over the
	// whole catalog. Fan it into one fast single-library index walk per library
	// and merge in memory instead. Only offset 0 is served this way; deeper
	// pages fall back to the multi-library query below.
	crossLibraryRecentlyAdded := filters.Sort == "recently_added" && filters.LibraryID == 0
	var crossLibraryIDs []int
	if crossLibraryRecentlyAdded {
		ids, err := s.accessibleLibraryIDs(ctx, filter)
		if err != nil {
			return nil, fmt.Errorf("browse items: %w", err)
		}
		// With 0 or 1 accessible libraries the single multi-library query is
		// already the fast path, so leave behavior unchanged.
		if len(ids) >= 2 {
			crossLibraryIDs = ids
		} else {
			crossLibraryRecentlyAdded = false
		}
	}

	var collected []upstreamListItem
	totalFromCatalog := 0
	totalKnown := false
	hasMore := false
	scannedRows := 0
	maxScannedRows := requestedLimit
	if isPlayedFilter != "" {
		maxScannedRows = max(requestedLimit*5, compatBrowseChunkLimit)
	}

	for len(collected) < requestedLimit && scannedRows < maxScannedRows {
		wantTotal := includeTotal && !totalKnown
		var result *catalog.BrowseResult
		var err error
		if crossLibraryRecentlyAdded && filters.Offset == 0 {
			// The merge helper returns a Total/HasMore consistent with offset 0,
			// so wantTotal is satisfied without the expensive cross-library count.
			//
			// The fast path cannot serve a global offset — a per-library top-N walk
			// can't satisfy a deep merged offset. When the isPlayed overlay drops
			// items, the offset-paging loop would advance filters.Offset on the next
			// iteration and fall through to BrowsePage's whole-catalog
			// MIN(first_seen_at) + GROUP BY (~0.8s+ on a large catalog). Instead,
			// pull the entire over-fetch budget in this single merged index walk so
			// the loop fills from one fast call.
			//
			// Caveat: BrowseRecentlyAddedAcrossLibraries clamps its limit to
			// MaxLimit (compatBrowseMaxLimit=1000), so this fully avoids the
			// BrowsePage fall-through only while maxScannedRows <= 1000 — i.e. for
			// requestedLimit <= 200, which covers the /Items/Latest hot path
			// (limit ~24-50). For very large requestedLimit (201-1000) the clamp
			// can still leave a 2nd chunk that pages into BrowsePage; that case is
			// rare for recently-added rails and remains correct, just not optimized.
			fastFilters := filters
			if isPlayedFilter != "" && fastFilters.Limit < maxScannedRows {
				fastFilters.Limit = maxScannedRows
			}
			result, err = s.browseRepo.BrowseRecentlyAddedAcrossLibraries(ctx, fastFilters, crossLibraryIDs)
		} else {
			result, err = s.browseRepo.BrowsePage(ctx, filters, wantTotal)
		}
		if err != nil {
			return nil, fmt.Errorf("browse items: %w", err)
		}
		if wantTotal {
			totalFromCatalog = result.Total
			totalKnown = true
		}
		hasMore = result.HasMore

		localizedItems := result.Items
		if s.detailSvc != nil {
			if localized, locErr := s.detailSvc.LocalizeItemModels(ctx, result.Items, filter); locErr == nil && localized != nil {
				localizedItems = localized
			}
		}
		batch := make([]upstreamListItem, 0, len(localizedItems))
		for _, mi := range localizedItems {
			batch = append(batch, mediaItemToListItem(mi))
		}
		presignCompatListItems(ctx, s.detailSvc, batch)
		if isPlayedFilter != "" {
			// Need user data to filter by played status. The handler's
			// resolveUserStateForContentIDs call covers UserData on the wire
			// for the unfiltered case, so we only fetch here when filtering.
			s.enrichListItemsUserData(ctx, session, batch)
			wantPlayed := isPlayedFilter == "true"
			for _, item := range batch {
				played := item.UserData != nil && item.UserData.Played
				if played == wantPlayed {
					collected = append(collected, item)
				}
			}
		} else {
			collected = append(collected, batch...)
		}

		scannedRows += len(result.Items)
		if len(result.Items) == 0 || !result.HasMore {
			break
		}
		filters.Offset += len(result.Items)
	}

	// Trim to requested limit.
	if len(collected) > requestedLimit {
		collected = collected[:requestedLimit]
	}

	// The handler's resolveUserStateForContentIDs overlay only covers items
	// with their own progress rows, which a series never has — aggregate
	// series watch state here so library views carry Played/UnplayedItemCount.
	// The is_played path already did this via enrichListItemsUserData.
	// That same absence is what keeps this safe from the handler overlay:
	// userDataDTO only overrides item-level UserData when a direct progress
	// row exists, and one never does for a series id.
	if isPlayedFilter == "" {
		s.EnrichSeriesUserData(ctx, session, collected)
	}

	if totalKnown {
		hasMore = totalFromCatalog > requestedOffset+requestedLimit
	}

	return &upstreamBrowseResponse{
		Total:   totalFromCatalog,
		HasMore: hasMore,
		Items:   collected,
	}, nil
}

func parseContentIDParam(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for part := range strings.SplitSeq(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func (s *directContentService) SearchItems(ctx context.Context, session *Session, opts SearchItemsOptions) (*upstreamBrowseResponse, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), opts.LibraryID)
	itemTypes := compatScopedSearchTypes(opts.ItemTypes)

	var items []*models.MediaItem
	var total int
	var hasMore bool
	if s.searchProvider != nil {
		result, err := s.searchProvider.Search(ctx, catalog.CatalogSearchRequest{
			Query:     opts.Query,
			ItemTypes: itemTypes,
			Limit:     opts.Limit,
			Offset:    opts.Offset,
			Access:    filter,
			SkipTotal: opts.SkipTotal,
		})
		if err != nil {
			return nil, fmt.Errorf("search items: %w", err)
		}
		if result != nil {
			items = result.Items
			total = result.Total
			hasMore = result.HasMore
		}
	} else {
		var err error
		items, total, err = s.itemRepo.Search(ctx, opts.Query, itemTypes, opts.Limit, opts.Offset, filter)
		if err != nil {
			return nil, fmt.Errorf("search items: %w", err)
		}
		hasMore = opts.Offset+len(items) < total
	}

	localizedItems := items
	if s.detailSvc != nil {
		if localized, locErr := s.detailSvc.LocalizeItemModels(ctx, items, filter); locErr == nil && localized != nil {
			localizedItems = localized
		}
	}
	listItems := make([]upstreamListItem, 0, len(localizedItems))
	for _, mi := range localizedItems {
		listItems = append(listItems, mediaItemToListItem(mi))
	}
	presignCompatListItems(ctx, s.detailSvc, listItems)
	s.enrichListItemsUserData(ctx, session, listItems)

	return &upstreamBrowseResponse{
		Total:   total,
		HasMore: hasMore,
		Items:   listItems,
	}, nil
}

func (s *directContentService) GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)

	detail, err := s.detailSvc.GetItemDetail(ctx, contentID, filter)
	if err != nil {
		return nil, wrapCatalogError(err)
	}
	// ABS-surface media types are not exposed on the Jellyfin compat surface
	// (this guard also blocks PlaybackInfo, which resolves items through
	// GetItemDetail).
	if isCompatExcludedMediaType(detail.Type) {
		return nil, &HTTPError{StatusCode: 404, Message: "item not found"}
	}

	result := itemDetailToUpstream(detail)

	// Enrich with user data
	if s.storeProvider != nil {
		if store, storeErr := s.storeProvider.ForUser(ctx, session.StreamAppUserID); storeErr == nil {
			s.enrichDetailUserData(ctx, store, session.ProfileID, contentID, &result)
		}
	}

	return &result, nil
}

// enrichDetailUserData populates the per-profile UserData on an item detail from
// the user store: leaf progress for movies/episodes, and an episode rollup for
// series (which never own a progress row of their own). Shared by GetItemDetail
// and GetItemDetailsByIDs so both surfaces report identical Played/position/
// UnplayedItemCount values.
func (s *directContentService) enrichDetailUserData(ctx context.Context, store userstore.UserStore, profileID, contentID string, result *upstreamItemDetail) {
	progress, _ := userstore.GetProgressWithCompletedHistory(ctx, store, profileID, contentID)
	result.UserData = seasonUserDataFromProgress(progress)

	// A series never has a progress row of its own, so roll watch state up from
	// its episodes (mirrors applySeasonUserData) to give clients Played/
	// UnplayedItemCount at the series level.
	if result.UserData == nil && strings.EqualFold(result.Type, "series") && s.episodeRepo != nil {
		if episodesBySeries, epErr := s.episodeRepo.ListBySeriesIDs(ctx, []string{contentID}); epErr == nil {
			episodes := episodesBySeries[contentID]
			episodeIDs := modelEpisodeContentIDs(episodes)
			progressMap := chunkedProgressByMediaItems(ctx, store, profileID, episodeIDs)
			result.UserData = catalog.EpisodeRollupUserData(episodes, progressMap)
		}
	}
}

// GetItemDetailsByIDs is the batched form of GetItemDetail for a page of content
// IDs. It returns upstream item details keyed by content ID, each identical to
// what GetItemDetail would return for that id: the catalog detail is built by
// the batched DetailService path (or GetEpisodeDetailsForSeries for episodes),
// then run through the same compat-excluded-type guard, itemDetailToUpstream
// conversion, and UserData enrichment. IDs that are excluded media types,
// access-filtered, or otherwise unresolved are simply absent from the map so the
// caller renders them from list data, mirroring GetItemDetail's error path.
func (s *directContentService) GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]*upstreamItemDetail, error) {
	out := make(map[string]*upstreamItemDetail, len(contentIDs))
	if len(contentIDs) == 0 || s.detailSvc == nil {
		return out, nil
	}
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)

	details := make(map[string]*catalog.ItemDetail, len(contentIDs))

	// Movies/series (and any other media_items rows) in one batched call.
	itemDetails, err := s.detailSvc.GetItemDetailsByIDs(ctx, contentIDs, filter)
	if err != nil {
		return nil, wrapCatalogError(err)
	}
	for id, detail := range itemDetails {
		details[id] = detail
	}

	// Episodes: group the remaining ids by series and reuse the series-hoisted
	// batch path, which is the episode equivalent of GetItemDetailsByIDs.
	remaining := make([]string, 0, len(contentIDs))
	for _, id := range contentIDs {
		if _, ok := details[id]; !ok {
			remaining = append(remaining, id)
		}
	}
	if len(remaining) > 0 && s.episodeRepo != nil {
		if episodeIDsBySeries, err := s.groupEpisodesBySeries(ctx, remaining); err == nil {
			for seriesID, epIDs := range episodeIDsBySeries {
				epDetails, epErr := s.detailSvc.GetEpisodeDetailsForSeries(ctx, seriesID, epIDs, filter)
				if epErr != nil {
					// Series inaccessible or a transient error: leave these ids
					// unresolved so the caller renders them from list data,
					// matching per-item GetItemDetail erroring for each.
					continue
				}
				for id, detail := range epDetails {
					details[id] = detail
				}
			}
		}
	}

	if len(details) == 0 {
		return out, nil
	}

	// Resolve the user store once for the whole page.
	var store userstore.UserStore
	if s.storeProvider != nil {
		if resolved, storeErr := s.storeProvider.ForUser(ctx, session.StreamAppUserID); storeErr == nil {
			store = resolved
		}
	}

	// Batch the leaf-item progress lookup. enrichDetailUserData issues
	// GetProgressWithCompletedHistory per item (two point queries each), so a
	// 50-item page cost ~100 sequential round-trips. Movies/episodes own a
	// progress row, so their played/position state is resolved here in a single
	// ListProgressWithCompletedHistory call — semantically identical to the
	// per-item path (absent id → nil, completed-history synthesized). Series own
	// no progress row and need a per-series episode rollup, so they stay on the
	// per-item enrichDetailUserData path in the loop below.
	var leafProgress map[string]userstore.WatchProgress
	leafBatchFailed := false
	if store != nil {
		leafIDs := make([]string, 0, len(details))
		for id, detail := range details {
			if isCompatExcludedMediaType(detail.Type) || strings.EqualFold(detail.Type, "series") {
				continue
			}
			leafIDs = append(leafIDs, id)
		}
		if len(leafIDs) > 0 {
			if pm, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, leafIDs); err == nil {
				leafProgress = pm
			} else {
				// A transient store error must not blank played/position state
				// for the whole page at once: fall back to the per-item lookups
				// below, which degrade one id at a time exactly like the
				// pre-batch path did.
				leafBatchFailed = true
				slog.WarnContext(ctx, "jellycompat: batch leaf progress lookup failed; falling back to per-item lookups", "component", "jellycompat",
					"error", err, "leaf_count", len(leafIDs))
			}
		}
	}

	for id, detail := range details {
		if isCompatExcludedMediaType(detail.Type) {
			continue
		}
		upstream := itemDetailToUpstream(detail)
		if store != nil {
			switch {
			case strings.EqualFold(detail.Type, "series"), leafBatchFailed:
				s.enrichDetailUserData(ctx, store, session.ProfileID, id, &upstream)
			default:
				var wp *userstore.WatchProgress
				if entry, ok := leafProgress[id]; ok {
					wp = &entry
				}
				upstream.UserData = seasonUserDataFromProgress(wp)
			}
		}
		out[id] = &upstream
	}
	return out, nil
}

// groupEpisodesBySeries maps the subset of contentIDs that are episodes to their
// series IDs, returning seriesID → episode content IDs. Non-episode ids are
// silently skipped.
func (s *directContentService) groupEpisodesBySeries(ctx context.Context, contentIDs []string) (map[string][]string, error) {
	episodes, err := s.episodeRepo.GetByIDs(ctx, contentIDs)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string][]string, len(episodes))
	for _, ep := range episodes {
		if ep == nil || ep.SeriesID == "" {
			continue
		}
		grouped[ep.SeriesID] = append(grouped[ep.SeriesID], ep.ContentID)
	}
	return grouped, nil
}

func (s *directContentService) ListSeasons(ctx context.Context, session *Session, seriesID string, libraryID *int) ([]upstreamSeason, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)
	if err := s.itemRepo.EnsureAccessible(ctx, seriesID, filter); err != nil {
		return nil, wrapCatalogError(err)
	}

	seasons, err := s.seasonRepo.ListBySeries(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list seasons: %w", err)
	}

	// Single batched fetch for every available episode in the series, replacing
	// the previous per-season ListBySeason loop (audit 2026-05-01 §2.2).
	groupedEpisodes, err := s.episodeRepo.ListBySeriesGroupedBySeason(ctx, seriesID)
	if err != nil {
		return nil, fmt.Errorf("list seasons episodes: %w", err)
	}

	// Collect every episode ID for a single batched progress fetch.
	totalEpisodes := 0
	for _, eps := range groupedEpisodes {
		totalEpisodes += len(eps)
	}
	allEpisodeIDs := make([]string, 0, totalEpisodes)
	for _, eps := range groupedEpisodes {
		for _, ep := range eps {
			if ep != nil && ep.ContentID != "" {
				allEpisodeIDs = append(allEpisodeIDs, ep.ContentID)
			}
		}
	}
	progressMap := s.batchProgressForEpisodes(ctx, session, allEpisodeIDs)

	if len(seasons) > 0 {
		result := make([]upstreamSeason, 0, len(seasons))
		for _, season := range seasons {
			eps := groupedEpisodes[season.SeasonNumber]
			if s.detailSvc != nil {
				if localized, locErr := s.detailSvc.LocalizeSeasonModel(ctx, season, filter); locErr == nil && localized != nil {
					season = localized
				}
			}
			us := modelSeasonToUpstream(season, len(eps))
			applySeasonUserData(&us, eps, progressMap)
			result = append(result, us)
		}
		s.presignSeasons(ctx, result)
		return result, nil
	}

	// Fallback: seasons table is empty for this series; derive summaries from
	// the already-fetched grouped episodes (no extra aggregation query).
	seasonNumbers := make([]int, 0, len(groupedEpisodes))
	for sn := range groupedEpisodes {
		seasonNumbers = append(seasonNumbers, sn)
	}
	sort.Ints(seasonNumbers)
	result := make([]upstreamSeason, 0, len(seasonNumbers))
	for _, sn := range seasonNumbers {
		eps := groupedEpisodes[sn]
		us := syntheticSeason(seriesID, sn, len(eps))
		applySeasonUserData(&us, eps, progressMap)
		result = append(result, us)
	}
	return result, nil
}

func (s *directContentService) GetSeason(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) (*upstreamSeason, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)
	if err := s.itemRepo.EnsureAccessible(ctx, seriesID, filter); err != nil {
		return nil, wrapCatalogError(err)
	}

	season, err := s.seasonRepo.GetBySeriesAndNumber(ctx, seriesID, seasonNumber)
	if err == nil {
		episodes, _ := s.episodeRepo.ListBySeason(ctx, seriesID, seasonNumber)
		if s.detailSvc != nil {
			if localized, locErr := s.detailSvc.LocalizeSeasonModel(ctx, season, filter); locErr == nil && localized != nil {
				season = localized
			}
		}
		result := modelSeasonToUpstream(season, len(episodes))
		s.presignSeason(ctx, &result)
		s.enrichSeasonUserData(ctx, session, &result, episodes)
		return &result, nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return nil, wrapCatalogError(err)
	}

	// Fallback: season row missing; synthesize from episodes.
	episodes, epErr := s.episodeRepo.ListBySeason(ctx, seriesID, seasonNumber)
	if epErr != nil || len(episodes) == 0 {
		return nil, &HTTPError{StatusCode: 404, Message: "season not found"}
	}
	us := syntheticSeason(seriesID, seasonNumber, len(episodes))
	s.enrichSeasonUserData(ctx, session, &us, episodes)
	return &us, nil
}

func (s *directContentService) ListEpisodes(ctx context.Context, session *Session, seriesID string, seasonNumber int, libraryID *int) ([]upstreamEpisode, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)
	if err := s.itemRepo.EnsureAccessible(ctx, seriesID, filter); err != nil {
		return nil, wrapCatalogError(err)
	}

	episodes, err := s.episodeRepo.ListBySeason(ctx, seriesID, seasonNumber)
	if err != nil {
		return nil, fmt.Errorf("list episodes: %w", err)
	}

	progressMap := map[string]userstore.WatchProgress{}
	if store, err := s.userStore(ctx, session); err == nil {
		episodeIDs := make([]string, 0, len(episodes))
		for _, ep := range episodes {
			episodeIDs = append(episodeIDs, ep.ContentID)
		}
		if progressEntries, progressErr := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, episodeIDs); progressErr == nil {
			progressMap = progressEntries
		}
	}

	result := make([]upstreamEpisode, 0, len(episodes))
	for _, ep := range episodes {
		if s.detailSvc != nil {
			if localized, locErr := s.detailSvc.LocalizeEpisodeModel(ctx, ep, filter); locErr == nil && localized != nil {
				ep = localized
			}
		}
		ue := modelEpisodeToUpstream(ep, seriesID)
		if progress, ok := progressMap[ep.ContentID]; ok {
			progressCopy := progress
			ue.UserData = seasonUserDataFromProgress(&progressCopy)
		}
		result = append(result, ue)
	}
	s.presignEpisodes(ctx, result)
	return result, nil
}

func (s *directContentService) ListEpisodesBySeasonID(ctx context.Context, session *Session, seasonID string, libraryID *int) ([]upstreamEpisode, error) {
	season, err := s.seasonRepo.GetByID(ctx, seasonID)
	if err != nil {
		return nil, wrapCatalogError(err)
	}
	return s.ListEpisodes(ctx, session, season.SeriesID, season.SeasonNumber, libraryID)
}

func (s *directContentService) ListItemFilters(ctx context.Context, session *Session, params url.Values) (*upstreamItemFiltersResponse, error) {
	filter := s.resolveFilter(ctx, session)

	filters := catalog.BrowseFilters{
		Type:               compatScopedTypes(params.Get("type")),
		LibraryID:          catalog.ParseIntParam(params.Get("library_id")),
		LibraryIDs:         filter.AllowedLibraryIDs,
		DisabledLibraryIDs: filter.DisabledLibraryIDs,
		MaxContentRating:   clampMaxContentRating(filter.MaxContentRating, params.Get("max_content_rating")),
	}

	genres, err := s.browseRepo.ListGenres(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("list genres: %w", err)
	}
	return &upstreamItemFiltersResponse{Genres: genres}, nil
}

// enrichListItemsUserData adds user data to a batch of list items.
func (s *directContentService) enrichListItemsUserData(ctx context.Context, session *Session, items []upstreamListItem) {
	if s.storeProvider == nil || len(items) == 0 {
		return
	}
	store, err := s.userStore(ctx, session)
	if err != nil {
		return
	}

	contentIDs := make([]string, 0, len(items))
	for _, item := range items {
		if item.ContentID != "" {
			contentIDs = append(contentIDs, item.ContentID)
		}
	}
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, contentIDs)
	if err != nil {
		return
	}

	for i := range items {
		progress, ok := progressMap[items[i].ContentID]
		if ok {
			progressCopy := progress
			items[i].UserData = seasonUserDataFromProgress(&progressCopy)
		}
	}

	s.enrichSeriesListUserData(ctx, session, store, items)
}

// EnrichSeriesUserData is enrichSeriesListUserData with store acquisition,
// for callers that haven't already resolved the user store. It is exported on
// the ContentService interface so the native /Items/Latest fast path can apply
// the same series watch-state rollup the BrowseItems path applies (a series has
// no progress row of its own, so this rollup is the only source of its
// Played/UnplayedItemCount).
func (s *directContentService) EnrichSeriesUserData(ctx context.Context, session *Session, items []upstreamListItem) {
	if s.storeProvider == nil || len(items) == 0 {
		return
	}
	store, err := s.userStore(ctx, session)
	if err != nil {
		return
	}
	s.enrichSeriesListUserData(ctx, session, store, items)
}

// enrichSeriesListUserData fills aggregated user data for series rows in a
// list response. A series never has a progress row of its own, so Played and
// UnplayedItemCount are rolled up from episode progress, mirroring
// applySeasonUserData semantics.
func (s *directContentService) enrichSeriesListUserData(ctx context.Context, session *Session, store userstore.UserStore, items []upstreamListItem) {
	if s.episodeRepo == nil {
		return
	}
	seriesIDs := make([]string, 0, len(items))
	for i := range items {
		if items[i].UserData == nil && items[i].ContentID != "" && strings.EqualFold(items[i].Type, "series") {
			seriesIDs = append(seriesIDs, items[i].ContentID)
		}
	}
	if len(seriesIDs) == 0 {
		return
	}
	// Cap the rollup so a max-limit browse (compatBrowseMaxLimit) of a
	// series-only library can't materialize hundreds of thousands of episode
	// rows in one request. Series past the cap keep a nil UserData — the same
	// shape they had before series aggregation existed. Real clients page at
	// 20-100 items, well under the cap.
	const maxSeriesUserDataRollups = 250
	if len(seriesIDs) > maxSeriesUserDataRollups {
		seriesIDs = seriesIDs[:maxSeriesUserDataRollups]
	}
	episodesBySeries, err := s.episodeRepo.ListBySeriesIDs(ctx, seriesIDs)
	if err != nil {
		return
	}
	episodeIDs := make([]string, 0, 256)
	for _, episodes := range episodesBySeries {
		episodeIDs = append(episodeIDs, modelEpisodeContentIDs(episodes)...)
	}
	progressMap := chunkedProgressByMediaItems(ctx, store, session.ProfileID, episodeIDs)
	for i := range items {
		if items[i].UserData != nil || !strings.EqualFold(items[i].Type, "series") {
			continue
		}
		if episodes, ok := episodesBySeries[items[i].ContentID]; ok {
			items[i].UserData = catalog.EpisodeRollupUserData(episodes, progressMap)
		}
	}
}

// modelEpisodeContentIDs returns the non-empty content ids of the given episodes.
func modelEpisodeContentIDs(episodes []*models.Episode) []string {
	ids := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		if ep != nil && ep.ContentID != "" {
			ids = append(ids, ep.ContentID)
		}
	}
	return ids
}

// chunkedProgressByMediaItems batches ListProgressByMediaItems calls when a
// series list expands to thousands of episode ids: per-user stores may be
// SQLite-backed (999 bind-variable default), and chunking also keeps
// Postgres IN-lists at a sane size. Returns an empty map (never nil); chunks
// that error are skipped.
func chunkedProgressByMediaItems(ctx context.Context, store userstore.UserStore, profileID string, mediaItemIDs []string) map[string]userstore.WatchProgress {
	const chunkSize = 500
	result := make(map[string]userstore.WatchProgress, len(mediaItemIDs))
	for start := 0; start < len(mediaItemIDs); start += chunkSize {
		chunk, err := userstore.ListProgressWithCompletedHistory(ctx, store, profileID, mediaItemIDs[start:min(start+chunkSize, len(mediaItemIDs))])
		if err != nil {
			continue
		}
		for id, progress := range chunk {
			result[id] = progress
		}
	}
	return result
}

// enrichSeasonUserData adds aggregated user data for a season. Used by
// callers that handle a single season (GetSeason paths). For batched
// callers like ListSeasons, prefer applySeasonUserData with a shared
// progressMap to avoid per-season fetches.
func (s *directContentService) enrichSeasonUserData(ctx context.Context, session *Session, season *upstreamSeason, episodes []*models.Episode) {
	if s.storeProvider == nil {
		return
	}
	store, err := s.userStore(ctx, session)
	if err != nil {
		return
	}

	episodeIDs := make([]string, 0, len(episodes))
	for _, ep := range episodes {
		if ep != nil && ep.ContentID != "" {
			episodeIDs = append(episodeIDs, ep.ContentID)
		}
	}
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, episodeIDs)
	if err != nil {
		return
	}
	applySeasonUserData(season, episodes, progressMap)
}

// applySeasonUserData computes WatchedCount/UnplayedCount/Played for a season
// using a pre-fetched progressMap. Pure function — no I/O.
func applySeasonUserData(season *upstreamSeason, episodes []*models.Episode, progressMap map[string]userstore.WatchProgress) {
	season.UserData = catalog.EpisodeRollupUserData(episodes, progressMap)
	if season.UserData == nil {
		season.UserData = &catalog.SeasonUserData{}
	}
}

// batchProgressForEpisodes fetches a progress map for every episode in the
// list with a single ListProgressByMediaItems call. Returns an empty map
// (never nil) on any error so callers can index unconditionally.
func (s *directContentService) batchProgressForEpisodes(ctx context.Context, session *Session, episodeIDs []string) map[string]userstore.WatchProgress {
	if s.storeProvider == nil || len(episodeIDs) == 0 {
		return map[string]userstore.WatchProgress{}
	}
	store, err := s.userStore(ctx, session)
	if err != nil {
		return map[string]userstore.WatchProgress{}
	}
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, episodeIDs)
	if err != nil || progressMap == nil {
		return map[string]userstore.WatchProgress{}
	}
	return progressMap
}

// enrichEpisodeUserData adds user data for a single episode.
func (s *directContentService) enrichEpisodeUserData(ctx context.Context, session *Session, ep *upstreamEpisode) {
	if progressMap, err := s.progressForContentIDs(ctx, session, []string{ep.ContentID}); err == nil {
		if progress, ok := progressMap[ep.ContentID]; ok {
			progressCopy := progress
			ep.UserData = seasonUserDataFromProgress(&progressCopy)
		}
	}
}

func (s *directContentService) userStore(ctx context.Context, session *Session) (userstore.UserStore, error) {
	if s.storeProvider == nil {
		return nil, fmt.Errorf("user store is not configured")
	}
	return s.storeProvider.ForUser(ctx, session.StreamAppUserID)
}

func (s *directContentService) progressForContentIDs(ctx context.Context, session *Session, contentIDs []string) (map[string]userstore.WatchProgress, error) {
	store, err := s.userStore(ctx, session)
	if err != nil {
		return nil, err
	}
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, contentIDs)
	if err != nil {
		return nil, err
	}
	return progressMap, nil
}

func seasonUserDataFromProgress(progress *userstore.WatchProgress) *catalog.SeasonUserData {
	if progress == nil {
		return nil
	}
	return &catalog.SeasonUserData{
		PositionSeconds: progress.PositionSeconds,
		DurationSeconds: progress.DurationSeconds,
		Played:          progress.Completed,
		IsInProgress:    progress.PositionSeconds > 0,
	}
}

// --- Image URL presigning helpers ---
//
// The list-item presign implementation (presignCompatListItems) and its shared
// collect/resolve helpers live in presign_list.go so directContentService and
// ItemsHandler share one batched path. Seasons and episodes carry a single
// relevant image type each, so they
// get their own bounded batch helpers below rather than reusing the four-type
// list presigner.

// presignSeason presigns a single season poster. It delegates to the batched
// presignSeasons so genuinely-singular callers (GetSeason) share one code path;
// page callers must use presignSeasons directly.
func (s *directContentService) presignSeason(ctx context.Context, season *upstreamSeason) {
	if season == nil {
		return
	}
	seasons := []upstreamSeason{*season}
	s.presignSeasons(ctx, seasons)
	*season = seasons[0]
}

// presignSeasons presigns every season poster in a single batched
// PresignImageURLsWithExpiry call for the whole collection instead of one
// singular call per season.
func (s *directContentService) presignSeasons(ctx context.Context, seasons []upstreamSeason) {
	if s.detailSvc == nil || len(seasons) == 0 {
		return
	}
	posterURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(seasons, func(se upstreamSeason) string { return se.PosterURL }), "poster", compatCardImageSize)
	for i := range seasons {
		seasons[i].PosterURL = resolvedListImageURL(posterURLs, seasons[i].PosterURL)
	}
}

// presignEpisodes presigns every episode still in a single batched
// PresignImageURLsWithExpiry call for the whole collection instead of one
// singular call per episode.
func (s *directContentService) presignEpisodes(ctx context.Context, episodes []upstreamEpisode) {
	if s.detailSvc == nil || len(episodes) == 0 {
		return
	}
	stillURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectImagePaths(episodes, func(ep upstreamEpisode) string { return ep.StillURL }), "still", compatCardImageSize)
	for i := range episodes {
		episodes[i].StillURL = resolvedListImageURL(stillURLs, episodes[i].StillURL)
	}
}

// --- Model-to-upstream mapping helpers ---

func mediaItemToListItem(mi *models.MediaItem) upstreamListItem {
	item := upstreamListItem{
		ContentID:         mi.ContentID,
		Type:              mi.Type,
		Title:             mi.Title,
		SortTitle:         mi.SortTitle,
		Year:              mi.Year,
		Genres:            mi.Genres,
		ContentRating:     mi.ContentRating,
		Status:            mi.Status,
		RatingIMDB:        mi.RatingIMDB,
		RatingTMDB:        mi.RatingTMDB,
		Overview:          mi.Overview,
		Tagline:           mi.Tagline,
		PosterURL:         mi.PosterPath,
		BackdropURL:       mi.BackdropPath,
		LogoURL:           mi.LogoPath,
		PosterPath:        mi.PosterPath,
		PosterThumbhash:   mi.PosterThumbhash,
		BackdropPath:      mi.BackdropPath,
		BackdropThumbhash: mi.BackdropThumbhash,
		LogoPath:          mi.LogoPath,
		UpdatedAt:         mi.UpdatedAt,
		Runtime:           mi.Runtime,
		SeasonCount:       mi.SeasonCount,
		Studios:           mi.Studios,
		Countries:         mi.Countries,
	}
	if premiereDate := compatPremiereDateString(mi.ReleaseDate, mi.FirstAirDate); premiereDate != "" {
		item.AirDate = premiereDate
	}
	return item
}

func itemDetailToUpstream(d *catalog.ItemDetail) upstreamItemDetail {
	detail := upstreamItemDetail{
		ContentID:     d.ContentID,
		Type:          d.Type,
		Title:         d.Title,
		SortTitle:     d.SortTitle,
		OriginalTitle: d.OriginalTitle,
		Year:          d.Year,
		Overview:      d.Overview,
		Tagline:       d.Tagline,
		Runtime:       d.Runtime,
		ContentRating: d.ContentRating,
		Genres:        d.Genres,
		RatingIMDB:    d.RatingIMDB,
		RatingTMDB:    d.RatingTMDB,
		PosterURL:     d.PosterURL,
		BackdropURL:   d.BackdropURL,
		LogoURL:       d.LogoURL,
		Studios:       d.Studios,
		Countries:     d.Countries,
		SeasonCount:   d.SeasonCount,
		SeriesID:      d.SeriesID,
		SeriesTitle:   d.SeriesTitle,
		SeasonNumber:  d.SeasonNumber,
		EpisodeNumber: d.EpisodeNumber,
		EpisodeCount:  d.EpisodeCount,
		AirDate:       compatPremiereDatePtr(d.ReleaseDate, d.FirstAirDate, d.AirDate),
		IsSpecials:    d.IsSpecials,
		UserData:      d.SeasonUserData,
		Versions:      d.Versions,
		Cast:          d.Cast,
		Crew:          d.Crew,
		Videos:        d.Videos,
		Extras:        d.Extras,
	}
	if detail.Genres == nil {
		detail.Genres = []string{}
	}
	if detail.Studios == nil {
		detail.Studios = []string{}
	}
	if detail.Countries == nil {
		detail.Countries = []string{}
	}
	if detail.Versions == nil {
		detail.Versions = []catalog.FileVersion{}
	}
	if detail.Cast == nil {
		detail.Cast = []catalog.CastCredit{}
	}
	if detail.Crew == nil {
		detail.Crew = []catalog.CrewCredit{}
	}
	return detail
}

func compatPremiereDateString(dates ...*string) string {
	for _, date := range dates {
		if date == nil {
			continue
		}
		if normalized := strings.TrimSpace(*date); normalized != "" {
			return normalized
		}
	}
	return ""
}

func compatPremiereDatePtr(dates ...*string) *string {
	if date := compatPremiereDateString(dates...); date != "" {
		return &date
	}
	return nil
}

func modelSeasonToUpstream(s *models.Season, episodeCount int) upstreamSeason {
	us := upstreamSeason{
		ContentID:       s.ContentID,
		SeasonNumber:    s.SeasonNumber,
		Title:           s.Title,
		Overview:        s.Overview,
		EpisodeCount:    episodeCount,
		PosterURL:       s.PosterPath,
		PosterPath:      s.PosterPath,
		PosterThumbhash: s.PosterThumbhash,
		UpdatedAt:       s.UpdatedAt,
		IsSpecials:      s.SeasonNumber == 0,
	}
	if s.AirDate != nil {
		us.AirDate = s.AirDate.Format(time.DateOnly)
	}
	return us
}

// syntheticSeason creates an upstreamSeason from episode aggregation when no
// row exists in the seasons table for the given series/season number.
func syntheticSeason(seriesID string, seasonNumber, episodeCount int) upstreamSeason {
	title := fmt.Sprintf("Season %d", seasonNumber)
	if seasonNumber == 0 {
		title = "Specials"
	}
	return upstreamSeason{
		ContentID:    fmt.Sprintf("%s-S%02d", seriesID, seasonNumber),
		SeasonNumber: seasonNumber,
		Title:        title,
		EpisodeCount: episodeCount,
		IsSpecials:   seasonNumber == 0,
	}
}

func modelEpisodeToUpstream(ep *models.Episode, seriesID string) upstreamEpisode {
	ue := upstreamEpisode{
		ContentID:      ep.ContentID,
		SeasonNumber:   ep.SeasonNumber,
		EpisodeNumber:  ep.EpisodeNumber,
		Title:          ep.Title,
		Overview:       ep.Overview,
		Runtime:        ep.Runtime,
		StillURL:       ep.StillPath,
		StillPath:      ep.StillPath,
		StillThumbhash: ep.StillThumbhash,
		UpdatedAt:      ep.UpdatedAt,
		SeriesID:       seriesID,
		SeasonID:       ep.SeasonID,
	}
	if ep.AirDate != nil {
		ue.AirDate = ep.AirDate.Format(time.DateOnly)
	}
	return ue
}

// wrapCatalogError converts catalog-level errors to compat HTTPError.
func wrapCatalogError(err error) error {
	if err == nil {
		return nil
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "not found") || strings.Contains(errMsg, "not playable") {
		return &HTTPError{StatusCode: 404, Message: errMsg}
	}
	return err
}
