package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// fakeRecsEngine answers a fixed list and records what it was asked.
type fakeRecsEngine struct {
	items        []recommendations.ScoredItem
	similarCalls int
	becauseCalls int
	becauseLimit int
	becauseWith  catalog.AccessFilter
}

func (f *fakeRecsEngine) SimilarItems(context.Context, int, string, string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	f.similarCalls++
	return f.items, nil
}

func (f *fakeRecsEngine) BecauseYouWatched(_ context.Context, _ int, _, _ string, limit int, filter catalog.AccessFilter) ([]recommendations.ScoredItem, error) {
	f.becauseCalls++
	f.becauseLimit = limit
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

// An anchored list ranks under the viewer's scope, and an anchor the viewer
// cannot see answers what an unknown anchor answers: an empty list, without
// running the engine.
func TestAnchoredRecommendationsHonorTheViewerScope(t *testing.T) {
	filter := catalog.AccessFilter{UserID: 7, ProfileID: "profile-1", AllowedLibraryIDs: []int{4}}
	engine := &fakeRecsEngine{items: scored("a", "b")}
	h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
	h.accessibleIDs = fakeAccessibleIDs{hidden: map[string]struct{}{"hidden-anchor": {}}}
	h.Fetcher = stubDiscoverFetcher{}

	similar, err := h.SimilarCards(t.Context(), 7, "profile-1", "hidden-anchor", 10, filter)
	if err != nil || len(similar) != 0 {
		t.Fatalf("similar = %v, %v; want none", similar, err)
	}
	because, err := h.BecauseWatchedCards(t.Context(), 7, "profile-1", "hidden-anchor", 10, filter)
	if err != nil || len(because) != 0 {
		t.Fatalf("because watched = %v, %v; want none", because, err)
	}
	if engine.similarCalls != 0 || engine.becauseCalls != 0 {
		t.Fatalf("engine ran %d similar and %d because-watched reads for a hidden anchor", engine.similarCalls, engine.becauseCalls)
	}

	if _, err := h.BecauseWatched(t.Context(), 7, "profile-1", "anchor", 10, filter); err != nil {
		t.Fatal(err)
	}
	if got := engine.becauseWith; !slices.Equal(got.AllowedLibraryIDs, []int{4}) || got.UserID != 7 || got.ProfileID != "profile-1" {
		t.Fatalf("engine filter = %+v, want the viewer's scope", got)
	}
}

// The shared service caps Because You Watched at the row maximum before the
// engine triples the limit into candidate queries; v1 forwards any positive
// limit.
func TestBecauseWatchedCapsTheEngineLimit(t *testing.T) {
	for _, tc := range []struct{ limit, want int }{{0, 20}, {50, 50}, {51, 50}, {math.MaxInt, 50}} {
		engine := &fakeRecsEngine{}
		h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
		h.accessibleIDs = fakeAccessibleIDs{}
		if _, err := h.BecauseWatched(t.Context(), 7, "profile-1", "anchor", tc.limit, catalog.AccessFilter{}); err != nil {
			t.Fatal(err)
		}
		if engine.becauseCalls != 1 || engine.becauseLimit != tc.want {
			t.Fatalf("limit %d: engine calls %d with limit %d; want one with %d", tc.limit, engine.becauseCalls, engine.becauseLimit, tc.want)
		}
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

// Recently Added reads the server-wide query, so it answers only the titles
// the viewer's libraries hold, newest first, and reads past hidden titles to
// fill its limit.
func TestRecentlyAddedItemsKeepsTheViewersLibrariesDB(t *testing.T) {
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
	prefix := fmt.Sprintf("recent-access-%d-", time.Now().UnixNano())
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
	filter := catalog.AccessFilter{UserID: 7, ProfileID: "profile-1", AllowedLibraryIDs: []int{kids}}
	read := func(limit int) []string {
		t.Helper()
		items, err := h.RecentlyAddedItems(t.Context(), 7, "profile-1", 14, limit, filter)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, id := range scoredIDs(items) {
			if strings.HasPrefix(id, prefix) {
				got = append(got, id)
			}
		}
		return got
	}
	if got := read(50); !slices.Equal(got, want) {
		t.Fatalf("recently added = %v, want the kids titles newest first %v", got, want)
	}

	// Hidden titles newer than every visible one do not take the list's
	// places: it reads past them to fill the limit.
	for i := range 10 {
		id := fmt.Sprintf("%snewer-adult-%d", prefix, i)
		exec(`INSERT INTO media_items (content_id,type,title,status,genres,created_at) VALUES ($1,'movie',$1,'matched','{}',NOW() + make_interval(hours => 2000 + $2))`, id, i)
		exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, id, adults)
	}
	if got := read(2); !slices.Equal(got, want[:2]) {
		t.Fatalf("recently added with limit 2 = %v, want %v", got, want[:2])
	}
}
