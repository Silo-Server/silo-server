package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// scopedFetcher is a discover fetcher whose viewer may see only the items in
// visible, as the access-filtered catalog read does.
type scopedFetcher struct {
	visible map[string]bool
}

func (f scopedFetcher) FetchItemsByContentIDs(_ context.Context, ids []string, _ catalog.AccessFilter) ([]*models.MediaItem, error) {
	var out []*models.MediaItem
	for _, id := range ids {
		if f.visible[id] {
			out = append(out, &models.MediaItem{ContentID: id, Type: "movie", Title: id})
		}
	}
	return out, nil
}

func (scopedFetcher) FetchEpisodesByContentIDs(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	return nil, map[string]sections.SectionItemMeta{}, nil
}

func (scopedFetcher) ListOverlaySummaries(context.Context, []string, catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	return map[string]*models.OverlaySummary{}, nil
}

// neighborEngine answers every seed with the same neighbors, one of them
// hidden from the viewer, and records the seeds it was asked about.
type neighborEngine struct {
	seeds []string
}

func (e *neighborEngine) neighbors(seed string) []recommendations.ScoredItem {
	e.seeds = append(e.seeds, seed)
	return []recommendations.ScoredItem{{MediaItemID: "movie-hidden-neighbor"}, {MediaItemID: "movie-visible-neighbor"}}
}

func (e *neighborEngine) SimilarItems(_ context.Context, itemID string, _ int) ([]recommendations.ScoredItem, error) {
	return e.neighbors(itemID), nil
}

func (e *neighborEngine) BecauseYouWatched(_ context.Context, _ int, _ string, itemID string, _ int) ([]recommendations.ScoredItem, error) {
	return e.neighbors(itemID), nil
}

func (e *neighborEngine) GetTasteProfileSummary(context.Context, int, string) (*recommendations.TasteProfileSummary, error) {
	return nil, nil
}

func scopedRecommendationsHandler() (*RecommendationsHandler, *neighborEngine) {
	engine := &neighborEngine{}
	h := NewRecommendationsHandler(engine, nil, nil, nil, nil, true)
	h.Fetcher = scopedFetcher{visible: map[string]bool{"movie-seed": true, "movie-visible-neighbor": true}}
	return h, engine
}

func scoredIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp scoredItemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Items == nil {
		t.Fatal("items must be an array, got null")
	}
	ids := make([]string, 0, len(resp.Items))
	for _, item := range resp.Items {
		ids = append(ids, item.MediaItemID)
	}
	return ids
}

// The v1 similar and because-watched lists name only titles the profile may
// see, and a list built from a title it may not see answers empty without
// asking the engine, as for an unknown title.
func TestRecommendationListsFollowViewerAccess(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		path   string
		handle func(*RecommendationsHandler, http.ResponseWriter, *http.Request)
	}{
		{"similar", "/recommendations/similar/", (*RecommendationsHandler).HandleSimilar},
		{"because-watched", "/recommendations/because-watched/", (*RecommendationsHandler).HandleBecauseWatched},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := func(h *RecommendationsHandler, seed string) []string {
				req := httptest.NewRequest(http.MethodGet, tc.path+seed, nil)
				rctx := chi.NewRouteContext()
				rctx.URLParams.Add("item_id", seed)
				req = req.WithContext(authenticatedRecsContext(req.Context(), rctx))
				rec := httptest.NewRecorder()
				tc.handle(h, rec, req)
				return scoredIDs(t, rec)
			}

			h, engine := scopedRecommendationsHandler()
			if got := call(h, "movie-seed"); !slices.Equal(got, []string{"movie-visible-neighbor"}) {
				t.Fatalf("visible seed listed %v, want only the visible neighbor", got)
			}
			if got := call(h, "movie-hidden-seed"); len(got) != 0 {
				t.Fatalf("hidden seed listed %v, want an empty list", got)
			}
			if !slices.Equal(engine.seeds, []string{"movie-seed"}) {
				t.Fatalf("engine asked about %v, want only the visible seed", engine.seeds)
			}
		})
	}
}

// The v2 cards share the seed check: a hidden seed renders no cards.
func TestSimilarCardsHiddenSeedAnswersEmpty(t *testing.T) {
	t.Parallel()
	h, engine := scopedRecommendationsHandler()
	cards, err := h.SimilarCards(context.Background(), 7, "profile-1", "movie-hidden-seed", 10, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("SimilarCards: %v", err)
	}
	if len(cards) != 0 || len(engine.seeds) != 0 {
		t.Fatalf("cards = %v, engine seeds = %v; want no cards and no engine call", cards, engine.seeds)
	}
}

// visibleScoredItems keeps the visible items in their order and keeps none
// when there is no fetcher to check against.
func TestVisibleScoredItems(t *testing.T) {
	t.Parallel()
	items := []recommendations.ScoredItem{{MediaItemID: "b"}, {MediaItemID: "hidden"}, {MediaItemID: "a"}}
	h := NewRecommendationsHandler(nil, nil, nil, nil, nil, false)
	h.Fetcher = scopedFetcher{visible: map[string]bool{"a": true, "b": true}}
	got, err := h.visibleScoredItems(context.Background(), catalog.AccessFilter{}, items)
	if err != nil {
		t.Fatalf("visibleScoredItems: %v", err)
	}
	if ids := []string{got[0].MediaItemID, got[1].MediaItemID}; len(got) != 2 || !slices.Equal(ids, []string{"b", "a"}) {
		t.Fatalf("visible = %v, want [b a]", got)
	}

	h.Fetcher = nil
	got, err = h.visibleScoredItems(context.Background(), catalog.AccessFilter{}, items)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("without a fetcher got %v, %v; want an empty, non-nil list", got, err)
	}
}

// crowdedEngine ranks two titles the viewer may not see ahead of the one it
// may, and answers at most as many as it is asked for.
type crowdedEngine struct{ neighborEngine }

func (e *crowdedEngine) SimilarItems(_ context.Context, _ string, limit int) ([]recommendations.ScoredItem, error) {
	ranked := []recommendations.ScoredItem{{MediaItemID: "movie-hidden-a"}, {MediaItemID: "movie-hidden-b"}, {MediaItemID: "movie-visible-neighbor"}}
	return ranked[:min(limit, len(ranked))], nil
}

// Hidden candidates do not use up the list: the engine is asked for more
// than the limit and the list is cut to the limit after filtering.
func TestSimilarItemsHiddenCandidatesLeaveRoom(t *testing.T) {
	t.Parallel()
	h := NewRecommendationsHandler(&crowdedEngine{}, nil, nil, nil, nil, true)
	h.Fetcher = scopedFetcher{visible: map[string]bool{"movie-seed": true, "movie-visible-neighbor": true}}
	items, err := h.SimilarItems(context.Background(), "movie-seed", 2, catalog.AccessFilter{})
	if err != nil {
		t.Fatalf("SimilarItems: %v", err)
	}
	if len(items) != 1 || items[0].MediaItemID != "movie-visible-neighbor" {
		t.Fatalf("items = %v, want the visible neighbor behind the hidden ones", items)
	}
}
