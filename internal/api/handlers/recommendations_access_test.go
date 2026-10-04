package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// fakeRecsEngine answers a fixed list and records what it was asked.
type fakeRecsEngine struct {
	items        []recommendations.ScoredItem
	similarCalls int
	becauseCalls int
	becauseWith  catalog.AccessFilter
}

func (f *fakeRecsEngine) SimilarItems(context.Context, string, int) ([]recommendations.ScoredItem, error) {
	f.similarCalls++
	return f.items, nil
}

func (f *fakeRecsEngine) BecauseYouWatched(_ context.Context, _ int, _, _ string, _ int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	f.becauseCalls++
	f.becauseWith = filter
	return f.items, nil
}

func (f *fakeRecsEngine) GetTasteProfileSummary(context.Context, int, string) (*recommendations.TasteProfileSummary, error) {
	return nil, nil
}

// fakeAccessibleIDs admits every item outside hidden.
type fakeAccessibleIDs struct {
	hidden map[string]struct{}
	err    error
}

func (f fakeAccessibleIDs) FilterAccessibleItemIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]struct{}, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, hidden := f.hidden[id]; !hidden {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

func scored(ids ...string) []recommendations.ScoredItem {
	items := make([]recommendations.ScoredItem, len(ids))
	for i, id := range ids {
		items[i] = recommendations.ScoredItem{MediaItemID: id, Score: float64(len(ids) - i)}
	}
	return items
}

func scoredIDs(items []recommendations.ScoredItem) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.MediaItemID
	}
	return ids
}

// restrictedRecsRequest is a v1 request for the anchored list, made by a
// profile whose scope allows library 4 only.
func restrictedRecsRequest(path, itemID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("item_id", itemID)
	ctx := authenticatedRecsContext(req.Context(), rctx)
	ctx = access.SetScope(ctx, access.Scope{UserID: 7, ProfileID: "profile-1", AllowedLibraryIDs: []int{4}, LibrariesRestricted: true})
	return req.WithContext(ctx)
}

func decodeScoredItems(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp scoredItemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Items == nil {
		t.Fatal("items must be an array, got null")
	}
	return scoredIDs(resp.Items)
}

// The v1 Similar and Because You Watched lists answer only identifiers the
// viewer can see, in the engine's order.
func TestV1AnchoredRecommendationsDropInaccessibleItems(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		handle func(*RecommendationsHandler, http.ResponseWriter, *http.Request)
	}{
		{"similar", "/recommendations/similar/anchor", (*RecommendationsHandler).HandleSimilar},
		{"because watched", "/recommendations/because-watched/anchor", (*RecommendationsHandler).HandleBecauseWatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeRecsEngine{items: scored("a", "adult-1", "b", "adult-2", "c")}
			h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
			h.accessibleIDs = fakeAccessibleIDs{hidden: map[string]struct{}{"adult-1": {}, "adult-2": {}}}

			rec := httptest.NewRecorder()
			tc.handle(h, rec, restrictedRecsRequest(tc.path, "anchor"))
			if got := decodeScoredItems(t, rec); !slices.Equal(got, []string{"a", "b", "c"}) {
				t.Fatalf("items = %v, want [a b c]", got)
			}
		})
	}
}

// Because You Watched ranks under the viewer's scope, not only filters after.
func TestBecauseWatchedRanksUnderTheViewerScope(t *testing.T) {
	engine := &fakeRecsEngine{items: scored("a")}
	h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
	h.accessibleIDs = fakeAccessibleIDs{}

	rec := httptest.NewRecorder()
	h.HandleBecauseWatched(rec, restrictedRecsRequest("/recommendations/because-watched/anchor", "anchor"))
	decodeScoredItems(t, rec)
	if !slices.Equal(engine.becauseWith.AllowedLibraryIDs, []int{4}) || engine.becauseWith.UserID != 7 || engine.becauseWith.ProfileID != "profile-1" {
		t.Fatalf("engine filter = %+v, want the request scope", engine.becauseWith)
	}
}

