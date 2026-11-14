package handlers

import (
	"context"
	"reflect"
	"strconv"
	"testing"
	"time"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/Silo-Server/silo-server/internal/watchlist"
)

// TestItemDetailViewerStateBatchDB pins item detail's one-round-trip viewer
// state (a store beside the catalog) to the one-query-at-a-time path other
// stores take, and checks that a watchlist title the library now has is
// promoted before in_watchlist is reported, although the batch reads the
// watchlist first. Skips unless SILO_TEST_DATABASE_URL names a migrated
// database.
func TestItemDetailViewerStateBatchDB(t *testing.T) {
	f := newPagingIntegrationFixture(t)
	ctx := t.Context()
	provider := pgstore.NewPostgresProvider(f.pool)
	store, err := provider.ForUser(ctx, f.account)
	if err != nil {
		t.Fatal(err)
	}
	profile := f.ids[0] + "-viewer"
	if err := store.CreateProfile(ctx, userstore.Profile{ID: profile, Name: "Viewer"}); err != nil {
		t.Fatal(err)
	}

	movie, promoted, plain := f.ids[1], f.ids[2], f.ids[3]
	if err := store.SetProgressAt(ctx, profile, movie, 600, 7200, false, time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSeriesPlaybackPreference(ctx, userstore.SeriesPlaybackPreference{
		ProfileID: profile, SeriesID: movie, Resolution: "2160p", FileID: 7, EditionKey: "extended",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddFavorite(ctx, profile, movie); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkNotInterested(ctx, profile, movie); err != nil {
		t.Fatal(err)
	}
	ratings := catalog.NewRatingsRepo(f.pool)
	if err := ratings.Set(ctx, f.account, profile, movie, 4); err != nil {
		t.Fatal(err)
	}

	// The profile watchlisted a title before the library had it; the library
	// item now carries the title's TMDB ID.
	itemRepo := catalog.NewItemRepository(f.pool)
	titles := watchlist.NewTitles(f.pool, itemRepo, provider, nil, nil)
	tmdbID := int(time.Now().UnixNano()%1_000_000_000) + 1
	f.exec(t, `UPDATE media_items SET tmdb_id = $2 WHERE content_id = $1`, promoted, strconv.Itoa(tmdbID))
	if _, _, err := titles.AddSnapshot(ctx, watchlist.Viewer{UserID: f.account, ProfileID: profile},
		watchlist.Snapshot{MediaType: "movie", TMDBID: tmdbID, Title: "Watchlisted early"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM watchlist_titles WHERE media_type = 'movie' AND tmdb_id = $1`, tmdbID)
	})

	newHandler := func(stores userstore.UserStoreProvider) *CatalogResourceHandler {
		h := NewCatalogResourceHandler(&ItemsHandler{
			browseRepo: catalog.NewBrowseRepository(f.pool), itemRepo: itemRepo, ratingsRepo: ratings, storeProvider: stores,
		})
		h.SetWatchlistPromoter(titles)
		return h
	}
	batched := newHandler(provider)
	oneAtATime := newHandler(pagingIntegrationProvider{account: f.account, store: userStoreOnly{store}})
	reqCtx := apimw.SetProfileID(apimw.SetClaims(ctx, &auth.Claims{UserID: f.account}), profile)
	viewer := ItemViewer{ProfileID: profile, Access: catalog.AccessFilter{UserID: f.account, ProfileID: profile, AllowedLibraryIDs: []int{f.library}}}
	detailOf := func(h *CatalogResourceHandler, id string) *catalog.ItemDetail {
		detail := &catalog.ItemDetail{ContentID: id, Type: itemTypeMovie}
		h.enrichItemDetail(reqCtx, viewer, detail, true)
		return detail
	}

	if _, ok := batched.batchedViewerState(reqCtx, viewer, &catalog.ItemDetail{ContentID: plain, Type: itemTypeMovie}); !ok {
		t.Fatal("the Postgres store was not read in one batch")
	}
	if got := detailOf(batched, promoted); got.UserState == nil || !got.UserState.InWatchlist {
		t.Fatalf("promoted title user_state = %+v, want in_watchlist", got.UserState)
	}
	for _, id := range []string{movie, promoted, plain} {
		got, want := detailOf(batched, id), detailOf(oneAtATime, id)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n batched      %+v %+v %+v\n one at a time %+v %+v %+v", id,
				got.UserState, got.SeasonUserData, got.UserRating, want.UserState, want.SeasonUserData, want.UserRating)
		}
	}
	if got := detailOf(batched, movie); got.UserRating == nil || *got.UserRating != 4 || got.SeasonUserData == nil ||
		got.SeasonUserData.LastFileID == nil || *got.SeasonUserData.LastFileID != 7 || !got.UserState.NotInterested {
		t.Fatalf("movie viewer state = %+v %+v %+v, want the seeded state", got.UserState, got.SeasonUserData, got.UserRating)
	}
}

// userStoreOnly hides every optional capability of the store it wraps.
type userStoreOnly struct{ userstore.UserStore }
