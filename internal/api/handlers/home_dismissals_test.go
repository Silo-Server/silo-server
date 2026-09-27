package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type dismissalTestStore struct {
	userstore.UserStore
	upserted []userstore.HomeItemDismissal
	deleted  []string
}

func (s *dismissalTestStore) UpsertHomeDismissal(_ context.Context, d userstore.HomeItemDismissal) error {
	s.upserted = append(s.upserted, d)
	return nil
}

func (s *dismissalTestStore) DeleteHomeDismissal(_ context.Context, _, surface, itemID string) error {
	s.deleted = append(s.deleted, surface+"/"+itemID)
	return nil
}

type dismissalTestProvider struct {
	userstore.UserStoreProvider
	store *dismissalTestStore
}

func (p dismissalTestProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

type fakeSeriesDrops struct {
	series     map[string]string // item -> series
	resolveErr error
	dropped    []string
	undropped  []string
}

func (f *fakeSeriesDrops) ResolveDropSeries(_ context.Context, itemID string) (string, bool, error) {
	if f.resolveErr != nil {
		return "", false, f.resolveErr
	}
	series, ok := f.series[itemID]
	return series, ok, nil
}

func (f *fakeSeriesDrops) Drop(_ context.Context, _ int, _, seriesID string) error {
	f.dropped = append(f.dropped, seriesID)
	return nil
}

func (f *fakeSeriesDrops) Undrop(_ context.Context, _ int, _, seriesID string) error {
	f.undropped = append(f.undropped, seriesID)
	return nil
}

type fakeDroppedDispatcher struct{ events []watchsync.LocalDroppedEvent }

func (f *fakeDroppedDispatcher) HandleLocalDroppedEvent(_ context.Context, event watchsync.LocalDroppedEvent) error {
	f.events = append(f.events, event)
	return nil
}

func newDismissalTestHandler() (*HomeDismissalHandler, *dismissalTestStore, *fakeSeriesDrops, *fakeDroppedDispatcher) {
	store := &dismissalTestStore{}
	drops := &fakeSeriesDrops{series: map[string]string{"episode-1": "series-1", "series-1": "series-1"}}
	dispatcher := &fakeDroppedDispatcher{}
	h := NewHomeDismissalHandler(dismissalTestProvider{store: store})
	h.SetSeriesDrops(drops, nil)
	h.SetLocalDroppedEventDispatcher(dispatcher)
	return h, store, drops, dispatcher
}

func TestDismissEpisodeDropsSeriesOnEitherSurface(t *testing.T) {
	for _, cmd := range []HomeDismissalCommand{
		{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceNextUp, ItemID: "episode-1", SeriesID: "series-1"},
		{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceContinueWatching, ItemID: "episode-1", ProgressUpdatedAt: "2026-01-02T03:04:05Z"},
	} {
		h, store, drops, dispatcher := newDismissalTestHandler()
		if err := h.DismissHomeItem(t.Context(), cmd); err != nil {
			t.Fatalf("%s: %v", cmd.Surface, err)
		}
		if !slices.Equal(drops.dropped, []string{"series-1"}) {
			t.Errorf("%s: dropped %v, want [series-1]", cmd.Surface, drops.dropped)
		}
		if len(store.upserted) != 0 {
			t.Errorf("%s: wrote per-item dismissal %+v", cmd.Surface, store.upserted)
		}
		if len(dispatcher.events) != 1 || !slices.Equal(dispatcher.events[0].SeriesIDs, []string{"series-1"}) || dispatcher.events[0].ProfileID != "p" {
			t.Errorf("%s: dispatched %+v", cmd.Surface, dispatcher.events)
		}
	}
}

func TestDismissMovieKeepsPerItemDismissal(t *testing.T) {
	h, store, drops, dispatcher := newDismissalTestHandler()
	err := h.DismissHomeItem(t.Context(), HomeDismissalCommand{
		UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceContinueWatching, ItemID: "movie-1", ProgressUpdatedAt: "2026-01-02T03:04:05Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.upserted) != 1 || store.upserted[0].MediaItemID != "movie-1" {
		t.Fatalf("upserted %+v, want movie-1", store.upserted)
	}
	if len(drops.dropped) != 0 || len(dispatcher.events) != 0 {
		t.Fatalf("movie dismissal dropped %v / dispatched %v", drops.dropped, dispatcher.events)
	}
}

func TestDismissStillValidatesSurfaceAnchor(t *testing.T) {
	h, _, drops, _ := newDismissalTestHandler()
	err := h.DismissHomeItem(t.Context(), HomeDismissalCommand{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceNextUp, ItemID: "episode-1"})
	if err == nil {
		t.Fatal("next_up dismissal without series_id succeeded")
	}
	if len(drops.dropped) != 0 {
		t.Fatalf("invalid dismissal dropped %v", drops.dropped)
	}
}

func TestUndismissEpisodeUndropsSeries(t *testing.T) {
	h, store, drops, dispatcher := newDismissalTestHandler()
	if err := h.UndismissHomeItem(t.Context(), 1, "p", userstore.HomeSurfaceNextUp, "episode-1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(drops.undropped, []string{"series-1"}) {
		t.Errorf("undropped %v, want [series-1]", drops.undropped)
	}
	if !slices.Equal(store.deleted, []string{"next_up/episode-1"}) {
		t.Errorf("deleted %v, want the legacy per-item row", store.deleted)
	}
	if len(dispatcher.events) != 1 {
		t.Errorf("dispatched %+v, want one event", dispatcher.events)
	}
}

func TestDismissResolveFailureIsInternalError(t *testing.T) {
	h, store, _, _ := newDismissalTestHandler()
	h.seriesDrops.(*fakeSeriesDrops).resolveErr = errors.New("db down")
	err := h.DismissHomeItem(t.Context(), HomeDismissalCommand{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceNextUp, ItemID: "episode-1", SeriesID: "series-1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(store.upserted) != 0 {
		t.Fatalf("fell back to a per-item dismissal: %+v", store.upserted)
	}
}

func TestDismissWithoutDropStoreKeepsPerItemBehavior(t *testing.T) {
	store := &dismissalTestStore{}
	h := NewHomeDismissalHandler(dismissalTestProvider{store: store})
	err := h.DismissHomeItem(t.Context(), HomeDismissalCommand{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceNextUp, ItemID: "episode-1", SeriesID: "series-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.upserted) != 1 {
		t.Fatalf("upserted %+v, want one per-item dismissal", store.upserted)
	}
}

func v1DismissalRequest(method, surface, itemID, body string) *http.Request {
	req := httptest.NewRequest(method, "/api/v1/home/dismissals/"+surface+"/"+itemID, strings.NewReader(body))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("surface", surface)
	routeCtx.URLParams.Add("item_id", itemID)
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx)
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 1})
	ctx = apimw.SetProfileID(ctx, "p")
	return req.WithContext(ctx)
}

func TestV1DismissalRoutesKeepPerCardDismissals(t *testing.T) {
	h, store, drops, dispatcher := newDismissalTestHandler()

	rec := httptest.NewRecorder()
	h.HandleUpsertDismissal(rec, v1DismissalRequest(http.MethodPut, userstore.HomeSurfaceNextUp, "episode-1", `{"series_id":"series-1"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("PUT status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.HandleDeleteDismissal(rec, v1DismissalRequest(http.MethodDelete, userstore.HomeSurfaceNextUp, "episode-1", ""))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d: %s", rec.Code, rec.Body.String())
	}

	if len(store.upserted) != 1 || store.upserted[0].MediaItemID != "episode-1" {
		t.Fatalf("upserted %+v, want a per-card dismissal of episode-1", store.upserted)
	}
	if len(drops.dropped)+len(drops.undropped) != 0 || len(dispatcher.events) != 0 {
		t.Fatalf("v1 dropped %v, undropped %v, dispatched %v; want none", drops.dropped, drops.undropped, dispatcher.events)
	}
}

type deniedSeriesAccess struct{ denied string }

func (a deniedSeriesAccess) EnsureAccessible(_ context.Context, contentID string, _ catalog.AccessFilter) error {
	if contentID == a.denied {
		return catalog.ErrItemNotFound
	}
	return nil
}

func TestDismissRefusesToDropAnInaccessibleSeries(t *testing.T) {
	h, store, drops, dispatcher := newDismissalTestHandler()
	h.SetSeriesDrops(drops, deniedSeriesAccess{denied: "series-1"})

	err := h.DismissHomeItem(t.Context(), HomeDismissalCommand{UserID: 1, ProfileID: "p", Surface: userstore.HomeSurfaceNextUp, ItemID: "episode-1", SeriesID: "series-1"})

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a not-found API error", err)
	}
	if len(drops.dropped) != 0 || len(dispatcher.events) != 0 || len(store.upserted) != 0 {
		t.Fatalf("dropped %v, dispatched %v, upserted %v; want nothing", drops.dropped, dispatcher.events, store.upserted)
	}
}
