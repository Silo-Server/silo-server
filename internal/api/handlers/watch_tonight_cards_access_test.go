package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
)

// hydratingFetcher hydrates every requested ID outside missing.
type hydratingFetcher struct {
	missing map[string]struct{}
	err     error
}

func (f hydratingFetcher) FetchItemsByContentIDs(_ context.Context, ids []string, _ catalog.AccessFilter) ([]*models.MediaItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	var items []*models.MediaItem
	for _, id := range ids {
		if _, gone := f.missing[id]; !gone {
			items = append(items, &models.MediaItem{ContentID: id, Type: "movie", Title: id})
		}
	}
	return items, nil
}

func (hydratingFetcher) FetchEpisodesByContentIDs(context.Context, []string, catalog.AccessFilter) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	return nil, map[string]sections.SectionItemMeta{}, nil
}

func (hydratingFetcher) ListOverlaySummaries(context.Context, []string, catalog.AccessFilter) (map[string]*models.OverlaySummary, error) {
	return map[string]*models.OverlaySummary{}, nil
}

// castByID gives every item one actor named after it.
type castByID struct{}

func (castByID) ListForItems(_ context.Context, ids []string) (map[string][]models.ItemPerson, error) {
	out := make(map[string][]models.ItemPerson, len(ids))
	for _, id := range ids {
		out[id] = []models.ItemPerson{{Person: models.Person{Name: id + "-actor"}, Kind: models.PersonKindActor}}
	}
	return out, nil
}

func recommendationCandidates(ids ...string) []mergedItem {
	out := make([]mergedItem, len(ids))
	for i, id := range ids {
		out[i] = mergedItem{scored: recommendations.ScoredItem{MediaItemID: id}, source: "recommendation"}
	}
	return out
}

// A candidate dropped in hydration takes no card, and every later card keeps
// its own cast; a page is full only when a hydrated candidate is left over.
func TestSwipeCardsSkipADroppedCandidateWithoutShiftingCast(t *testing.T) {
	h := &RecommendationsHandler{CastFetcher: castByID{}}
	candidates := recommendationCandidates("a", "dropped", "c", "d")
	enriched, err := (&RecommendationsHandler{Fetcher: hydratingFetcher{missing: map[string]struct{}{"dropped": {}}}}).
		enrichItems(t.Context(), 7, "p1", catalog.AccessFilter{}, []string{"a", "dropped", "c", "d"})
	if err != nil {
		t.Fatal(err)
	}

	cards, more := h.swipeCards(t.Context(), candidates, enriched, 3, imagesize.Request{})
	var got []string
	for _, card := range cards {
		if len(card.Cast) != 1 || card.Cast[0].Name != card.ContentID+"-actor" {
			t.Fatalf("card %s has cast %+v, want its own", card.ContentID, card.Cast)
		}
		got = append(got, card.ContentID)
	}
	if !slices.Equal(got, []string{"a", "c", "d"}) || more {
		t.Fatalf("cards = %v more=%v, want [a c d] and no more", got, more)
	}

	cards, more = h.swipeCards(t.Context(), candidates, enriched, 2, imagesize.Request{})
	if len(cards) != 2 || cards[1].ContentID != "c" || !more {
		t.Fatalf("limit 2: %d cards, more=%v; want a and c with more", len(cards), more)
	}
}

type watchTonightReader struct {
	stubRecommendationsReader
	result recommendations.WatchTonightResult
}

func (r watchTonightReader) GetWatchTonight(context.Context, int, string, int, catalog.AccessFilter) (recommendations.WatchTonightResult, error) {
	return r.result, nil
}

// A failed item lookup is a 500, not 200 with no items.
func TestWatchTonightItemLookupFailureIsAnError(t *testing.T) {
	h := NewRecommendationsHandler(nil, watchTonightReader{result: recommendations.WatchTonightResult{Items: []recommendations.ScoredItem{{MediaItemID: "a", Score: 1}}}}, nil, nil, nil, true)
	h.Fetcher = hydratingFetcher{err: errors.New("db down")}

	_, err := h.WatchTonight(t.Context(), 7, "p1", catalog.AccessFilter{}, 5)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v, want a 500 API error", err)
	}
}

