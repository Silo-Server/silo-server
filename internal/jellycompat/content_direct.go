package jellycompat

import (
	"context"
	"fmt"
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
	ListGenres(ctx context.Context, filters catalog.BrowseFilters) ([]string, error)
}

const (
	compatBrowseChunkLimit = 100
	compatBrowseMaxLimit   = 1000
)

// itemAccessSource is the subset of *catalog.ItemRepository that
// directContentService season/episode handlers rely on. Defined as an
// interface so tests can substitute a stub without standing up a Postgres
// pool.
type itemAccessSource interface {
	EnsureAccessible(ctx context.Context, contentID string, filter catalog.AccessFilter) error
	Search(ctx context.Context, query string, itemTypes []string, limit, offset int, filter catalog.AccessFilter) ([]*models.MediaItem, int, error)
	GetByIDs(ctx context.Context, contentIDs []string) ([]*models.MediaItem, error)
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
}

// directContentService implements ContentService by calling catalog repos directly.
type directContentService struct {
	browseRepo      browseSource
	itemRepo        itemAccessSource
	seasonRepo      seasonListSource
	episodeRepo     episodeListSource
	detailSvc       *catalog.DetailService
	folderRepo      *catalog.FolderRepository
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
	folderRepo *catalog.FolderRepository,
	storeProvider userstore.UserStoreProvider,
	accessFilter AccessFilterResolver,
) *directContentService {
	return &directContentService{
		browseRepo:    browseRepo,
		itemRepo:      itemRepo,
		seasonRepo:    seasonRepo,
		episodeRepo:   episodeRepo,
		detailSvc:     detailSvc,
		folderRepo:    folderRepo,
		storeProvider: storeProvider,
		accessFilter:  accessFilter,
	}
}

