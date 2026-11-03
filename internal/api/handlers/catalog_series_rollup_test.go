package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// rollupOnlyStore answers the SQL rollup and nothing else. Its embedded nil
// UserStore panics on any other call, as does a pool-less episode repository,
// so a fallback to the per-episode fold fails the test.
type rollupOnlyStore struct {
	userstore.UserStore
	series, seasons map[string]userstore.SeriesWatchCounts
	seasonNumbers   map[int]userstore.SeriesWatchCounts
}

func (s rollupOnlyStore) SeriesEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return s.series, nil
}

func (s rollupOnlyStore) SeasonEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return s.seasons, nil
}

func (s rollupOnlyStore) SeriesSeasonWatchCounts(context.Context, string, string) (map[int]userstore.SeriesWatchCounts, error) {
	return s.seasonNumbers, nil
}

// TestSeriesUserDataUsesEpisodeRollup pins series and season user_data to the
// store's SQL rollup when it has one: no episode listing, no per-episode
// progress reads. TestSeriesUserDataRollupParity proves the values match.
func TestSeriesUserDataUsesEpisodeRollup(t *testing.T) {
	store := rollupOnlyStore{
		series:  map[string]userstore.SeriesWatchCounts{"series-1": {TotalEpisodes: 5, WatchedCount: 2, InProgressCount: 1}},
		seasons: map[string]userstore.SeriesWatchCounts{"season-1": {TotalEpisodes: 3, WatchedCount: 3}},
		seasonNumbers: map[int]userstore.SeriesWatchCounts{
			0: {TotalEpisodes: 2, InProgressCount: 1},
			1: {TotalEpisodes: 3, WatchedCount: 3},
		},
	}
	h := &ItemsHandler{episodeRepo: catalog.NewEpisodeRepository(nil), storeProvider: testUserStoreProvider{store: store}}
	ctx := middleware.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user"})
	viewer := ItemViewer{ProfileID: "profile-1"}

	for _, tc := range []struct {
		itemType, id string
		want         *catalog.SeasonUserData
	}{
		{"series", "series-1", &catalog.SeasonUserData{WatchedCount: 2, UnplayedCount: 3, InProgressCount: 1}},
		{"season", "season-1", &catalog.SeasonUserData{WatchedCount: 3, Played: true}},
		// Like the fold, a parent without available episodes has no user_data.
		{"series", "series-2", nil},
		{"season", "season-2", nil},
	} {
		got, ok := h.parentRollupUserData(ctx, viewer, tc.itemType, tc.id)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s %s: user_data = %+v (ok %v), want %+v", tc.itemType, tc.id, got, ok, tc.want)
		}
	}

	episodeCounts, userData, err := h.seriesSeasonRollups(ctx, viewer, "series-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]int{0: 2, 1: 3}; !reflect.DeepEqual(episodeCounts, want) {
		t.Fatalf("episode counts = %v, want %v", episodeCounts, want)
	}
	wantUserData := map[int]*catalog.SeasonUserData{
		0: {UnplayedCount: 2, InProgressCount: 1},
		1: {WatchedCount: 3, Played: true},
	}
	if !reflect.DeepEqual(userData, wantUserData) {
		t.Fatalf("season user_data = %+v, want %+v", userData, wantUserData)
	}
}
