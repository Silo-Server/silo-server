package apiv2

import (
	"context"
	"encoding/json"
	"fmt"

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
	swipePool           int
	lastFilter          catalogpkg.AccessFilter
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

func (f *fakeRecommendations) PopularCards(_ context.Context, _ int, _ string, days, limit int, filter catalogpkg.AccessFilter) ([]handlers.SectionItemView, error) {
	f.lastItemID, f.lastDays, f.lastFilter = "", days, filter
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

func (f *fakeRecommendations) SubmitTasteSeed(_ context.Context, _ int, _ string, itemIDs []string, _ catalogpkg.AccessFilter) (int, error) {
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

func (f *fakeRecommendations) WatchTonightCards(_ context.Context, _ int, _ string, _ catalogpkg.AccessFilter, mode string, genres []string, excludeIDs map[string]struct{}, limit int) (handlers.WatchTonightCardsView, error) {
	f.lastMode, f.lastGenres, f.lastExclude, f.lastLimit = mode, genres, excludeIDs, limit
	if f.err != nil {
		return handlers.WatchTonightCardsView{}, f.err
	}
	if f.swipePool > 0 {
		cards := make([]handlers.WatchTonightCardView, 0, limit)
		more := false
		for i := range f.swipePool {
			id := fmt.Sprintf("movie:swipe-%d", i)
			if _, ok := excludeIDs[id]; ok {
				continue
			}
			if len(cards) == limit {
				more = true
				break
			}
			card := fakeWatchTonightCard()
			card.ContentID = id
			cards = append(cards, card)
		}
		return handlers.WatchTonightCardsView{Cards: cards, HasMore: more}, nil
	}
	return handlers.WatchTonightCardsView{Cards: []handlers.WatchTonightCardView{fakeWatchTonightCard()}, HasMore: f.cardsHasMore}, nil
}

type recommendationRowDoc struct {
	Type  string                       `json:"type"`
	Title string                       `json:"title"`
	Kind  string                       `json:"kind"`
	Key   string                       `json:"key"`
	Items []map[string]json.RawMessage `json:"items"`
}

func fakeWatchTonightItem() handlers.WatchTonightItemView {
	card := fakeCard()
	card.ItemSource = "next_up"
	return handlers.NewWatchTonightItemView(card, "next_up")
}

func fakeWatchTonightCard() handlers.WatchTonightCardView {
	card := fakeCard()
	card.ItemSource = "recommendation"
	// Production leaves runtime off the embedded section item and sets it on
	// the swipe card; mirror that so the test proves v2 reads the outer member.
	card.Runtime = 0
	return handlers.NewWatchTonightCardView(card, "recommendation", 170, []handlers.WatchTonightCastMemberView{{Name: "Al Pacino", Character: "Lt. Vincent Hanna"}})
}
