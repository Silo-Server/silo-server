package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
)

// countingContentService is a ContentService double that returns a fixed
// episode and series detail and counts calls to GetItemDetail. Other methods
// panic so tests catch unexpected use.
type countingContentService struct {
	episodeDetail      *upstreamItemDetail
	seriesDetail       *upstreamItemDetail
	seasons            []upstreamSeason
	getItemDetailCalls int
	listSeasonsCalls   int
	listSeasonsSeries  string
}

func (s *countingContentService) GetItemDetail(_ context.Context, _ *Session, contentID string, _ *int) (*upstreamItemDetail, error) {
	s.getItemDetailCalls++
	if s.episodeDetail != nil && contentID == s.episodeDetail.ContentID {
		d := *s.episodeDetail
		return &d, nil
	}
	if s.seriesDetail != nil && contentID == s.seriesDetail.ContentID {
		d := *s.seriesDetail
		return &d, nil
	}
	// Fallback: synthesize a minimal detail to keep tests deterministic if a
	// content ID is requested that we did not pre-stage. Tests should still
	// observe getItemDetailCalls to detect unexpected lookups.
	return &upstreamItemDetail{ContentID: contentID}, nil
}

func (s *countingContentService) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	panic("unused")
}

func (s *countingContentService) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	panic("unused")
}

func (s *countingContentService) SearchItems(context.Context, *Session, string, []string, int, int, *int) (*upstreamBrowseResponse, error) {
	panic("unused")
}

func (s *countingContentService) ListSeasons(_ context.Context, _ *Session, seriesID string, _ *int) ([]upstreamSeason, error) {
	s.listSeasonsCalls++
	s.listSeasonsSeries = seriesID
	if s.seasons == nil {
		panic("unused")
	}
	out := make([]upstreamSeason, len(s.seasons))
	copy(out, s.seasons)
	return out, nil
}

func (s *countingContentService) GetSeason(context.Context, *Session, string, int, *int) (*upstreamSeason, error) {
	panic("unused")
}

func (s *countingContentService) ListEpisodes(context.Context, *Session, string, int, *int) ([]upstreamEpisode, error) {
	panic("unused")
}

func (s *countingContentService) ListEpisodesBySeasonID(context.Context, *Session, string, *int) ([]upstreamEpisode, error) {
	panic("unused")
}

func (s *countingContentService) ListItemFilters(context.Context, *Session, url.Values) (*upstreamItemFiltersResponse, error) {
	panic("unused")
}

func TestHandleItems_SeriesParentSeasonFilterReturnsPagedSeasons(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	encodedSeriesID := codec.EncodeStringID(EncodedIDItem, seriesContentID)
	contentSvc := &countingContentService{
		seasons: []upstreamSeason{
			{ContentID: "season-1", SeasonNumber: 1, Title: "Season 1", EpisodeCount: 10},
			{ContentID: "season-2", SeasonNumber: 2, Title: "Season 2", EpisodeCount: 8},
		},
	}

	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   NewImageCache(time.Hour, time.Now),
	}

	req := httptest.NewRequest("GET", "/Users/test/Items?ParentId="+encodedSeriesID+
		"&IncludeItemTypes=Season&Recursive=false&SortBy=IndexNumber&SortOrder=Ascending"+
		"&Fields=PrimaryImageAspectRatio,CanDelete&StartIndex=1&Limit=1", nil)
	req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{
		StreamAppUserID: 1,
		ProfileID:       "profile-1",
	}))

	rec := httptest.NewRecorder()
	h.HandleItems(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if contentSvc.listSeasonsCalls != 1 || contentSvc.listSeasonsSeries != seriesContentID {
		t.Fatalf("ListSeasons calls = %d for %q, want 1 for %q",
			contentSvc.listSeasonsCalls, contentSvc.listSeasonsSeries, seriesContentID)
	}

	var result queryResultDTO
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.TotalRecordCount != 2 || result.StartIndex != 1 {
		t.Fatalf("TotalRecordCount/StartIndex = %d/%d, want 2/1",
			result.TotalRecordCount, result.StartIndex)
	}
	if len(result.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(result.Items))
	}
	item := result.Items[0]
	if item.Type != "Season" || item.Name != "Season 2" {
		t.Fatalf("item = {%q %q}, want Season/Season 2", item.Type, item.Name)
	}
	if item.ParentID != encodedSeriesID || item.SeriesID != encodedSeriesID {
		t.Fatalf("ParentID/SeriesID = %q/%q, want %q/%q",
			item.ParentID, item.SeriesID, encodedSeriesID, encodedSeriesID)
	}
}