func (s *directContentService) resolveFilter(ctx context.Context, session *Session) catalog.AccessFilter {
	if s.accessFilter != nil {
		return s.accessFilter(ctx, session.StreamAppUserID, session.ProfileID)
	}
	return catalog.AccessFilter{}
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

	var folders []*models.MediaFolder
	var err error
	if len(filter.AllowedLibraryIDs) > 0 {
		folders, err = s.folderRepo.ListByIDs(ctx, filter.AllowedLibraryIDs)
	} else {
		folders, err = s.folderRepo.GetEnabled(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}

	libraries := make([]upstreamUserLibrary, 0, len(folders))
	for _, f := range folders {
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

func (s *directContentService) BrowseItems(ctx context.Context, session *Session, params url.Values) (*upstreamBrowseResponse, error) {
	filter := s.resolveFilter(ctx, session)
	isPlayedFilter := params.Get("is_played") // "", "true", or "false"
	contentIDs := parseContentIDParam(params.Get("content_ids"))
	includeTotal := parseBool(params.Get("include_total"), true)

	requestedLimit := catalog.ParseIntParam(params.Get("limit"))
	if requestedLimit <= 0 {
		requestedLimit = 24
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
		Type:               params.Get("type"),
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
		result, err := s.browseRepo.BrowsePage(ctx, filters, wantTotal)
		if err != nil {
			return nil, fmt.Errorf("browse items: %w", err)
		}
		if wantTotal {
			totalFromCatalog = result.Total
			totalKnown = true
		}
		hasMore = result.HasMore

		batch := make([]upstreamListItem, 0, len(result.Items))
		for _, mi := range result.Items {
			if s.detailSvc != nil {
				if localized, locErr := s.detailSvc.LocalizeItemModel(ctx, mi, filter); locErr == nil && localized != nil {
					mi = localized
				}
			}
			batch = append(batch, mediaItemToListItem(mi))
		}
		s.presignListItems(ctx, batch)
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

func (s *directContentService) SearchItems(ctx context.Context, session *Session, query string, itemTypes []string, limit, offset int, libraryID *int) (*upstreamBrowseResponse, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)

	items, total, err := s.itemRepo.Search(ctx, query, itemTypes, limit, offset, filter)
	if err != nil {
		return nil, fmt.Errorf("search items: %w", err)
	}

	listItems := make([]upstreamListItem, 0, len(items))
	for _, mi := range items {
		if s.detailSvc != nil {
			if localized, locErr := s.detailSvc.LocalizeItemModel(ctx, mi, filter); locErr == nil && localized != nil {
				mi = localized
			}
		}
		listItems = append(listItems, mediaItemToListItem(mi))
	}
	s.presignListItems(ctx, listItems)
	s.enrichListItemsUserData(ctx, session, listItems)

	return &upstreamBrowseResponse{
		Total:   total,
		HasMore: offset+len(listItems) < total,
		Items:   listItems,
	}, nil
}

func (s *directContentService) GetItemDetail(ctx context.Context, session *Session, contentID string, libraryID *int) (*upstreamItemDetail, error) {
	filter := applyCompatPresentationLibrary(s.resolveFilter(ctx, session), libraryID)

	detail, err := s.detailSvc.GetItemDetail(ctx, contentID, filter)
	if err != nil {
		return nil, wrapCatalogError(err)
	}

	result := itemDetailToUpstream(detail)

	// Enrich with user data
	if s.storeProvider != nil {
		store, storeErr := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
		if storeErr == nil {
			progress, _ := store.GetProgress(ctx, session.ProfileID, contentID)
			if progress != nil {
				result.UserData = &catalog.SeasonUserData{
					PositionSeconds: progress.PositionSeconds,
					DurationSeconds: progress.DurationSeconds,
					Played:          progress.Completed,
					IsInProgress:    progress.PositionSeconds > 0 && !progress.Completed,
				}
			}
		}
	}

	return &result, nil
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
			s.presignSeason(ctx, &us)
			applySeasonUserData(&us, eps, progressMap)
			result = append(result, us)
		}
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
		if progressEntries, progressErr := store.ListProgressByMediaItems(ctx, session.ProfileID, episodeIDs); progressErr == nil {
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
		s.presignEpisode(ctx, &ue)
		if progress, ok := progressMap[ep.ContentID]; ok {
			ue.UserData = seasonUserDataFromProgress(progress)
		}
		result = append(result, ue)
	}
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
		Type:               params.Get("type"),
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
	progressMap, err := store.ListProgressByMediaItems(ctx, session.ProfileID, contentIDs)
	if err != nil {
		return
	}

	for i := range items {
		progress, ok := progressMap[items[i].ContentID]
		if ok {
			items[i].UserData = seasonUserDataFromProgress(progress)
		}
	}
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
	progressMap, err := store.ListProgressByMediaItems(ctx, session.ProfileID, episodeIDs)
	if err != nil {
		return
	}
	applySeasonUserData(season, episodes, progressMap)
}

// applySeasonUserData computes WatchedCount/UnplayedCount/Played for a season
// using a pre-fetched progressMap. Pure function — no I/O.
func applySeasonUserData(season *upstreamSeason, episodes []*models.Episode, progressMap map[string]userstore.WatchProgress) {
	watched := 0
	unplayed := 0
	for _, ep := range episodes {
		if ep == nil {
			continue
		}
		progress, ok := progressMap[ep.ContentID]
		if ok && progress.Completed {
			watched++
		} else {
			unplayed++
		}
	}
	season.UserData = &catalog.SeasonUserData{
		WatchedCount:  watched,
		UnplayedCount: unplayed,
		Played:        unplayed == 0 && len(episodes) > 0,
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
	progressMap, err := store.ListProgressByMediaItems(ctx, session.ProfileID, episodeIDs)
	if err != nil || progressMap == nil {
		return map[string]userstore.WatchProgress{}
	}
	return progressMap
}

// enrichEpisodeUserData adds user data for a single episode.
func (s *directContentService) enrichEpisodeUserData(ctx context.Context, session *Session, ep *upstreamEpisode) {
	if progressMap, err := s.progressMapForContentIDs(ctx, session, []string{ep.ContentID}); err == nil {
		if progress, ok := progressMap[ep.ContentID]; ok {
			ep.UserData = seasonUserDataFromProgress(progress)
		}
	}
}

func (s *directContentService) userStore(ctx context.Context, session *Session) (userstore.UserStore, error) {
	if s.storeProvider == nil {
		return nil, fmt.Errorf("user store is not configured")
	}
	return s.storeProvider.ForUser(ctx, session.StreamAppUserID)
}

func (s *directContentService) progressMapForContentIDs(ctx context.Context, session *Session, contentIDs []string) (map[string]userstore.WatchProgress, error) {
	store, err := s.userStore(ctx, session)
	if err != nil {
		return nil, err
	}
	return store.ListProgressByMediaItems(ctx, session.ProfileID, contentIDs)
}

func seasonUserDataFromProgress(progress userstore.WatchProgress) *catalog.SeasonUserData {
	return &catalog.SeasonUserData{
		PositionSeconds: progress.PositionSeconds,
		DurationSeconds: progress.DurationSeconds,
		Played:          progress.Completed,
		IsInProgress:    progress.PositionSeconds > 0 && !progress.Completed,
	}
}

// --- Image URL presigning helpers ---

func (s *directContentService) presignListItem(ctx context.Context, item *upstreamListItem) {
	if item == nil {
		return
	}
	items := []upstreamListItem{*item}
	s.presignListItems(ctx, items)
	*item = items[0]
}

func (s *directContentService) presignListItems(ctx context.Context, items []upstreamListItem) {
	if len(items) == 0 {
		return
	}
	for i := range items {
		ensureListItemImagePaths(&items[i])
	}
	if s.detailSvc == nil {
		return
	}

	posterURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectListImagePaths(items, func(item upstreamListItem) string { return item.PosterURL }), "poster", compatCardImageSize)
	backdropURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectListImagePaths(items, func(item upstreamListItem) string { return item.BackdropURL }), "backdrop", compatCardImageSize)
	logoURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectListImagePaths(items, func(item upstreamListItem) string { return item.LogoURL }), "logo", compatCardImageSize)
	stillURLs := s.detailSvc.PresignImageURLsWithExpiry(ctx, collectListImagePaths(items, func(item upstreamListItem) string { return item.StillURL }), "still", compatCardImageSize)

	for i := range items {
		items[i].PosterURL = resolvedListImageURL(posterURLs, items[i].PosterURL)
		items[i].BackdropURL = resolvedListImageURL(backdropURLs, items[i].BackdropURL)
		items[i].LogoURL = resolvedListImageURL(logoURLs, items[i].LogoURL)
		items[i].StillURL = resolvedListImageURL(stillURLs, items[i].StillURL)
	}
}

func ensureListItemImagePaths(item *upstreamListItem) {
	if item.PosterPath == "" {
		item.PosterPath = item.PosterURL
	}
	if item.BackdropPath == "" {
		item.BackdropPath = item.BackdropURL
	}
	if item.LogoPath == "" {
		item.LogoPath = item.LogoURL
	}
	if item.StillPath == "" {
		item.StillPath = item.StillURL
	}
}

func collectListImagePaths(items []upstreamListItem, pick func(upstreamListItem) string) []string {
	paths := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		path := pick(item)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func resolvedListImageURL(resolved map[string]catalog.ResolvedImageURL, path string) string {
	if path == "" {
		return ""
	}
	if value, ok := resolved[path]; ok {
		return value.URL
	}
	return ""
}

func (s *directContentService) presignSeason(ctx context.Context, season *upstreamSeason) {
	season.PosterURL = compatPresignImage(s.detailSvc, ctx, season.PosterURL, "poster", compatCardImageSize)
}

func (s *directContentService) presignEpisode(ctx context.Context, ep *upstreamEpisode) {
	ep.StillURL = compatPresignImage(s.detailSvc, ctx, ep.StillURL, "still", compatCardImageSize)
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
