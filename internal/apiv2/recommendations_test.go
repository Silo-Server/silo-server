package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/recommendations"
)

// fakeRecommendations records the last call and answers the shared fake card.
type fakeRecommendations struct {
	err                 error
	lastItemID          string
	lastDays, lastLimit int
	lastKind, lastKey   string
	emptyForYou         bool
	emptyTaste          bool
	lastOffset          int
	seedCandidates      int
	lastSeedIDs         []string
	lastMode            string
	lastGenres          []string
	lastExclude         map[string]struct{}
	cardsHasMore        bool
}

func fakeRecommendationRow() handlers.DiscoverRowView {
	return handlers.DiscoverRowView{Type: "cluster", Label: "Because you enjoy Crime", SectionKind: "cluster", SectionKey: "0", Items: []handlers.SectionItemView{fakeCard()}}
}

func (f *fakeRecommendations) cards(limit int) ([]handlers.SectionItemView, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastLimit = limit
	return []handlers.SectionItemView{fakeCard()}, nil
}

func (f *fakeRecommendations) BecauseWatchedCards(_ context.Context, _ int, _, itemID string, limit int, _ catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays = itemID, 0
	return f.cards(limit)
}

func (f *fakeRecommendations) PopularCards(_ context.Context, _ int, _ string, days, limit int, _ catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays = "", days
	return f.cards(limit)
}

func (f *fakeRecommendations) RecentlyAddedCards(_ context.Context, _ int, _ string, days, limit int, _ catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays = "", days
	return f.cards(limit)
}

func (f *fakeRecommendations) ForYouMainCards(_ context.Context, _ int, _ string, limit int, _ catalogpkg.AccessFilter) (handlers.DiscoverRowView, error) {
	if f.err != nil {
		return handlers.DiscoverRowView{}, f.err
	}
	f.lastLimit = limit
	if f.emptyForYou {
		return handlers.DiscoverRowView{Type: "cluster", Label: "For You", SectionKind: "for-you-main", Items: []handlers.SectionItemView{}}, nil
	}
	return fakeRecommendationRow(), nil
}

func (f *fakeRecommendations) ForYouRowCards(_ context.Context, _ int, _ string, limit int, _ catalogpkg.AccessFilter) ([]handlers.DiscoverRowView, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastLimit = limit
	return []handlers.DiscoverRowView{fakeRecommendationRow()}, nil
}

func (f *fakeRecommendations) Discover(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter) (handlers.DiscoverView, error) {
	if f.err != nil {
		return handlers.DiscoverView{}, f.err
	}
	return handlers.DiscoverView{Rows: []handlers.DiscoverRowView{fakeRecommendationRow()}}, nil
}

func (f *fakeRecommendations) Section(_ context.Context, _ int, _, kind, key string, limit int, _ catalogpkg.AccessFilter) (handlers.SectionDetailView, error) {
	if f.err != nil {
		return handlers.SectionDetailView{}, f.err
	}
	f.lastKind, f.lastKey, f.lastLimit = kind, key, limit
	if kind == "genre" && key != "Crime" {
		return handlers.SectionDetailView{Kind: kind, Key: key, Items: []handlers.SectionItemView{}}, nil
	}
	return handlers.SectionDetailView{Kind: kind, Key: key, Type: "genre_sampler", Label: "Popular in Crime", Items: []handlers.SectionItemView{fakeCard()}}, nil
}

func (f *fakeRecommendations) SimilarCards(_ context.Context, _ int, _, itemID string, limit int, _ catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays = itemID, 0
	return f.cards(limit)
}

func (f *fakeRecommendations) SimilarUsersCards(_ context.Context, _ int, _ string, limit int, _ catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays = "", 0
	return f.cards(limit)
}

