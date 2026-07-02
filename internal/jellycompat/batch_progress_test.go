package jellycompat

import (
	"context"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
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

func (s *stubContentService) GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int) (map[string]*upstreamItemDetail, error) {
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

func TestHandleGetUserData_UsesBatchResolveCall(t *testing.T) {
	// HandleGetUserData should resolve favorite + progress in a single
	// resolveUserStateForContentIDs call (one DB round-trip pair),
	// NOT separate IsFavorite + GetProgress scalar calls.
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)

	mockUserData := &mockUserDataService{}
	stubContent := &stubContentService{detail: &upstreamItemDetail{
		ContentID: contentID,
		Type:      "movie",
		Title:     "Test Movie",
	}}

	h := &UserDataHandler{
		content:  stubContent,
		userData: mockUserData,
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
	}

	req := httptest.NewRequest("GET", "/UserItems/"+encodedID+"/UserData", nil)

	// Inject chi route param so handler can resolve "itemId".
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("itemId", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)

	// Inject an authenticated compat session.
	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	ctx = context.WithValue(ctx, compatSessionKey, session)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleGetUserData(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}

	if mockUserData.isFavoriteCalls != 0 {
		t.Errorf("expected zero IsFavorite calls; got %d", mockUserData.isFavoriteCalls)
	}
	if mockUserData.getProgressCalls != 0 {
		t.Errorf("expected zero GetProgress calls; got %d", mockUserData.getProgressCalls)
	}
	if mockUserData.listFavoritesByMediaItemsCalls != 1 {
		t.Errorf("expected one batch favorites call; got %d",
			mockUserData.listFavoritesByMediaItemsCalls)
	}
	if mockUserData.listProgressByMediaItemsCalls != 1 {
		t.Errorf("expected one batch progress call; got %d",
			mockUserData.listProgressByMediaItemsCalls)
	}
}

// TestHandleFavoriteMutation_UsesBatchResolveCall verifies that the favorite
// add/remove mutation handler also uses the batch resolver to fetch the
// updated user-data response, rather than scalar IsFavorite + GetProgress.
func TestHandleFavoriteMutation_UsesBatchResolveCall(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-1"
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)

	mockUserData := &mockUserDataService{}
	stubContent := &stubContentService{detail: &upstreamItemDetail{
		ContentID: contentID,
		Type:      "movie",
		Title:     "Test Movie",
	}}

	h := &UserDataHandler{
		content:  stubContent,
		userData: mockUserData,
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
	}

	req := httptest.NewRequest("POST", "/UserFavoriteItems/"+encodedID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("itemId", encodedID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)

	session := &Session{StreamAppUserID: 1, ProfileID: "profile-1"}
	ctx = context.WithValue(ctx, compatSessionKey, session)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleAddFavorite(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}

	if mockUserData.addFavoriteCalls != 1 {
		t.Errorf("expected one AddFavorite call; got %d", mockUserData.addFavoriteCalls)
	}
	if mockUserData.isFavoriteCalls != 0 {
		t.Errorf("expected zero IsFavorite calls; got %d", mockUserData.isFavoriteCalls)
	}
	if mockUserData.getProgressCalls != 0 {
		t.Errorf("expected zero GetProgress calls; got %d", mockUserData.getProgressCalls)
	}
	if mockUserData.listFavoritesByMediaItemsCalls != 1 {
		t.Errorf("expected one batch favorites call; got %d",
			mockUserData.listFavoritesByMediaItemsCalls)
	}
	if mockUserData.listProgressByMediaItemsCalls != 1 {
		t.Errorf("expected one batch progress call; got %d",
			mockUserData.listProgressByMediaItemsCalls)
	}
}
