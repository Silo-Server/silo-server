package jellycompat

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchstate"
)

// directUserDataService implements UserDataService using the user store directly.
type directUserDataService struct {
	storeProvider           userstore.UserStoreProvider
	itemRepo                *catalog.ItemRepository
	detailSvc               *catalog.DetailService
	watchState              *watchstate.Service
	resumeFilter            *catalog.ContinueWatchingProgressFilter
	profileStaler           profileStaler
	profileRefreshRequester profileRefreshRequester
}

func newDirectUserDataService(
	storeProvider userstore.UserStoreProvider,
	itemRepo *catalog.ItemRepository,
	episodeRepo *catalog.EpisodeRepository,
	providerIDRepo *catalog.ProviderIDRepository,
	detailSvc *catalog.DetailService,
	resumeFilter *catalog.ContinueWatchingProgressFilter,
	staler profileStaler,
	requester profileRefreshRequester,
	completionObserver watchstate.CompletionObserver,
) *directUserDataService {
	return &directUserDataService{
		storeProvider: storeProvider,
		itemRepo:      itemRepo,
		detailSvc:     detailSvc,
		watchState: watchstate.NewService(storeProvider).
			WithStableIdentityResolver(watchstate.NewStableIdentityResolver(itemRepo, episodeRepo, providerIDRepo)).
			WithCompletionObserver(completionObserver),
		resumeFilter:            resumeFilter,
		profileStaler:           staler,
		profileRefreshRequester: requester,
	}
}

func (s *directUserDataService) ListFavorites(ctx context.Context, session *Session, limit, offset int) ([]upstreamListItem, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	// ABS-surface favorites (audiobooks/podcasts) are filtered out below, so
	// the limit/offset window must apply to the *filtered* list — a raw
	// store-level window would shift or shrink the visible page. Over-fetch
	// the raw rows, filter, then window.
	scanLimit := min(max((limit+offset)*2, 200), 10000)
	favorites, err := store.ListFavorites(ctx, session.ProfileID, scanLimit, 0)
	if err != nil {
		return nil, fmt.Errorf("list favorites: %w", err)
	}

	contentIDs := make([]string, 0, len(favorites))
	for _, fav := range favorites {
		contentIDs = append(contentIDs, fav.MediaItemID)
	}

	if len(contentIDs) == 0 {
		return []upstreamListItem{}, nil
	}

	items, err := s.itemRepo.GetByIDs(ctx, contentIDs)
	if err != nil {
		return nil, fmt.Errorf("get favorite items: %w", err)
	}

	// Build a map for ordering by the original favorites list order
	itemMap := make(map[string]*upstreamListItem, len(items))
	for _, mi := range items {
		// Favorites are shared with the ABS surface; its media types are
		// never exposed here (they would 404 on detail/PlaybackInfo).
		if isCompatExcludedMediaType(mi.Type) {
			continue
		}
		li := mediaItemToListItem(mi)
		itemMap[mi.ContentID] = &li
	}

	ordered := make([]upstreamListItem, 0, len(contentIDs))
	for _, id := range contentIDs {
		if li, ok := itemMap[id]; ok {
			ordered = append(ordered, *li)
		}
	}

	// Presign artwork only for the page being returned, batching each image
	// type into one PresignImageURLsWithExpiry call for the whole page.
	result := slicePage(ordered, offset, limit)
	presignCompatListItems(ctx, s.detailSvc, result)
	if result == nil {
		result = []upstreamListItem{}
	}
	return result, nil
}

func (s *directUserDataService) ListFavoritesByMediaItems(ctx context.Context, session *Session, mediaItemIDs []string) (map[string]bool, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	result, err := store.ListFavoritesByMediaItems(ctx, session.ProfileID, mediaItemIDs)
	if err != nil {
		return nil, fmt.Errorf("list favorites by media items: %w", err)
	}
	if result == nil {
		return map[string]bool{}, nil
	}
	return result, nil
}

func (s *directUserDataService) IsFavorite(ctx context.Context, session *Session, contentID string) (bool, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return false, fmt.Errorf("open user store: %w", err)
	}

	favorite, err := store.IsFavorite(ctx, session.ProfileID, contentID)
	if err != nil {
		return false, fmt.Errorf("check favorite: %w", err)
	}
	return favorite, nil
}

func (s *directUserDataService) AddFavorite(ctx context.Context, session *Session, contentID string) error {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}
	if err := store.AddFavorite(ctx, session.ProfileID, contentID); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func (s *directUserDataService) RemoveFavorite(ctx context.Context, session *Session, contentID string) error {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}
	if err := store.RemoveFavorite(ctx, session.ProfileID, contentID); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func (s *directUserDataService) ListProgress(ctx context.Context, session *Session, status string, limit, offset int) ([]upstreamProgress, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	entries, err := store.ListProgress(ctx, session.ProfileID, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list progress: %w", err)
	}

	result := make([]upstreamProgress, 0, len(entries))
	for _, entry := range entries {
		result = append(result, toUpstreamProgress(entry))
	}
	return result, nil
}

func (s *directUserDataService) ListProgressFiltered(ctx context.Context, session *Session, status string, types []string, libraryID *int, limit, offset int) ([]upstreamProgress, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	entries, err := store.ListProgressFiltered(ctx, session.ProfileID, status, types, libraryID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list filtered progress: %w", err)
	}

	result := make([]upstreamProgress, 0, len(entries))
	for _, entry := range entries {
		result = append(result, toUpstreamProgress(entry))
	}
	return result, nil
}