func (f *fakeRecommendations) TasteProfile(_ context.Context, _ int, _ string) recommendations.TasteProfileSummary {
	if f.emptyTaste {
		return recommendations.TasteProfileSummary{}
	}
	return recommendations.TasteProfileSummary{TopGenres: []string{"Crime", "Thriller"}, FavoriteDirectors: []string{"Michael Mann"}, SignalCounts: map[string]int{"rated": 4, "favorited": 2}, UpdatedAt: "2026-01-02T03:04:05Z"}
}

func (f *fakeRecommendations) TasteSeedItems(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, limit, offset int) ([]handlers.SectionItemView, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	f.lastLimit, f.lastOffset = limit, offset
	return []handlers.SectionItemView{fakeCard()}, f.seedCandidates, nil
}

func (f *fakeRecommendations) SubmitTasteSeed(_ context.Context, _ int, _ string, itemIDs []string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.lastSeedIDs = itemIDs
	return len(itemIDs), nil
}

func (f *fakeRecommendations) WatchTonight(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, limit int) (handlers.WatchTonightView, error) {
	if f.err != nil {
		return handlers.WatchTonightView{}, f.err
	}
	f.lastLimit = limit
	return handlers.WatchTonightView{Items: []handlers.WatchTonightItemView{fakeWatchTonightItem()}, IsCold: true}, nil
}

func (f *fakeRecommendations) WatchTonightCards(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, mode string, genres []string, excludeIDs map[string]struct{}, limit int) handlers.WatchTonightCardsView {
	f.lastMode, f.lastGenres, f.lastExclude, f.lastLimit = mode, genres, excludeIDs, limit
	return handlers.WatchTonightCardsView{Cards: []handlers.WatchTonightCardView{fakeWatchTonightCard()}, HasMore: f.cardsHasMore}
}

func recommendationDeps(t *testing.T) (Dependencies, *fakeRecommendations) {
	t.Helper()
	deps := libraryViewDeps(t)
	fake := &fakeRecommendations{}
	deps.Recommendations = fake
	return deps, fake
}

type recommendationRowDoc struct {
	Type  string                       `json:"type"`
	Title string                       `json:"title"`
	Kind  string                       `json:"kind"`
	Key   string                       `json:"key"`
	Items []map[string]json.RawMessage `json:"items"`
}

func requireFakeCard(t *testing.T, card map[string]json.RawMessage) {
	t.Helper()
	for k, want := range map[string]string{"content_id": `"movie:heat-1995"`, "keywords": `[]`, "progress_updated_at": `"2026-01-02T03:04:05.000Z"`, "overlay_summary": `{"resolution":"4K","hdr":"Dolby Vision"}`, "user_state": `{"played":false,"is_favorite":false,"in_watchlist":true}`} {
		if string(card[k]) != want {
			t.Errorf("%s = %s, want %s", k, card[k], want)
		}
	}
	for _, absent := range []string{"score", "reason", "media_item_id"} {
		if _, ok := card[absent]; ok {
			t.Errorf("a recommendation card carries no %s", absent)
		}
	}
}

