package historyimport

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"
)

// collectStream reads a whole stream and merges records by external ID, the
// view of a source the import's writes converge on.
func collectStream(ctx context.Context, provider Provider) ([]Record, []string, error) {
	merged := map[string]Record{}
	var watchlist []Record
	var warnings []string
	err := provider.Stream(ctx, nil, func(batch RecordBatch) error {
		warnings = append(warnings, batch.Warnings...)
		for _, record := range batch.Records {
			if record.Watchlisted {
				watchlist = append(watchlist, record)
				continue
			}
			if existing, ok := merged[record.ExternalID]; ok {
				record = mergeRecords(existing, record)
			}
			merged[record.ExternalID] = record
		}
		return nil
	})
	if err != nil {
		return nil, warnings, err
	}
	return append(slices.Collect(maps.Values(merged)), watchlist...), warnings, nil
}

func (p *PlexServerProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return collectStream(ctx, p)
}
func (p *PlexAdminProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return collectStream(ctx, p)
}
func (p *EmbyProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return collectStream(ctx, p)
}
func (p *JellyfinProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return collectStream(ctx, p)
}
func (p privateNetworkProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	return collectStream(ctx, p)
}

func (c *EmbyClient) FetchItems(ctx context.Context, auth embyLocalAuth, filter string) ([]embyItem, error) {
	return c.fetchAllItems(ctx, auth, embyEndpointItems, filter, embyPlayableTypes)
}

func (c *JellyfinClient) FetchItems(ctx context.Context, auth jellyfinLocalAuth, filter, types string) ([]jellyfinItem, error) {
	var items []jellyfinItem
	err := c.forEachItemsPage(ctx, auth, filter, types, 0, func(page []jellyfinItem, _ int) error {
		items = append(items, page...)
		return nil
	})
	return items, err
}

// FetchPlayedSince gathers streamPlayedSince, keeping each item once. A read
// that reports itself unsorted hands its caller nothing to use.
func (c *JellyfinClient) FetchPlayedSince(ctx context.Context, auth jellyfinLocalAuth, since time.Time) ([]jellyfinItem, bool, error) {
	var items []jellyfinItem
	seen := map[string]bool{}
	sorted, err := c.streamPlayedSince(ctx, auth, since, func(page []jellyfinItem) error {
		for _, item := range page {
			if !seen[item.ID] {
				seen[item.ID] = true
				items = append(items, item)
			}
		}
		return nil
	})
	if err != nil || !sorted {
		return nil, sorted, err
	}
	return items, true, nil
}

func (c *PlexClient) FetchSectionItems(ctx context.Context, baseURL, token, key string, mediaType int) ([]PlexItem, error) {
	var items []PlexItem
	err := c.forEachSectionPage(ctx, baseURL, token, key, mediaType, "", 0, func(page []PlexItem, _ int) error {
		items = append(items, page...)
		return nil
	})
	return items, err
}

func (c *PlexClient) FetchUserHistory(ctx context.Context, baseURL, token, accountID string) ([]PlexHistoryItem, error) {
	var items []PlexHistoryItem
	err := c.forEachUserHistoryPage(ctx, baseURL, token, accountID, 0, func(page []PlexHistoryItem, _ int) error {
		items = append(items, page...)
		return nil
	})
	return items, err
}

func importedShowsOf(t *testing.T, drops SeriesDropStore, episodes []importedEpisode) *importedShows {
	t.Helper()
	shows := newImportedShows()
	shows.add(t.Context(), drops, episodes)
	if shows.failed != nil {
		t.Fatal(shows.failed)
	}
	return shows
}
