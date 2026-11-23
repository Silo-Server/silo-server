package handlers

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type pagedProgressStore struct {
	entries []userstore.WatchProgress
	reads   int
}

func (s *pagedProgressStore) ListProgress(_ context.Context, _, _ string, limit, offset int) ([]userstore.WatchProgress, error) {
	s.reads++
	if offset >= len(s.entries) {
		return nil, nil
	}
	return s.entries[offset:min(offset+limit, len(s.entries))], nil
}

// droppedPrefixFetcher treats every entry whose ID starts with "dropped" as a
// dropped series' episode.
type droppedPrefixFetcher struct{}

func (droppedPrefixFetcher) FetchNextUpItems(context.Context, int, string, *int, []int, catalog.AccessFilter, int) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	return nil, nil, nil
}

func (droppedPrefixFetcher) FilterDroppedProgress(_ context.Context, _ int, _ string, entries []userstore.WatchProgress) ([]userstore.WatchProgress, error) {
	var kept []userstore.WatchProgress
	for _, entry := range entries {
		if !strings.HasPrefix(entry.MediaItemID, "dropped") {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

func progressIDs(entries []userstore.WatchProgress) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.MediaItemID)
	}
	return ids
}

// distanceOrderedCardsRepo answers the taste candidate query like the real
// one: the nearest titles sharing a selected genre, in distance order, cut to
// the asked-for limit.
type distanceOrderedCardsRepo struct {
	nearest []recommendations.ScoredItem
	genres  map[string][]string
}

func (r distanceOrderedCardsRepo) GetTasteProfile(context.Context, int, string) ([]float32, error) {
	return []float32{1}, nil
}

func (r distanceOrderedCardsRepo) FindTasteProfileCandidates(_ context.Context, _ []float32, _ []string, _ []string, limit int, _ catalog.AccessFilter) ([]recommendations.ScoredItem, map[string][]string, error) {
	items := r.nearest[:min(limit, len(r.nearest))]
	genres := make(map[string][]string, len(items))
	for _, item := range items {
		genres[item.MediaItemID] = r.genres[item.MediaItemID]
	}
	return items, genres, nil
}

func (distanceOrderedCardsRepo) GetItemAllGenres(context.Context, []string) (map[string][]string, error) {
	return nil, nil
}

func (distanceOrderedCardsRepo) GetRecommendationCache(context.Context, int, string, string, string) ([]recommendations.ScoredItem, error) {
	return nil, nil
}

func (distanceOrderedCardsRepo) ListDefaultRowItems(context.Context, catalog.AccessFilter, string, []string, int) ([]recommendations.ScoredItem, error) {
	return nil, nil
}

func (distanceOrderedCardsRepo) FilterRecommendableItemIDs(_ context.Context, ids []string, _ catalog.AccessFilter) (map[string]struct{}, error) {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}

// With genres selected, discover cards lead with the titles matching the
// most of them, even when nearer titles matching fewer fill the pool: the
// taste query returns distance order, so the cards read past their pool.
func TestDiscoverCardsLeadWithTheBestGenreMatchBeyondThePool(t *testing.T) {
	repo := distanceOrderedCardsRepo{genres: map[string][]string{}}
	for i := range 4 * discoverMinPoolSize {
		id := fmt.Sprintf("near-%03d", i)
		genres := []string{"Crime"}
		if i == 2*discoverMinPoolSize {
			id = "both"
			genres = []string{"Crime", "Thriller"}
		}
		repo.nearest = append(repo.nearest, recommendations.ScoredItem{MediaItemID: id, Score: 1 - float64(i)/1000})
		repo.genres[id] = genres
	}
	h := &RecommendationsHandler{engine: &fakeRecsEngine{}, enabled: true, cardsRepo: repo}

	cards, cold, err := h.buildDiscoverCards(t.Context(), 1, "p", catalog.AccessFilter{}, []string{"Crime", "Thriller"}, map[string]struct{}{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if cold || len(cards) == 0 {
		t.Fatalf("got %d cards, cold %v; want taste cards", len(cards), cold)
	}
	if got := cards[0].scored.MediaItemID; got != "both" {
		t.Fatalf("first card %q, want the title matching both genres", got)
	}
	if len(cards) > discoverCandidatePoolSize(1, 2) {
		t.Fatalf("%d cards, more than the pool of %d", len(cards), discoverCandidatePoolSize(1, 2))
	}
}