// TestHandleItem_Episode_FetchesSeriesDetailForStableParentImageTags verifies
// that episode detail responses fetch parent series image metadata even when
// image URLs are already cached. Cached URLs are not enough to build stable
// signed tags after Jellycompat restarts.
func TestHandleItem_Episode_FetchesSeriesDetailForStableParentImageTags(t *testing.T) {
	codec := NewResourceIDCodec()
	episodeContentID := "ep1"
	seriesContentID := "series-1"
	encodedEpisodeID := codec.EncodeStringID(EncodedIDItem, episodeContentID)
	encodedSeriesID := codec.EncodeStringID(EncodedIDItem, seriesContentID)

	contentSvc := &countingContentService{
		episodeDetail: &upstreamItemDetail{
			ContentID: episodeContentID,
			Type:      "episode",
			Title:     "Test Episode",
			SeriesID:  seriesContentID,
		},
		seriesDetail: &upstreamItemDetail{
			ContentID:   seriesContentID,
			Type:        "series",
			Title:       "Test Series",
			PosterURL:   "/img/series-1-poster.jpg",
			BackdropURL: "/img/series-1-backdrop.jpg",
		},
	}

	imageCache := NewImageCache(time.Hour, time.Now)
	// Pre-populate the cache as a list/browse response would have.
	imageCache.RememberSized(encodedSeriesID, "Primary", "/img/series-1-poster.jpg", compatCardImageSize)
	imageCache.RememberSized(encodedSeriesID, "Backdrop", "/img/series-1-backdrop.jpg", compatCardImageSize)

	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   imageCache,
	}

	req := httptest.NewRequest("GET", "/Items/"+encodedEpisodeID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedEpisodeID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleItem(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if contentSvc.getItemDetailCalls != 2 {
		t.Errorf("expected episode and series GetItemDetail calls for stable parent image tags; got %d",
			contentSvc.getItemDetailCalls)
	}
}

// TestHandleItem_Episode_FallsBackToSeriesDetailOnCacheMiss verifies that when
// the ImageCache has no entry for the parent series, the handler still falls
// back to fetching the series detail.
func TestHandleItem_Episode_FallsBackToSeriesDetailOnCacheMiss(t *testing.T) {
	codec := NewResourceIDCodec()
	episodeContentID := "ep1"
	seriesContentID := "series-1"
	encodedEpisodeID := codec.EncodeStringID(EncodedIDItem, episodeContentID)

	contentSvc := &countingContentService{
		episodeDetail: &upstreamItemDetail{
			ContentID: episodeContentID,
			Type:      "episode",
			Title:     "Test Episode",
			SeriesID:  seriesContentID,
		},
		seriesDetail: &upstreamItemDetail{
			ContentID:   seriesContentID,
			Type:        "series",
			Title:       "Test Series",
			PosterURL:   "/img/series-1-poster.jpg",
			BackdropURL: "/img/series-1-backdrop.jpg",
		},
	}

	// Empty cache — both Primary and Backdrop will miss.
	imageCache := NewImageCache(time.Hour, time.Now)

	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   imageCache,
	}

	req := httptest.NewRequest("GET", "/Items/"+encodedEpisodeID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedEpisodeID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleItem(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if contentSvc.getItemDetailCalls != 2 {
		t.Errorf("cache miss should fall back to series detail fetch; got %d GetItemDetail calls",
			contentSvc.getItemDetailCalls)
	}
}

// TestHandleItem_Episode_PartialCacheFallsBackToSeriesDetail pins the partial-
// hit fallback: when only one of Primary/Backdrop is cached, the handler must
// fall back to the series-detail fetch so the response carries both URLs
// rather than silently degrading with an empty backdrop tag.
func TestHandleItem_Episode_PartialCacheFallsBackToSeriesDetail(t *testing.T) {
	codec := NewResourceIDCodec()
	episodeContentID := "ep1"
	seriesContentID := "series-1"
	encodedEpisodeID := codec.EncodeStringID(EncodedIDItem, episodeContentID)
	encodedSeriesID := codec.EncodeStringID(EncodedIDItem, seriesContentID)

	contentSvc := &countingContentService{
		episodeDetail: &upstreamItemDetail{
			ContentID: episodeContentID,
			Type:      "episode",
			Title:     "Test Episode",
			SeriesID:  seriesContentID,
		},
		seriesDetail: &upstreamItemDetail{
			ContentID:   seriesContentID,
			Type:        "series",
			Title:       "Test Series",
			PosterURL:   "/img/series-1-poster.jpg",
			BackdropURL: "/img/series-1-backdrop.jpg",
		},
	}

	imageCache := NewImageCache(time.Hour, time.Now)
	// Only Primary cached; Backdrop missing — partial hit must fall back.
	imageCache.RememberSized(encodedSeriesID, "Primary", "/img/series-1-poster.jpg", compatCardImageSize)

	h := &ItemsHandler{
		content:  contentSvc,
		userData: &mockUserDataService{},
		codec:    codec,
		mapper:   newMapper(codec, &config.Config{}),
		images:   imageCache,
	}

	req := httptest.NewRequest("GET", "/Items/"+encodedEpisodeID, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", encodedEpisodeID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleItem(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if contentSvc.getItemDetailCalls != 2 {
		t.Errorf("partial cache hit (only Primary) must fall back to series detail; got %d GetItemDetail calls",
			contentSvc.getItemDetailCalls)
	}
}

// TestRememberDetailImages_SeedsMediumWithoutTouchingSmall asserts that detail
// poster/backdrop/logo land in the "medium" route bucket (matching catalog's
// w500/w1920 featured sizing for size="") and do NOT pollute the "small"
// bucket seeded by list paths.
func TestRememberDetailImages_SeedsMediumWithoutTouchingSmall(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "movie-42"
	encodedID := codec.EncodeStringID(EncodedIDItem, contentID)

	imageCache := NewImageCache(time.Hour, time.Now)
	listSmallURL := "https://image.tmdb.org/t/p/original/list-small.jpg"
	imageCache.RememberSized(encodedID, "Primary", listSmallURL, compatCardImageSize)

	h := &ItemsHandler{codec: codec, images: imageCache}
	detail := upstreamItemDetail{
		ContentID:   contentID,
		Type:        "movie",
		PosterURL:   "https://s3.example/poster/w500.jpg",
		BackdropURL: "https://s3.example/backdrop/w1920.jpg",
		LogoURL:     "https://s3.example/logo/w500.jpg",
	}

	h.rememberDetailImages(detail)

	if got, ok := imageCache.LookupSized(encodedID, "Primary", "", compatCardImageSize); !ok || got != listSmallURL {
		t.Fatalf("small Primary bucket = (%q, %v), want list-seeded URL untouched", got, ok)
	}
	if got, ok := imageCache.LookupSized(encodedID, "Primary", "", "medium"); !ok || got != detail.PosterURL {
		t.Fatalf("medium Primary bucket = (%q, %v), want %q", got, ok, detail.PosterURL)
	}
	if got, ok := imageCache.LookupSized(encodedID, "Backdrop", "", "medium"); !ok || got != detail.BackdropURL {
		t.Fatalf("medium Backdrop bucket = (%q, %v), want %q", got, ok, detail.BackdropURL)
	}
	if got, ok := imageCache.LookupSized(encodedID, "Logo", "", "medium"); !ok || got != detail.LogoURL {
		t.Fatalf("medium Logo bucket = (%q, %v), want %q", got, ok, detail.LogoURL)
	}
	if _, ok := imageCache.LookupSized(encodedID, "Backdrop", "", "original"); ok {
		t.Fatal("original Backdrop bucket must NOT be seeded by detail (would shadow real original asset)")
	}
}

// TestRememberDetailImages_SeasonRouteIDUsesEncodedIDSeason asserts that for
// detail.Type=="season" the route bucket is keyed under EncodedIDSeason so
// no-tag image lookups for seasons hit the cache.
func TestRememberDetailImages_SeasonRouteIDUsesEncodedIDSeason(t *testing.T) {
	codec := NewResourceIDCodec()
	contentID := "season-7"
	encodedItemID := codec.EncodeStringID(EncodedIDItem, contentID)
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, contentID)

	imageCache := NewImageCache(time.Hour, time.Now)
	h := &ItemsHandler{codec: codec, images: imageCache}

	detail := upstreamItemDetail{
		ContentID: contentID,
		Type:      "Season",
		PosterURL: "https://s3.example/season-poster/w500.jpg",
	}
	h.rememberDetailImages(detail)

	if got, ok := imageCache.LookupSized(encodedSeasonID, "Primary", "", "medium"); !ok || got != detail.PosterURL {
		t.Fatalf("season route lookup via EncodedIDSeason = (%q, %v), want %q", got, ok, detail.PosterURL)
	}
	// Item-encoded route is also seeded so callers that reach seasons through
	// the item route (e.g. some cross-referenced lookups) still resolve.
	if got, ok := imageCache.LookupSized(encodedItemID, "Primary", "", "medium"); !ok || got != detail.PosterURL {
		t.Fatalf("season route lookup via EncodedIDItem = (%q, %v), want %q", got, ok, detail.PosterURL)
	}
}

// TestHandleUpcoming_Unauthorized confirms the endpoint requires a session.
func TestHandleUpcoming_Unauthorized(t *testing.T) {
	h := &ItemsHandler{codec: NewResourceIDCodec()}
	req := httptest.NewRequest("GET", "/Shows/Upcoming", nil)
	rec := httptest.NewRecorder()
	h.HandleUpcoming(rec, req)
	if rec.Code != 401 {
		t.Fatalf("expected 401 without session; got %d, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleUpcoming_MissingSeriesId_ReturnsEmptyNot404 pins the central
// contract of this endpoint: when no SeriesId/ParentId is supplied we must
// return 200 with an empty result. Returning 404 here re-triggers the
// Android TV fallback to global /Shows/NextUp that this handler exists to
// suppress (see error-report-2026-05-08.md §11).
func TestHandleUpcoming_MissingSeriesId_ReturnsEmptyNot404(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, mapper: newMapper(codec, &config.Config{})}

	req := httptest.NewRequest("GET", "/Shows/Upcoming", nil)
	ctx := context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	rec := httptest.NewRecorder()
	h.HandleUpcoming(rec, req.WithContext(ctx))

	if rec.Code != 200 {
		t.Fatalf("expected 200 (not 404) when SeriesId missing; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Items":[]`) {
		t.Fatalf("expected empty Items array; got body=%s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"TotalRecordCount":0`) {
		t.Fatalf("expected TotalRecordCount 0; got body=%s", rec.Body.String())
	}
}

// TestHandleUpcoming_InvalidSeriesId_ReturnsEmptyNot404 — same contract for
// undecodable IDs. Decode failure must NOT 404 for the same reason.
func TestHandleUpcoming_InvalidSeriesId_ReturnsEmptyNot404(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, mapper: newMapper(codec, &config.Config{})}

	req := httptest.NewRequest("GET", "/Shows/Upcoming?SeriesId=not-a-valid-encoded-id", nil)
	ctx := context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 1, ProfileID: "profile-1"})
	rec := httptest.NewRecorder()
	h.HandleUpcoming(rec, req.WithContext(ctx))

	if rec.Code != 200 {
		t.Fatalf("expected 200 (not 404) for undecodable SeriesId; got %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"Items":[]`) {
		t.Fatalf("expected empty Items array; got body=%s", rec.Body.String())
	}
}