func TestRecommendationCardLists(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		path        string
		limit, days int
		itemID      string
	}{
		{path: "/api/v2/recommendations/because-watched/movie:heat-1995?limit=12", limit: 12, itemID: "movie:heat-1995"},
		{path: "/api/v2/recommendations/popular", limit: 20, days: 30},
		{path: "/api/v2/recommendations/popular?days=7&limit=5", limit: 5, days: 7},
		{path: "/api/v2/recommendations/recently-added", limit: 20, days: 14},
	} {
		rec := do(t, h, http.MethodGet, tc.path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []map[string]json.RawMessage `json:"items"`
			Page  *PageInfo                    `json:"page"`
		}
		decodeJSON(t, rec.Body, &body)
		if len(body.Items) != 1 || body.Page != nil {
			t.Fatalf("%s: body = %s", tc.path, rec.Body.String())
		}
		requireFakeCard(t, body.Items[0])
		if fake.lastLimit != tc.limit || fake.lastDays != tc.days || fake.lastItemID != tc.itemID {
			t.Fatalf("%s: seam got limit=%d days=%d item=%q", tc.path, fake.lastLimit, fake.lastDays, fake.lastItemID)
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular?limit=0", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular?limit=51", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/recently-added?days=0", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular?offset=5", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to fetch popular items"}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/popular", "", viewerHeaders()), TypeInternalError)
	if strings.Contains(p.Detail, "popular") {
		t.Fatalf("internal detail leaked: %q", p.Detail)
	}
	deps.Recommendations = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/recommendations/popular", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestRecommendationRows(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	for _, path := range []string{"/api/v2/recommendations/discover", "/api/v2/recommendations/for-you/rows?limit=8"} {
		rec := do(t, h, http.MethodGet, path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []recommendationRowDoc `json:"items"`
		}
		decodeJSON(t, rec.Body, &body)
		if len(body.Items) != 1 || len(body.Items[0].Items) != 1 {
			t.Fatalf("%s: body = %s", path, rec.Body.String())
		}
		row := body.Items[0]
		if row.Type != "cluster" || row.Title != "Because you enjoy Crime" || row.Kind != "cluster" || row.Key != "0" {
			t.Fatalf("%s: row = %+v", path, row)
		}
		requireFakeCard(t, row.Items[0])
		if strings.Contains(rec.Body.String(), `"label"`) || strings.Contains(rec.Body.String(), `"rows"`) {
			t.Fatalf("%s: v1 member names leaked: %s", path, rec.Body.String())
		}
	}
	if fake.lastLimit != 8 {
		t.Fatalf("for-you rows seam got limit=%d", fake.lastLimit)
	}
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/for-you/main", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"title":"Because you enjoy Crime"`) || fake.lastLimit != 20 {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastLimit)
	}
	fake.emptyForYou = true
	rec = do(t, h, http.MethodGet, "/api/v2/recommendations/for-you/main", "", viewerHeaders())
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"for-you-main"`) || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/for-you/rows?limit=51", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/discover", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/discover", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to fetch recommendations"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/discover", "", viewerHeaders()), TypeInternalError)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/for-you/main", "", viewerHeaders()), TypeInternalError)
}

func TestRecommendationSection(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/section/genre?key=Crime&limit=30", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var row recommendationRowDoc
	decodeJSON(t, rec.Body, &row)
	if row.Kind != "genre" || row.Key != "Crime" || row.Type != "genre_sampler" || row.Title != "Popular in Crime" || len(row.Items) != 1 {
		t.Fatalf("row = %+v", row)
	}
	requireFakeCard(t, row.Items[0])
	if fake.lastKind != "genre" || fake.lastKey != "Crime" || fake.lastLimit != 30 {
		t.Fatalf("seam got kind=%q key=%q limit=%d", fake.lastKind, fake.lastKey, fake.lastLimit)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/recommendations/section/popular", "", viewerHeaders())
	if rec.Code != 200 || fake.lastKey != "" || fake.lastLimit != 60 {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastKey, fake.lastLimit)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/recommendations/section/genre?key=Western", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"type":"","title":"","kind":"genre","key":"Western","items":[]}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/cluster", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.key" || p.Errors[0].Code != codeRequired {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/nope", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/popular?limit=61", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/popular", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/popular", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Section not found"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/section/popular", "", viewerHeaders()), TypeNotFound)
}

func TestRecommendationSimilarLists(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		path   string
		limit  int
		itemID string
	}{
		{path: "/api/v2/recommendations/similar/movie:heat-1995?limit=12", limit: 12, itemID: "movie:heat-1995"},
		{path: "/api/v2/recommendations/similar-users", limit: 20},
		{path: "/api/v2/recommendations/similar-users?limit=5", limit: 5},
	} {
		rec := do(t, h, http.MethodGet, tc.path, "", viewerHeaders())
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
		var body struct {
			Items []map[string]json.RawMessage `json:"items"`
			Page  *PageInfo                    `json:"page"`
		}
		decodeJSON(t, rec.Body, &body)
		if len(body.Items) != 1 || body.Page != nil {
			t.Fatalf("%s: body = %s", tc.path, rec.Body.String())
		}
		requireFakeCard(t, body.Items[0])
		if fake.lastLimit != tc.limit || fake.lastItemID != tc.itemID {
			t.Fatalf("%s: seam got limit=%d item=%q", tc.path, fake.lastLimit, fake.lastItemID)
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/similar/movie:heat-1995?limit=51", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/similar-users?offset=5", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/similar-users", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/similar/movie:heat-1995", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to fetch similar items"}
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/similar/movie:heat-1995", "", viewerHeaders()), TypeInternalError)
	if strings.Contains(p.Detail, "similar") {
		t.Fatalf("internal detail leaked: %q", p.Detail)
	}
	deps.Recommendations = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/recommendations/similar-users", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestRecommendationTasteProfile(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/taste-profile", "", viewerHeaders())
	want := `{"top_genres":["Crime","Thriller"],"favorite_directors":["Michael Mann"],"signal_counts":{"favorited":2,"rated":4},"updated_at":"2026-01-02T03:04:05.000Z"}` + "\n"
	if rec.Code != 200 || rec.Body.String() != want {
		t.Fatal(rec.Code, rec.Body.String())
	}
	fake.emptyTaste = true
	rec = do(t, h, http.MethodGet, "/api/v2/recommendations/taste-profile", "", viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"top_genres":[],"favorite_directors":[],"signal_counts":{}}`+"\n" {
		t.Fatal(rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-profile?limit=1", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-profile", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-profile", "", nil), TypeAuthenticationRequired)
	deps.Recommendations = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/recommendations/taste-profile", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestRecommendationTasteSeed(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	fake.seedCandidates = 2
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items?limit=2", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var page struct {
		Items []map[string]json.RawMessage `json:"items"`
		Page  PageInfo                     `json:"page"`
	}
	decodeJSON(t, rec.Body, &page)
	if len(page.Items) != 1 || !page.Page.HasMore || page.Page.NextCursor == "" || fake.lastLimit != 2 || fake.lastOffset != 0 {
		t.Fatalf("body = %s, seam got limit=%d offset=%d", rec.Body.String(), fake.lastLimit, fake.lastOffset)
	}
	requireFakeCard(t, page.Items[0])
	first := page.Page.NextCursor
	fake.seedCandidates = 1
	rec = do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items?limit=2&cursor="+first, "", viewerHeaders())
	if rec.Code != 200 || !strings.HasSuffix(rec.Body.String(), `"page":{"has_more":false}}`+"\n") || fake.lastOffset != 2 {
		t.Fatalf("second page: %d %s offset=%d", rec.Code, rec.Body.String(), fake.lastOffset)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items?limit=1&cursor="+first+"x", "", viewerHeaders()), TypeInvalidCursor)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items?offset=30", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items?limit=61", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items", "", bearer(memberToken)), TypeValidationFailed)

	rec = do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995","movie:collateral-2004"]}`, viewerHeaders())
	if rec.Code != 200 || rec.Body.String() != `{"added":2}`+"\n" || len(fake.lastSeedIDs) != 2 {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastSeedIDs)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":[]}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{}`, viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995"],"replace":true}`, viewerHeaders()), TypeValidationFailed)
	p := requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995"," "]}`, viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "body.item_ids[1]" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995"]}`, bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995"]}`, nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusServiceUnavailable, Code: "unavailable", Message: "User store unavailable"}
	requireProblem(t, do(t, h, http.MethodPost, "/api/v2/recommendations/taste-seed", `{"item_ids":["movie:heat-1995"]}`, viewerHeaders()), TypeDependencyUnavailable)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to fetch taste seed candidates"}
	p = requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/taste-seed/items", "", viewerHeaders()), TypeInternalError)
	if strings.Contains(p.Detail, "taste") {
		t.Fatalf("internal detail leaked: %q", p.Detail)
	}
}

func TestRecommendationWatchTonight(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight", "", viewerHeaders())
	if rec.Code != 200 || fake.lastLimit != 5 {
		t.Fatal(rec.Code, rec.Body.String(), fake.lastLimit)
	}
	var body struct {
		Items  []map[string]json.RawMessage `json:"items"`
		IsCold bool                         `json:"is_cold"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 1 || !body.IsCold || string(body.Items[0]["watch_tonight_source"]) != `"next_up"` || string(body.Items[0]["item_source"]) != `"next_up"` {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireFakeCard(t, body.Items[0])
	if rec = do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight?limit=20", "", viewerHeaders()); rec.Code != 200 || fake.lastLimit != 20 {
		t.Fatal(rec.Code, fake.lastLimit)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight?limit=21", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight", "", nil), TypeAuthenticationRequired)
	fake.err = &handlers.APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "Failed to fetch recommendations"}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight", "", viewerHeaders()), TypeInternalError)
	deps.Recommendations = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/recommendations/watch-tonight", "", viewerHeaders()), TypeDependencyUnavailable)
}

