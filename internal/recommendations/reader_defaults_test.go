package recommendations

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func scored(ids ...string) []ScoredItem {
	items := make([]ScoredItem, len(ids))
	for i, id := range ids {
		items[i] = ScoredItem{MediaItemID: id}
	}
	return items
}

// watchedStore is a user store in which profile p1 watched the given items.
func watchedStore(ids ...string) fakeSignalProvider {
	store := &fakeSignalStore{}
	for _, id := range ids {
		store.progress = append(store.progress, userstore.WatchProgress{ProfileID: "p1", MediaItemID: id, Completed: true})
	}
	return fakeSignalProvider{store: store}
}

// quietHouseholdCatalog is the global row store of a single-account server
// whose titles were all added a month ago: no title has a crowd of accounts
// behind it, and nothing was added in the last 14 days. cached holds what the
// worker writes.
type quietHouseholdCatalog struct {
	cached map[string][]ScoredItem
}

func (c *quietHouseholdCatalog) GetPopularItems(_ context.Context, _, minAccounts, _ int) ([]ScoredItem, error) {
	if minAccounts > 1 {
		return nil, nil
	}
	return scored("household-pick"), nil
}

func (c *quietHouseholdCatalog) GetRecentlyAddedItems(_ context.Context, days, _ int) ([]ScoredItem, error) {
	if days > 0 && days < 30 {
		return nil, nil
	}
	return scored("month-old-1", "month-old-2"), nil
}

func (c *quietHouseholdCatalog) GetTopGenres(context.Context, int) ([]string, error) {
	return nil, nil
}

func (c *quietHouseholdCatalog) GetGenreSamplerItems(context.Context, string, int) ([]ScoredItem, error) {
	return nil, nil
}

func (c *quietHouseholdCatalog) UpsertRecommendationCache(_ context.Context, _ int, _, recType, _ string, items []ScoredItem, _ string) error {
	c.cached[recType] = items
	return nil
}

func (c *quietHouseholdCatalog) ExtendGlobalRecommendationCache(context.Context, string, bool, string) (int64, error) {
	return 0, nil
}

func (c *quietHouseholdCatalog) DeleteGlobalRecommendationCache(context.Context, string, bool, []string) (int64, error) {
	return 0, nil
}
