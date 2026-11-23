package handlers

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/recommendations"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// pagedProgressStore is a user store serving entries, already in listing
// order, by offset page; the profile holds no favorites or other flags.
type pagedProgressStore struct {
	userstore.UserStore
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

func (s *pagedProgressStore) ItemUserFlags(context.Context, string, []string, []string) (map[string]userstore.ItemUserFlags, bool, error) {
	return nil, true, nil
}

func (s *pagedProgressStore) ForUser(context.Context, int) (userstore.UserStore, error) {
	return s, nil
}

func (s *pagedProgressStore) Close() error { return nil }

// droppedPrefixFetcher serves nextUp as the Next Up row and treats every
// entry whose ID starts with "dropped" as a dropped series' episode.
type droppedPrefixFetcher struct {
	nextUp []sections.SectionItemMeta
}

func (f droppedPrefixFetcher) FetchNextUpItems(context.Context, int, string, *int, []int, catalog.AccessFilter, int) ([]*models.MediaItem, map[string]sections.SectionItemMeta, error) {
	items := make([]*models.MediaItem, 0, len(f.nextUp))
	meta := make(map[string]sections.SectionItemMeta, len(f.nextUp))
	for _, m := range f.nextUp {
		id := *m.SeriesID
		items = append(items, &models.MediaItem{ContentID: id})
		meta[id] = m
	}
	return items, meta, nil
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

// Watch Tonight answers the same cards on every read. Continue Watching and
// Next Up titles the recommendations do not hold share one score per source,
// so they go most recent activity first, then by content ID.
func TestWatchTonightOrdersTiedCardsByActivity(t *testing.T) {
	at := time.Date(2026, 11, 1, 20, 0, 0, 0, time.UTC)
	progress := func(id string, hoursAgo int) userstore.WatchProgress {
		updated := at.Add(-time.Duration(hoursAgo) * time.Hour)
		return userstore.WatchProgress{MediaItemID: id, UpdatedAt: updated.Format(time.RFC3339), UpdatedAtTime: updated}
	}
	nextUp := func(id string, hoursAgo int) sections.SectionItemMeta {
		// The fake keys the item by SeriesID; the handler reads no other field.
		return sections.SectionItemMeta{SeriesID: &id, ItemSource: "next_up", SortTimestamp: at.Add(-time.Duration(hoursAgo) * time.Hour)}
	}
	for _, tc := range []struct {
		name     string
		progress []userstore.WatchProgress
		nextUp   []sections.SectionItemMeta
		want     []string
	}{
		{
			name: "Continue Watching by most recent progress, then content ID",
			// The store orders by progress time alone, so tied rows come
			// back in either order.
			progress: []userstore.WatchProgress{progress("cw-new", 0), progress("cw-b", 1), progress("cw-a", 1), progress("cw-old", 3)},
			nextUp:   []sections.SectionItemMeta{nextUp("nu", 0)},
			want:     []string{"cw-new", "cw-a", "cw-b", "cw-old", "nu"},
		},
		{
			name:     "Next Up after Continue Watching by most recent activity",
			progress: []userstore.WatchProgress{progress("cw", 9)},
			nextUp:   []sections.SectionItemMeta{nextUp("nu-mid", 2), nextUp("nu-new", 1), nextUp("nu-old", 5), nextUp("nu-b", 6), nextUp("nu-a", 6)},
			want:     []string{"cw", "nu-new", "nu-mid", "nu-old", "nu-a"},
		},
		{
			name: "a store keeping only whole-second progress times",
			progress: []userstore.WatchProgress{
				{MediaItemID: "cw-a", UpdatedAt: at.Add(-time.Hour).Format(time.RFC3339)},
				{MediaItemID: "cw-b", UpdatedAt: at.Format(time.RFC3339)},
			},
			want: []string{"cw-b", "cw-a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &pagedProgressStore{entries: tc.progress}
			var items []*models.MediaItem
			for _, p := range tc.progress {
				items = append(items, &models.MediaItem{ContentID: p.MediaItemID, Type: "movie"})
			}
			for _, m := range tc.nextUp {
				items = append(items, &models.MediaItem{ContentID: *m.SeriesID, Type: "episode"})
			}
			h := &RecommendationsHandler{
				storeProvider:       store,
				Fetcher:             stubDiscoverFetcher{items: items},
				WatchTonightFetcher: droppedPrefixFetcher{nextUp: tc.nextUp},
			}
			// The cards pass through a map, whose order changes per run.
			for range 20 {
				view, err := h.WatchTonight(t.Context(), 7, "p1", catalog.AccessFilter{}, watchTonightLimit)
				if err != nil {
					t.Fatal(err)
				}
				got := make([]string, len(view.Items))
				for i, item := range view.Items {
					got[i] = item.ContentID
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("cards = %v, want %v", got, tc.want)
				}
			}
		})
	}
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

// coldPopularCardsRepo is a discover cards repo for a profile without a taste
// profile, whose cached Popular row is popular.
type coldPopularCardsRepo struct {
	distanceOrderedCardsRepo
	popular []recommendations.ScoredItem
}

func (coldPopularCardsRepo) GetTasteProfile(context.Context, int, string) ([]float32, error) {
	return nil, nil
}

func (r coldPopularCardsRepo) GetRecommendationCache(context.Context, int, string, string, string) ([]recommendations.ScoredItem, error) {
	return r.popular, nil
}

// The cold-start rows are not filtered for the titles the client already
// showed, so the pool is cut only after those leave it: a page whose top
// Popular titles were all shown still fills with the ones after them.
func TestColdDiscoverCardsSkipShownTitlesBeforeCuttingThePool(t *testing.T) {
	pool := discoverCandidatePoolSize(1, 0)
	repo := coldPopularCardsRepo{distanceOrderedCardsRepo: distanceOrderedCardsRepo{genres: map[string][]string{}}}
	shown := map[string]struct{}{}
	for i := range 2 * pool {
		id := fmt.Sprintf("popular-%03d", i)
		repo.popular = append(repo.popular, recommendations.ScoredItem{MediaItemID: id, Score: float64(2*pool - i)})
		if i < pool {
			shown[id] = struct{}{}
		}
	}
	h := &RecommendationsHandler{engine: &fakeRecsEngine{}, enabled: true, cardsRepo: repo}

	cards, cold, err := h.buildDiscoverCards(t.Context(), 1, "p", catalog.AccessFilter{}, nil, shown, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !cold || len(cards) != pool {
		t.Fatalf("got %d cards, cold %v; want %d cold-start cards", len(cards), cold, pool)
	}
	for _, card := range cards {
		if _, ok := shown[card.scored.MediaItemID]; ok {
			t.Fatalf("card %s was already shown", card.scored.MediaItemID)
		}
	}
}
