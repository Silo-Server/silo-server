package jellycompat

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
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
			if _, err := store.AddFavorite(t.Context(), "p1", id); err != nil {
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

// countingSeriesRepo counts the series whose episodes are read.
type countingSeriesRepo struct {
	episodeListSource
	reads *int
}

func (r countingSeriesRepo) ListAvailableIDsBySeriesPage(_ context.Context, id string, limit, offset int) ([]string, error) {
	*r.reads++
	return slicePage([]string{id + "-episode"}, offset, limit), nil
}

// ListBySeriesIDs serves the series rollups BrowseItems adds to the page.
func (countingSeriesRepo) ListBySeriesIDs(context.Context, []string) (map[string][]*models.Episode, error) {
	return map[string][]*models.Episode{}, nil
}

// A page without a total, such as Latest, checks series only until the page
// is filled: each series costs episode reads.
func TestConfiguredBrowseStateStopsCheckingSeriesOncePageIsFull(t *testing.T) {
	store := newJellycompatUserStore(t)
	browse := &stubBrowseSource{}
	for i := range 100 {
		id := fmt.Sprintf("series-%03d", i)
		browse.items = append(browse.items, &models.MediaItem{ContentID: id, Type: "series", Title: id})
	}
	reads := 0
	svc := &directContentService{browseRepo: browse, episodeRepo: countingSeriesRepo{reads: &reads}, storeProvider: compatTestUserStoreProvider{store: store}}
	params := url.Values{"type": {"series"}, "is_played": {"false"}, "compose_state": {"true"}, "include_total": {"false"}, "offset": {"0"}, "limit": {"5"}}
	result, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 5 || !result.HasMore {
		t.Fatalf("page: %+v", result)
	}
	// Five for the page and one to know there are more.
	if reads != 6 {
		t.Fatalf("series checked = %d, want 6", reads)
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
			_, err := (&directContentService{}).matchConfiguredUserState(t.Context(), store, "p1", []upstreamListItem{{ContentID: "movie", Type: "movie"}}, catalog.BrowseFilters{IsFavorite: true, IsPlayed: new(true)}, 1)
			if err == nil {
				t.Fatal("state error hidden")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	svc := &directContentService{storeProvider: compatTestUserStoreProvider{store: newJellycompatUserStore(t)}}
	_, err := svc.browseConfiguredUserState(ctx, &Session{StreamAppUserID: 1, ProfileID: "p1"}, catalog.BrowseFilters{}, true, func(catalog.BrowseFilters) ([]upstreamListItem, bool, error) {
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

func (r *configuredParentRepo) BrowseEpisodes(_ context.Context, series, season string, number *int, start string, f catalog.BrowseFilters, _ catalog.AccessFilter, includeTotal bool) ([]*models.Episode, int, error) {
	r.t.Helper()
	if series != "series" || season != "season" || start != "start" || number == nil || *number != 2 || f.IsFavorite || f.IsPlayed != nil || f.IsResumable || !slices.Equal(f.Genres, []string{"Drama"}) || !slices.Equal(f.Years, []int{2024}) {
		r.t.Fatalf("parent constraints lost: %q %q %q %v %+v", series, season, start, number, f)
	}
	if includeTotal || f.Limit != compatBrowseChunkLimit+1 {
		r.t.Fatalf("candidate page must use bounded lookahead without a count: includeTotal=%v limit=%d", includeTotal, f.Limit)
	}
	r.calls = append(r.calls, f)
	return slicePage(r.all, f.Offset, f.Limit), 0, nil
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
func TestConfiguredBrowseWithoutTotalStopsAfterLookahead(t *testing.T) {
	for _, wantPlayed := range []bool{true} {
		t.Run(fmt.Sprintf("played=%v", wantPlayed), func(t *testing.T) {
			store := newJellycompatUserStore(t)
			browse := &stubBrowseSource{}
			for i := range 305 {
				id := fmt.Sprintf("movie-%03d", i)
				browse.items = append(browse.items, &models.MediaItem{ContentID: id, Type: "movie", Title: id})
				match := i == 99 || i == 100 || i == 101 || i == 250
				if match == wantPlayed {
					if err := store.SetProgressAt(t.Context(), "p1", id, 100, 100, true, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				}
			}
			svc := &directContentService{browseRepo: browse, storeProvider: compatTestUserStoreProvider{store: store}}
			for _, tc := range []struct {
				total             bool
				offset, wantCalls int
				wantID            string
				more              bool
			}{
				{false, 1, 2, "movie-100", true},
				{true, 1, 4, "movie-100", true},
				{false, 3, 4, "movie-250", false},
				{false, 4, 4, "", false},
			} {
				t.Run(fmt.Sprintf("total=%v/offset=%d", tc.total, tc.offset), func(t *testing.T) {
					browse.calls = nil
					params := url.Values{"type": {"movie"}, "is_played": {fmt.Sprint(wantPlayed)}, "compose_state": {"true"}, "include_total": {fmt.Sprint(tc.total)}, "offset": {fmt.Sprint(tc.offset)}, "limit": {"1"}}
					result, err := svc.BrowseItems(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, params)
					if err != nil {
						t.Fatal(err)
					}
					wantTotal := 0
					if tc.total {
						wantTotal = 4
					}
					if result.Total != wantTotal || result.HasMore != tc.more || len(browse.calls) != tc.wantCalls {
						t.Fatalf("result=%+v catalog calls=%d", result, len(browse.calls))
					}
					if tc.wantID == "" {
						if len(result.Items) != 0 {
							t.Fatalf("past last page: %+v", result.Items)
						}
					} else if len(result.Items) != 1 || result.Items[0].ContentID != tc.wantID {
						t.Fatalf("page: %+v", result.Items)
					}
				})
			}
		})
	}
}

// TestConfiguredBrowseHugeLimitWithoutTotal pins that a Limit near math.MaxInt
// still returns the page from Offset. Offset+Limit used to wrap negative and
// end the scan at the first match, before the page.
func TestConfiguredBrowseHugeLimitWithoutTotal(t *testing.T) {
	store := newJellycompatUserStore(t)
	var items []upstreamListItem
	for _, id := range []string{"movie-1", "movie-2", "movie-3"} {
		items = append(items, upstreamListItem{ContentID: id, Type: "movie"})
		if _, err := store.AddFavorite(t.Context(), "p1", id); err != nil {
			t.Fatal(err)
		}
	}
	svc := &directContentService{storeProvider: compatTestUserStoreProvider{store: store}}
	filters := catalog.BrowseFilters{IsFavorite: true, Offset: 1, Limit: math.MaxInt}
	result, err := svc.browseConfiguredUserState(t.Context(), &Session{StreamAppUserID: 1, ProfileID: "p1"}, filters, false, func(catalog.BrowseFilters) ([]upstreamListItem, bool, error) {
		return items, false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].ContentID != "movie-2" || result.Items[1].ContentID != "movie-3" || result.HasMore {
		t.Fatalf("page: %+v", result)
	}
}

type configuredSeriesEnrichmentRepo struct {
	configuredSeriesRepo
	enrichedIDs []string
}

func (r *configuredSeriesEnrichmentRepo) ListBySeriesIDs(_ context.Context, ids []string) (map[string][]*models.Episode, error) {
	r.enrichedIDs = append(r.enrichedIDs, ids...)
	result := make(map[string][]*models.Episode, len(ids))
	for _, id := range ids {
		for _, ep := range r.pages[id] {
			result[id] = append(result[id], &models.Episode{ContentID: ep, SeriesID: id})
		}
	}
	return result, nil
}
