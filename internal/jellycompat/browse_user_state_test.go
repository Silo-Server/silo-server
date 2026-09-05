package jellycompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestConfiguredBrowseStateSQLiteCountAndPage(t *testing.T) {
	store := newJellycompatUserStore(t)
	browse := &stubBrowseSource{}
	now := time.Now().UTC()
	for i := range 205 {
		id := fmt.Sprintf("movie-%03d", i)
		browse.items = append(browse.items, &models.MediaItem{ContentID: id, Type: "movie", Title: id})
		if i%50 == 0 {
			if err := store.AddFavorite(t.Context(), "p1", id); err != nil {
				t.Fatal(err)
			}
			if err := store.SetProgressAt(t.Context(), "p1", id, 10, 100, false, now); err != nil {
				t.Fatal(err)
			}
			if err := store.AddHistory(t.Context(), userstore.WatchHistoryEntry{ProfileID: "p1", MediaItemID: id, Completed: true, WatchedAt: now.Add(-time.Hour).Format(time.RFC3339)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc := &directContentService{browseRepo: browse, storeProvider: compatTestUserStoreProvider{store: store}}
	params := url.Values{"type": {"movie"}, "genres": {"Drama"}, "years": {"2024"}, "is_favorite": {"true"}, "is_played": {"true"}, "compose_state": {"true"}, "is_resumable": {"true"}, "offset": {"2"}, "limit": {"2"}}
	result, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, params)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 5 || !result.HasMore || len(result.Items) != 2 || result.Items[0].ContentID != "movie-100" || result.Items[1].ContentID != "movie-150" {
		t.Fatalf("filtered page: %+v", result)
	}
	if len(browse.calls) != 3 {
		t.Fatalf("catalog pages: %d", len(browse.calls))
	}
	for _, call := range browse.calls {
		f := call.filters
		if f.IsFavorite || f.IsPlayed != nil || f.IsResumable || !slices.Equal(f.Genres, []string{"Drama"}) || !slices.Equal(f.Years, []int{2024}) || f.Limit != compatBrowseChunkLimit {
			t.Fatalf("candidate filters: %+v", f)
		}
	}
	result, err = svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p2"}, params)
	if err != nil || result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("profile isolation: %+v %v", result, err)
	}
	params.Set("is_favorite", "false")
	params.Set("is_resumable", "false")
	params.Set("is_played", "false")
	result, err = svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, params)
	if err != nil || result.Total != 200 || result.Items[0].ContentID != "movie-003" {
		t.Fatalf("unplayed count/page: %+v %v", result, err)
	}
}

type configuredSeriesRepo struct {
	episodeListSource
	pages map[string][]string
}

func (r configuredSeriesRepo) ListAvailableIDsBySeriesPage(_ context.Context, id string, limit, offset int) ([]string, error) {
	return slicePage(r.pages[id], offset, limit), nil
}

func TestConfiguredSeriesPlayedUsesEpisodes(t *testing.T) {
	store := newJellycompatUserStore(t)
	now := time.Now().UTC()
	for _, id := range []string{"incomplete", "done-episode"} {
		if err := store.SetProgressAt(t.Context(), "p1", id, 100, 100, true, now); err != nil {
			t.Fatal(err)
		}
	}
	svc := &directContentService{episodeRepo: configuredSeriesRepo{pages: map[string][]string{"incomplete": {"unplayed-episode"}, "done": {"done-episode"}}}}
	items := []upstreamListItem{{ContentID: "incomplete", Type: "series"}, {ContentID: "done", Type: "series"}, {ContentID: "empty", Type: "series"}}
	matches, err := svc.matchConfiguredUserState(t.Context(), store, "p1", items, catalog.BrowseFilters{IsPlayed: new(true)})
	if err != nil || !slices.Equal(matches, []bool{false, true, false}) {
		t.Fatalf("series rollup: %v %v", matches, err)
	}
	matches, err = svc.matchConfiguredUserState(t.Context(), store, "p1", items, catalog.BrowseFilters{IsPlayed: new(false)})
	if err != nil || !slices.Equal(matches, []bool{true, false, true}) {
		t.Fatalf("unplayed series: %v %v", matches, err)
	}
}

type failedBrowseStateStore struct {
	userstore.UserStore
	failure string
}

func (s failedBrowseStateStore) ListFavoritesByMediaItems(ctx context.Context, profile string, ids []string) (map[string]bool, error) {
	if s.failure == "favorite" {
		return nil, errors.New("favorite unavailable")
	}
	return s.UserStore.ListFavoritesByMediaItems(ctx, profile, ids)
}
func (s failedBrowseStateStore) ListProgressByMediaItems(ctx context.Context, profile string, ids []string) (map[string]userstore.WatchProgress, error) {
	if s.failure == "progress" {
		return nil, errors.New("progress unavailable")
	}
	return s.UserStore.ListProgressByMediaItems(ctx, profile, ids)
}
func (s failedBrowseStateStore) ListCompletedHistoryItems(ctx context.Context, q userstore.CompletedHistoryItemQuery) ([]userstore.CompletedHistoryItem, error) {
	if s.failure == "history" {
		return nil, errors.New("history unavailable")
	}
	return s.UserStore.ListCompletedHistoryItems(ctx, q)
}
func TestConfiguredBrowseStatePropagatesErrors(t *testing.T) {
	for _, failure := range []string{"favorite", "progress", "history"} {
		t.Run(failure, func(t *testing.T) {
			store := failedBrowseStateStore{UserStore: newJellycompatUserStore(t), failure: failure}
			_, err := (&directContentService{}).matchConfiguredUserState(t.Context(), store, "p1", []upstreamListItem{{ContentID: "movie", Type: "movie"}}, catalog.BrowseFilters{IsFavorite: true, IsPlayed: new(true)})
			if err == nil {
				t.Fatal("state error hidden")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc := &directContentService{storeProvider: compatTestUserStoreProvider{store: newJellycompatUserStore(t)}}
	_, err := svc.browseConfiguredUserState(ctx, &Session{StreamAppUserID: 1, ProfileID: "p1"}, catalog.BrowseFilters{}, func(catalog.BrowseFilters) ([]upstreamListItem, bool, error) {
		t.Fatal("canceled scan reached catalog")
		return nil, false, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

type configuredParentContent struct{ *directContentService }

func (configuredParentContent) ListSeasons(context.Context, *Session, string, *int) ([]upstreamSeason, error) {
	return nil, nil
}

type configuredParentRepo struct {
	fakeSeasonEpisodeRepo
	calls []catalog.BrowseFilters
	all   []*models.Episode
	t     *testing.T
}

func (r *configuredParentRepo) BrowseEpisodes(_ context.Context, series, season string, number *int, start string, f catalog.BrowseFilters, _ catalog.AccessFilter) ([]*models.Episode, int, error) {
	r.t.Helper()
	if series != "series" || season != "season" || start != "start" || number == nil || *number != 2 || f.IsFavorite || f.IsPlayed != nil || f.IsResumable || !slices.Equal(f.Genres, []string{"Drama"}) || !slices.Equal(f.Years, []int{2024}) {
		r.t.Fatalf("parent constraints lost: %q %q %q %v %+v", series, season, start, number, f)
	}
	r.calls = append(r.calls, f)
	return slicePage(r.all, f.Offset, f.Limit), len(r.all), nil
}
func (r *configuredParentRepo) GetByIDs(_ context.Context, ids []string) ([]*models.Episode, error) {
	result := []*models.Episode{}
	for _, ep := range r.all {
		if slices.Contains(ids, ep.ContentID) {
			result = append(result, ep)
		}
	}
	return result, nil
}
func TestConfiguredParentEpisodesCountBeforePaging(t *testing.T) {
	store := newJellycompatUserStore(t)
	repo := &configuredParentRepo{t: t}
	for i := range 105 {
		id := fmt.Sprintf("ep-%03d", i)
		repo.all = append(repo.all, &models.Episode{ContentID: id, SeriesID: "series", SeasonID: "season", SeasonNumber: 2, EpisodeNumber: i + 1})
		if i == 1 || i == 101 || i == 104 {
			if err := store.AddFavorite(t.Context(), "p1", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	codec := NewResourceIDCodec()
	content := configuredParentContent{&directContentService{storeProvider: compatTestUserStoreProvider{store: store}}}
	h := &ItemsHandler{content: content, episodeRepo: repo, codec: codec, mapper: newMapper(codec, &config.Config{}), userData: &mockUserDataService{}, images: NewImageCache(time.Hour, time.Now)}
	query := itemsQuery{enableTotalRecordCount: true, isFavorite: true, isPlayed: new(false), genres: []string{"Drama"}, years: []int{2024}, seasonNumber: new(2), startIndex: 1, limit: 1, startItemID: codec.EncodeStringID(EncodedIDItem, "start")}
	req := httptest.NewRequest("GET", "/Items", nil)
	rec := httptest.NewRecorder()
	h.writeSeriesEpisodesResponse(rec, req, &Session{StreamAppUserID: 1, ProfileID: "p1"}, query, "series", "season", true)
	if rec.Code != 200 {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	var result queryResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.TotalRecordCount != 3 || result.StartIndex != 1 || len(result.Items) != 1 || result.Items[0].ID != codec.EncodeStringID(EncodedIDItem, "ep-101") {
		t.Fatalf("parent filtered page: %+v", result)
	}
	if len(repo.calls) != 2 || repo.calls[1].Offset != 100 {
		t.Fatalf("candidate pagination: %+v", repo.calls)
	}
}

func TestConfiguredRandomBrowseKeepsOneSnapshot(t *testing.T) {
	store := newJellycompatUserStore(t)
	browse := &stubBrowseSource{items: makeBrowseTestMediaItems(105)}
	svc := &directContentService{browseRepo: browse, storeProvider: compatTestUserStoreProvider{store: store}}
	_, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, url.Values{"is_favorite": {"true"}, "sort": {"random"}, "limit": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(browse.calls) != 2 {
		t.Fatalf("pages=%d", len(browse.calls))
	}
	first, second := browse.calls[0].filters.SnapshotAt, browse.calls[1].filters.SnapshotAt
	if first == nil || second == nil || !first.Equal(*second) {
		t.Fatalf("random scan reshuffled: %v %v", first, second)
	}
}