// The frozen v1 route keeps answering 200 with no items when the lookup
// fails, as it did before the v2 seam started reporting the error.
func TestV1WatchTonightItemLookupFailureAnswersEmpty(t *testing.T) {
	h := NewRecommendationsHandler(nil, watchTonightReader{result: recommendations.WatchTonightResult{Items: []recommendations.ScoredItem{{MediaItemID: "a", Score: 1}}}}, nil, nil, nil, true)
	h.Fetcher = hydratingFetcher{err: errors.New("db down")}

	rr := httptest.NewRecorder()
	h.HandleWatchTonight(rr, newAuthorizedProfileRequestWithRole(http.MethodGet, "/api/v1/recommendations/watch-tonight", "", "user", "p1"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || len(body.Items) != 0 {
		t.Fatalf("body = %s (%v), want no items", rr.Body.String(), err)
	}
}

// fakeCardsRepo serves discover-mode cards from memory: a taste profile
// whose candidates are personal, cached global rows, and a type check that
// leaves books out.
type fakeCardsRepo struct {
	tasteReads int
	global     map[string][]recommendations.ScoredItem
	// recent is the live Recently Added row; recentExclude and recentLimit
	// record its last read.
	recent        []recommendations.ScoredItem
	recentExclude []string
	recentLimit   int
	books         map[string]struct{}
}

func (f *fakeCardsRepo) ListDefaultRowItems(_ context.Context, _ catalog.AccessFilter, kind string, exclude []string, limit int) ([]recommendations.ScoredItem, error) {
	if kind != recommendations.RecTypeRecentlyAdded {
		return nil, fmt.Errorf("unexpected default row %q", kind)
	}
	f.recentExclude, f.recentLimit = slices.Sorted(slices.Values(exclude)), limit
	var out []recommendations.ScoredItem
	for _, item := range f.recent {
		if !slices.Contains(exclude, item.MediaItemID) && len(out) < limit {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeCardsRepo) GetTasteProfile(context.Context, int, string) ([]float32, error) {
	f.tasteReads++
	return []float32{1}, nil
}

func (f *fakeCardsRepo) FindTasteProfileCandidates(context.Context, []float32, []string, []string, int, catalog.AccessFilter) ([]recommendations.ScoredItem, map[string][]string, error) {
	return []recommendations.ScoredItem{{MediaItemID: "personal", Score: 1}}, map[string][]string{}, nil
}

func (f *fakeCardsRepo) GetItemAllGenres(context.Context, []string) (map[string][]string, error) {
	return map[string][]string{}, nil
}

func (f *fakeCardsRepo) GetRecommendationCache(_ context.Context, _ int, _, recType, _ string) ([]recommendations.ScoredItem, error) {
	return f.global[recType], nil
}

func (f *fakeCardsRepo) FilterRecommendableItemIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, book := f.books[id]; !book {
			out[id] = struct{}{}
		}
	}
	return out, nil
}

// With recommendations disabled, discover-mode cards come from the global
// rows only, never the profile's taste profile, and leave out types the rows
// no longer offer, such as a book cached before books left them.
func TestDiscoverCardsServeNoPersonalCandidatesWhileDisabled(t *testing.T) {
	repo := &fakeCardsRepo{
		global: map[string][]recommendations.ScoredItem{
			recommendations.RecTypePopular: {{MediaItemID: "popular", Score: 2}, {MediaItemID: "cached-book", Score: 3}},
		},
		recent: []recommendations.ScoredItem{{MediaItemID: "recent", Score: 1}},
		books:  map[string]struct{}{"cached-book": {}},
	}
	h := &RecommendationsHandler{cardsRepo: repo, enabled: false}
	merged, isCold, err := h.buildDiscoverCards(t.Context(), 7, "p1", catalog.AccessFilter{}, nil, map[string]struct{}{}, 12)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range merged {
		ids = append(ids, m.scored.MediaItemID)
	}
	if !isCold || !slices.Equal(ids, []string{"popular", "recent"}) {
		t.Fatalf("cards = %v (cold %t), want the global rows without the book", ids, isCold)
	}
	if repo.tasteReads != 0 {
		t.Fatalf("taste profile read %d times, want none while disabled", repo.tasteReads)
	}
}

// With nothing cached, as on a fresh server or once the cached rows expire
// with recommendations disabled, discover-mode cards come from the live
// Recently Added row, read without the titles the client already showed.
func TestDiscoverCardsFallBackToTheLiveRecentlyAddedRow(t *testing.T) {
	repo := &fakeCardsRepo{recent: []recommendations.ScoredItem{
		{MediaItemID: "shown", Score: 3}, {MediaItemID: "newest", Score: 2}, {MediaItemID: "older", Score: 1},
	}}
	h := &RecommendationsHandler{cardsRepo: repo, enabled: false}
	merged, isCold, err := h.buildDiscoverCards(t.Context(), 7, "p1", catalog.AccessFilter{}, nil, map[string]struct{}{"shown": {}}, 12)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range merged {
		ids = append(ids, m.scored.MediaItemID)
	}
	if !isCold || !slices.Equal(ids, []string{"newest", "older"}) {
		t.Fatalf("cards = %v (cold %t), want the live Recently Added row", ids, isCold)
	}
	if !slices.Equal(repo.recentExclude, []string{"shown"}) || repo.recentLimit != discoverCandidatePoolSize(12, 0) {
		t.Fatalf("Recently Added read with exclude=%v limit=%d", repo.recentExclude, repo.recentLimit)
	}
}

// The live Recently Added fallback is limited in SQL, so the profile's
// excluded titles (watched, favorited, rated) go into the query with the
// shown ones. Removed only afterwards, they would leave a cold profile a
// short page, and a favorite would come back as a recommendation.
func TestDiscoverCardsExcludeTheProfilesTitlesInTheQuery(t *testing.T) {
	repo := &fakeCardsRepo{recent: []recommendations.ScoredItem{
		{MediaItemID: "favorite", Score: 5}, {MediaItemID: "watched", Score: 4}, {MediaItemID: "shown", Score: 3},
		{MediaItemID: "newest", Score: 2}, {MediaItemID: "older", Score: 1},
	}}
	h := &RecommendationsHandler{cardsRepo: repo, enabled: false}
	h.exclusionSet = func(_ context.Context, userID int, profileID string) (map[string]struct{}, error) {
		if userID != 7 || profileID != "p1" {
			t.Fatalf("exclusion set read for %d/%s", userID, profileID)
		}
		return map[string]struct{}{"favorite": {}, "watched": {}}, nil
	}
	merged, _, err := h.buildDiscoverCards(t.Context(), 7, "p1", catalog.AccessFilter{}, nil, map[string]struct{}{"shown": {}}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range merged {
		ids = append(ids, m.scored.MediaItemID)
	}
	if !slices.Equal(repo.recentExclude, []string{"favorite", "shown", "watched"}) {
		t.Fatalf("Recently Added read with exclude=%v, want the shown and the profile's excluded titles", repo.recentExclude)
	}
	if !slices.Equal(ids, []string{"newest", "older"}) {
		t.Fatalf("cards = %v, want only eligible titles", ids)
	}

	// Without the set, the shown titles are still left out.
	h.exclusionSet = func(context.Context, int, string) (map[string]struct{}, error) {
		return nil, errors.New("signals unavailable")
	}
	if _, _, err := h.buildDiscoverCards(t.Context(), 7, "p1", catalog.AccessFilter{}, nil, map[string]struct{}{"shown": {}}, 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(repo.recentExclude, []string{"shown"}) {
		t.Fatalf("Recently Added read with exclude=%v after the set failed, want the shown title", repo.recentExclude)
	}
}
