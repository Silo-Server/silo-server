package jellycompat

import (
	"context"
	"net/url"
)

// mockUserDataService is a counting fake that satisfies UserDataService.
// It records calls to the per-item scalar accessors (IsFavorite, GetProgress)
// and the batch accessors (ListFavoritesByMediaItems, ListProgressByMediaItems)
// so tests can assert that handlers prefer the batch resolver over the
// scalar API. All other methods panic on unexpected use.
type mockUserDataService struct {
	isFavoriteCalls                int
	getProgressCalls               int
	listFavoritesByMediaItemsCalls int
	listProgressByMediaItemsCalls  int
	addFavoriteCalls               int
	removeFavoriteCalls            int
}

func (m *mockUserDataService) ListFavoritesByMediaItems(_ context.Context, _ *Session, mediaItemIDs []string) (map[string]bool, error) {
	m.listFavoritesByMediaItemsCalls++
	out := make(map[string]bool, len(mediaItemIDs))
	for _, id := range mediaItemIDs {
		out[id] = false
	}
	return out, nil
}

func (m *mockUserDataService) ListProgressByMediaItems(_ context.Context, _ *Session, mediaItemIDs []string) (map[string]*upstreamProgress, error) {
	m.listProgressByMediaItemsCalls++
	return map[string]*upstreamProgress{}, nil
}

func (m *mockUserDataService) IsFavorite(_ context.Context, _ *Session, _ string) (bool, error) {
	m.isFavoriteCalls++
	return false, nil
}

func (m *mockUserDataService) GetProgress(_ context.Context, _ *Session, _ string) (*upstreamProgress, error) {
	m.getProgressCalls++
	return nil, nil
}

func (m *mockUserDataService) AddFavorite(_ context.Context, _ *Session, _ string) error {
	m.addFavoriteCalls++
	return nil
}

func (m *mockUserDataService) RemoveFavorite(_ context.Context, _ *Session, _ string) error {
	m.removeFavoriteCalls++
	return nil
}

// Remaining UserDataService methods panic — the test should not exercise them.
func (m *mockUserDataService) ListFavorites(context.Context, *Session, int, int) ([]upstreamListItem, error) {
	panic("unused")
}

func (m *mockUserDataService) ListProgress(context.Context, *Session, string, int, int) ([]upstreamProgress, error) {
	panic("unused")
}

func (m *mockUserDataService) ListProgressFiltered(context.Context, *Session, string, []string, *int, int, int) ([]upstreamProgress, error) {
	panic("unused")
}

func (m *mockUserDataService) ListResumableProgress(context.Context, *Session, []string) ([]upstreamProgress, error) {
	return nil, nil
}

func (m *mockUserDataService) FilterResumeProgress(_ context.Context, _ *Session, entries []upstreamProgress) ([]upstreamProgress, error) {
	return entries, nil
}

func (m *mockUserDataService) MarkPlayed(context.Context, *Session, string) error {
	panic("unused")
}

func (m *mockUserDataService) MarkPlayedBatch(context.Context, *Session, []string) error {
	panic("unused")
}

func (m *mockUserDataService) MarkUnplayed(context.Context, *Session, string) error {
	panic("unused")
}

func (m *mockUserDataService) MarkUnplayedBatch(context.Context, *Session, []string) error {
	panic("unused")
}

// stubContentService is a minimal ContentService that returns a fixed
// upstreamItemDetail from GetItemDetail. Other methods panic.
type stubContentService struct {
	detail *upstreamItemDetail
}

func (s *stubContentService) GetItemDetail(_ context.Context, _ *Session, contentID string, _ *int) (*upstreamItemDetail, error) {
	d := *s.detail
	if d.ContentID == "" {
		d.ContentID = contentID
	}
	return &d, nil
}

func (s *stubContentService) GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int, _ detailBatchOptions) (map[string]*upstreamItemDetail, error) {
	out := make(map[string]*upstreamItemDetail, len(contentIDs))
	for _, id := range contentIDs {
		detail, err := s.GetItemDetail(ctx, session, id, libraryID)
		if err != nil || detail == nil {
			continue
		}
		out[id] = detail
	}
	return out, nil
}

func (s *stubContentService) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	panic("unused")
}

func (s *stubContentService) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	panic("unused")
}

func (s *stubContentService) SearchItems(context.Context, *Session, SearchItemsOptions) (*upstreamBrowseResponse, error) {
	panic("unused")
}

func (s *stubContentService) ListSeasons(context.Context, *Session, string, *int) ([]upstreamSeason, error) {
	panic("unused")
}

func (s *stubContentService) GetSeason(context.Context, *Session, string, int, *int) (*upstreamSeason, error) {
	panic("unused")
}

func (s *stubContentService) ListEpisodes(context.Context, *Session, string, int, *int) ([]upstreamEpisode, error) {
	panic("unused")
}

func (s *stubContentService) ListEpisodesBySeasonID(context.Context, *Session, string, *int) ([]upstreamEpisode, error) {
	panic("unused")
}

func (s *stubContentService) ListItemFilters(context.Context, *Session, url.Values) (*upstreamItemFiltersResponse, error) {
	panic("unused")
}

func (s *stubContentService) EnrichSeriesUserData(context.Context, *Session, []upstreamListItem) {}