// FilterResumeProgress applies the same hiding rules as the first-party
// Continue Watching fetcher: dismissed entries and episodes superseded by a
// later-completed episode in the same series.
func (s *directUserDataService) FilterResumeProgress(ctx context.Context, session *Session, entries []upstreamProgress) ([]upstreamProgress, error) {
	if len(entries) == 0 {
		return entries, nil
	}
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	progress := make([]userstore.WatchProgress, 0, len(entries))
	for _, entry := range entries {
		progress = append(progress, fromUpstreamProgress(entry))
	}

	// Dismissal lookup failures degrade to showing the entries, matching the
	// first-party fetcher.
	if dismissals, err := store.ListHomeDismissals(ctx, session.ProfileID, userstore.HomeSurfaceContinueWatching); err != nil {
		slog.Error("listing continue watching dismissals", "profile_id", session.ProfileID, "error", err)
	} else {
		progress = catalog.NewHomeDismissalIndex(dismissals).FilterProgress(progress)
	}

	superseded, err := s.resumeFilter.SupersededEpisodeProgressIDs(ctx, store, session.ProfileID, progress)
	if err != nil {
		return nil, fmt.Errorf("filter superseded progress: %w", err)
	}
	progress = catalog.FilterSupersededProgress(progress, superseded)

	result := make([]upstreamProgress, 0, len(progress))
	for _, entry := range progress {
		result = append(result, toUpstreamProgress(entry))
	}
	return result, nil
}

func (s *directUserDataService) ListProgressByMediaItems(ctx context.Context, session *Session, mediaItemIDs []string) (map[string]*upstreamProgress, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	mediaItemIDs = normalizeContentIDs(mediaItemIDs)
	progressMap, err := userstore.ListProgressWithCompletedHistory(ctx, store, session.ProfileID, mediaItemIDs)
	if err != nil {
		return nil, fmt.Errorf("list progress by media items: %w", err)
	}

	result := make(map[string]*upstreamProgress, len(progressMap))
	for contentID, progress := range progressMap {
		entry := toUpstreamProgress(progress)
		result[contentID] = &entry
	}
	return result, nil
}

func (s *directUserDataService) GetProgress(ctx context.Context, session *Session, contentID string) (*upstreamProgress, error) {
	store, err := s.storeProvider.ForUser(ctx, session.StreamAppUserID)
	if err != nil {
		return nil, fmt.Errorf("open user store: %w", err)
	}

	progress, err := userstore.GetProgressWithCompletedHistory(ctx, store, session.ProfileID, contentID)
	if err != nil {
		return nil, fmt.Errorf("get progress: %w", err)
	}
	if progress == nil {
		return nil, nil
	}

	entry := toUpstreamProgress(*progress)
	return &entry, nil
}

func (s *directUserDataService) MarkPlayed(ctx context.Context, session *Session, contentID string) error {
	if s.watchState == nil {
		return fmt.Errorf("watch state service is not configured")
	}
	if err := s.watchState.RecordJellycompatMarkPlayed(ctx, session.StreamAppUserID, session.ProfileID, contentID, time.Now().UTC()); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func (s *directUserDataService) MarkPlayedBatch(ctx context.Context, session *Session, contentIDs []string) error {
	if s.watchState == nil {
		return fmt.Errorf("watch state service is not configured")
	}
	if len(contentIDs) == 0 {
		return nil
	}
	if err := s.watchState.RecordJellycompatMarkPlayedBatch(ctx, session.StreamAppUserID, session.ProfileID, contentIDs, time.Now().UTC()); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func (s *directUserDataService) MarkUnplayed(ctx context.Context, session *Session, contentID string) error {
	if s.watchState == nil {
		return fmt.Errorf("watch state service is not configured")
	}
	if err := s.watchState.RecordJellycompatMarkUnplayed(ctx, session.StreamAppUserID, session.ProfileID, contentID); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func (s *directUserDataService) MarkUnplayedBatch(ctx context.Context, session *Session, contentIDs []string) error {
	if s.watchState == nil {
		return fmt.Errorf("watch state service is not configured")
	}
	if len(contentIDs) == 0 {
		return nil
	}
	if err := s.watchState.RecordJellycompatMarkUnplayedBatch(ctx, session.StreamAppUserID, session.ProfileID, contentIDs); err != nil {
		return err
	}
	triggerProfileRefresh(ctx, s.profileStaler, s.profileRefreshRequester, session.StreamAppUserID, session.ProfileID)
	return nil
}

func toUpstreamProgress(entry userstore.WatchProgress) upstreamProgress {
	return upstreamProgress{
		MediaItemID:     entry.MediaItemID,
		PositionSeconds: entry.PositionSeconds,
		DurationSeconds: entry.DurationSeconds,
		Completed:       entry.Completed,
		UpdatedAt:       entry.UpdatedAt,
	}
}

func fromUpstreamProgress(entry upstreamProgress) userstore.WatchProgress {
	return userstore.WatchProgress{
		MediaItemID:     entry.MediaItemID,
		PositionSeconds: entry.PositionSeconds,
		DurationSeconds: entry.DurationSeconds,
		Completed:       entry.Completed,
		UpdatedAt:       entry.UpdatedAt,
	}
}