func TestRecommendationWatchTonightCards(t *testing.T) {
	deps, fake := recommendationDeps(t)
	h := newTestHandler(t, deps)
	fake.cardsHasMore = true
	rec := do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover&genres=Crime&genres=Thriller&genres=Crime&exclude_ids=movie:a&exclude_ids=movie:b&limit=3", "", viewerHeaders())
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var body struct {
		Items   []map[string]json.RawMessage `json:"items"`
		HasMore bool                         `json:"has_more"`
		IsCold  bool                         `json:"is_cold"`
	}
	decodeJSON(t, rec.Body, &body)
	if len(body.Items) != 1 || !body.HasMore || body.IsCold {
		t.Fatalf("body = %s", rec.Body.String())
	}
	requireFakeCard(t, body.Items[0])
	if string(body.Items[0]["cast"]) != `[{"name":"Al Pacino","character":"Lt. Vincent Hanna"}]` || string(body.Items[0]["watch_tonight_source"]) != `"recommendation"` {
		t.Fatalf("card = %s", rec.Body.String())
	}
	if fake.lastMode != "discover" || strings.Join(fake.lastGenres, ",") != "Crime,Thriller" || len(fake.lastExclude) != 2 || fake.lastLimit != 3 {
		t.Fatalf("seam got mode=%q genres=%v exclude=%v limit=%d", fake.lastMode, fake.lastGenres, fake.lastExclude, fake.lastLimit)
	}
	if rec = do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=continue", "", viewerHeaders()); rec.Code != 200 || fake.lastMode != "continue" || fake.lastGenres != nil || fake.lastExclude != nil || fake.lastLimit != 12 {
		t.Fatal(rec.Code, fake.lastMode, fake.lastGenres, fake.lastExclude, fake.lastLimit)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=random", "", viewerHeaders()), TypeValidationFailed)
	p := requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover&genres=Crime&genres=Noir", "", viewerHeaders()), TypeValidationFailed)
	if len(p.Errors) != 1 || p.Errors[0].Location != "query.genres[1]" {
		t.Fatalf("errors = %+v", p.Errors)
	}
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover&limit=21", "", viewerHeaders()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover", "", bearer(memberToken)), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover", "", nil), TypeAuthenticationRequired)
	deps.Recommendations = nil
	requireProblem(t, do(t, newTestHandler(t, deps), http.MethodGet, "/api/v2/recommendations/watch-tonight/cards?mode=discover", "", viewerHeaders()), TypeDependencyUnavailable)
}

func fakeWatchTonightItem() handlers.WatchTonightItemView {
	card := fakeCard()
	card.ItemSource = "next_up"
	return handlers.NewWatchTonightItemView(card, "next_up")
}

func fakeWatchTonightCard() handlers.WatchTonightCardView {
	card := fakeCard()
	card.ItemSource = "recommendation"
	return handlers.NewWatchTonightCardView(card, "recommendation", []handlers.WatchTonightCastMemberView{{Name: "Al Pacino", Character: "Lt. Vincent Hanna"}})
}