// An anchor the viewer cannot see answers what an unknown anchor answers: an
// empty list, without running the engine, on v1 and on the v2 seams.
func TestInaccessibleAnchorAnswersLikeAnUnknownOne(t *testing.T) {
	engine := &fakeRecsEngine{items: scored("a", "b")}
	h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
	h.accessibleIDs = fakeAccessibleIDs{hidden: map[string]struct{}{"hidden-anchor": {}}}

	for _, tc := range []struct {
		name   string
		path   string
		handle func(*RecommendationsHandler, http.ResponseWriter, *http.Request)
	}{
		{"v1 similar", "/recommendations/similar/hidden-anchor", (*RecommendationsHandler).HandleSimilar},
		{"v1 because watched", "/recommendations/because-watched/hidden-anchor", (*RecommendationsHandler).HandleBecauseWatched},
	} {
		rec := httptest.NewRecorder()
		tc.handle(h, rec, restrictedRecsRequest(tc.path, "hidden-anchor"))
		if got := decodeScoredItems(t, rec); len(got) != 0 {
			t.Fatalf("%s: items = %v, want none", tc.name, got)
		}
	}

	filter := catalog.AccessFilter{UserID: 7, ProfileID: "profile-1"}
	h.Fetcher = stubDiscoverFetcher{}
	similar, err := h.SimilarCards(t.Context(), 7, "profile-1", "hidden-anchor", 10, filter)
	if err != nil || len(similar) != 0 {
		t.Fatalf("v2 similar = %v, %v; want none", similar, err)
	}
	because, err := h.BecauseWatchedCards(t.Context(), 7, "profile-1", "hidden-anchor", 10, filter)
	if err != nil || len(because) != 0 {
		t.Fatalf("v2 because watched = %v, %v; want none", because, err)
	}
	if engine.similarCalls != 0 || engine.becauseCalls != 0 {
		t.Fatalf("engine ran %d similar and %d because-watched reads for a hidden anchor", engine.similarCalls, engine.becauseCalls)
	}
}

func TestKeepAccessible(t *testing.T) {
	h := &RecommendationsHandler{accessibleIDs: fakeAccessibleIDs{hidden: map[string]struct{}{"x": {}}}}
	got, err := h.keepAccessible(t.Context(), scored("c", "x", "a", "b"), catalog.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if ids := scoredIDs(got); !slices.Equal(ids, []string{"c", "a", "b"}) {
		t.Fatalf("kept = %v, want [c a b] in the given order", ids)
	}

	// A failed check is an error, never the unfiltered list.
	h.accessibleIDs = fakeAccessibleIDs{err: errors.New("db down")}
	if _, err := h.keepAccessible(t.Context(), scored("a"), catalog.AccessFilter{}); err == nil {
		t.Fatal("keepAccessible answered without its access check")
	} else {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
			t.Fatalf("err = %v, want a 500 API error", err)
		}
	}

	// With no access check wired, nothing is shown.
	h.accessibleIDs = nil
	if got, err := h.keepAccessible(t.Context(), scored("a"), catalog.AccessFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("unwired keepAccessible = %v, %v; want none", got, err)
	}
}

// The v1 Recently Added list, which reads the server-wide query, answers only
// the titles the viewer's libraries hold, newest first.
func TestV1RecentlyAddedDropsInaccessibleItemsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	prefix := fmt.Sprintf("v1-recent-access-%d-", time.Now().UnixNano())
	library := func(name string) int {
		var id int
		if err := pool.QueryRow(t.Context(), `INSERT INTO media_folders (type,name) VALUES ('movies',$1) RETURNING id`, prefix+name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	kids, adults := library("kids"), library("adults")
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id LIKE $1`, prefix+"%")
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = ANY($1)`, []int{kids, adults})
	})
	// Created in the future so they lead the server-wide list; newest first
	// is kids-2, adult-1, kids-1, adult-0, kids-0.
	var want []string
	for i, id := range []string{"kids-0", "adult-0", "kids-1", "adult-1", "kids-2"} {
		lib := kids
		if strings.HasPrefix(id, "adult") {
			lib = adults
		} else {
			want = append([]string{prefix + id}, want...)
		}
		exec(`INSERT INTO media_items (content_id,type,title,status,genres,created_at) VALUES ($1,'movie',$1,'matched','{}',NOW() + make_interval(hours => 1000 + $2))`, prefix+id, i)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, prefix+id, lib)
	}

	h := NewRecommendationsHandler(nil, nil, nil, nil, recommendations.NewRepo(pool), true)
	req := httptest.NewRequest(http.MethodGet, "/recommendations/recently-added?limit=50", nil)
	ctx := authenticatedRecsContext(req.Context(), nil)
	ctx = access.SetScope(ctx, access.Scope{UserID: 7, ProfileID: "profile-1", AllowedLibraryIDs: []int{kids}, LibrariesRestricted: true})
	rec := httptest.NewRecorder()
	h.HandleRecentlyAdded(rec, req.WithContext(ctx))

	var got []string
	for _, id := range decodeScoredItems(t, rec) {
		if strings.HasPrefix(id, prefix) {
			got = append(got, id)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("recently added = %v, want the kids titles newest first %v", got, want)
	}
}
