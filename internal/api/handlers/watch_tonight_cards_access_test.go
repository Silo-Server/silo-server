package handlers

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
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

	cards, more := h.swipeCards(t.Context(), candidates, enriched, 3)
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

	cards, more = h.swipeCards(t.Context(), candidates, enriched, 2)
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
