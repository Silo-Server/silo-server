package jellycompat

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
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

func (s *countingContentService) GetItemDetailsByIDs(ctx context.Context, session *Session, contentIDs []string, libraryID *int, _ detailBatchOptions) (map[string]*upstreamItemDetail, error) {
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

func (s *countingContentService) ListUserLibraries(context.Context, *Session) ([]upstreamUserLibrary, error) {
	panic("unused")
}

func (s *countingContentService) BrowseItems(context.Context, *Session, url.Values) (*upstreamBrowseResponse, error) {
	panic("unused")
}

func (s *countingContentService) SearchItems(context.Context, *Session, SearchItemsOptions) (*upstreamBrowseResponse, error) {
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

func (s *countingContentService) EnrichSeriesUserData(context.Context, *Session, []upstreamListItem) {
}

type recordingSearchContentService struct {
	countingContentService
	options []SearchItemsOptions
	result  *upstreamBrowseResponse
}

func (s *recordingSearchContentService) SearchItems(_ context.Context, _ *Session, opts SearchItemsOptions) (*upstreamBrowseResponse, error) {
	s.options = append(s.options, opts)
	if s.result != nil {
		return s.result, nil
	}
	return &upstreamBrowseResponse{Items: []upstreamListItem{}}, nil
}

func performEpisodesRequest(t *testing.T, h *ItemsHandler, target, seriesID string) queryResultDTO {
	t.Helper()
	req := httptest.NewRequest("GET", target, nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", seriesID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = context.WithValue(ctx, compatSessionKey, &Session{
		StreamAppUserID: 1,
		ProfileID:       "profile-1",
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleEpisodes(rec, req)
	if rec.Code != 200 {
		t.Fatalf("expected status 200; got %d, body=%s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return result
}

// TestHandleEpisodes_AuthoritativeSeasonID verifies that Swiftfin's call shape
// GET /Shows/{seasonID}/Episodes?seasonId={seasonID} (where the path {id} segment
// holds an EncodedIDSeason ID) treats seasonId as authoritative, resolves the owning
// series, and returns the season's episodes without a 404 series-decode failure.
func TestHandleEpisodes_AuthoritativeSeasonID(t *testing.T) {
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	seasonContentID := "season-1"
	encodedSeasonID := codec.EncodeStringID(EncodedIDSeason, seasonContentID)

	contentSvc := &countingContentService{
		seasons: []upstreamSeason{
			{ContentID: seasonContentID, SeasonNumber: 1, Title: "Season 1", EpisodeCount: 2},
		},
	}
	episodeRepo := &fakeSeasonEpisodeRepo{bySeason: map[string][]*models.Episode{
		episodeBySeasonKey(seriesContentID, 1): {
			{ContentID: "ep-1", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 1, Title: "E1"},
			{ContentID: "ep-2", SeriesID: seriesContentID, SeasonID: seasonContentID, SeasonNumber: 1, EpisodeNumber: 2, Title: "E2"},
		},
	}}
	h := &ItemsHandler{
		content:     contentSvc,
		userData:    &mockUserDataService{},
		codec:       codec,
		mapper:      newMapper(codec, &config.Config{}),
		images:      NewImageCache(time.Hour, time.Now),
		episodeRepo: episodeRepo,
		seasonRepo: &fakeSeasonByIDRepo{seasons: map[string]*models.Season{
			seasonContentID: {ContentID: seasonContentID, SeriesID: seriesContentID, SeasonNumber: 1},
		}},
	}

	result := performEpisodesRequest(t, h, "/Shows/"+encodedSeasonID+"/Episodes?SeasonId="+encodedSeasonID, encodedSeasonID)
	if result.TotalRecordCount != 2 || len(result.Items) != 2 {
		t.Fatalf("expected 2 episodes for Swiftfin seasonId query, got total=%d items=%+v", result.TotalRecordCount, result.Items)
	}
	if result.Items[0].Name != "E1" || result.Items[1].Name != "E2" {
		t.Fatalf("unexpected episode names: %+v", result.Items)
	}
}
