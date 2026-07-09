package historyimport

import (
	"context"
	"fmt"
	"log/slog"
)

type PlexServerProvider struct {
	client       *PlexClient
	baseURL      string
	token        string
	accountToken string
}

func NewPlexServerProvider(client *PlexClient, baseURL, token string) *PlexServerProvider {
	return &PlexServerProvider{client: client, baseURL: baseURL, token: token}
}

// WithAccountToken enables account-level fetches (the watchlist). Empty
// disables them: server-token-only imports still work, minus the watchlist.
func (p *PlexServerProvider) WithAccountToken(token string) *PlexServerProvider {
	p.accountToken = token
	return p
}

func (p *PlexServerProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	sections, err := p.client.FetchLibrarySections(ctx, p.baseURL, p.token)
	if err != nil {
		return nil, nil, err
	}

	var allItems []PlexItem
	var warnings []string

	for _, section := range sections {
		switch section.Type {
		case "movie":
			items, err := p.client.FetchWatchedItems(ctx, p.baseURL, p.token, section.Key, 1)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to fetch movies from section %q: %v", section.Title, err))
				continue
			}
			allItems = append(allItems, items...)
		case "show":
			items, err := p.client.FetchWatchedItems(ctx, p.baseURL, p.token, section.Key, 4)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("failed to fetch episodes from section %q: %v", section.Title, err))
				continue
			}
			allItems = append(allItems, items...)
		}
	}

	onDeck, err := p.client.FetchOnDeck(ctx, p.baseURL, p.token)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("failed to fetch on-deck items: %v", err))
	} else {
		allItems = append(allItems, onDeck...)
	}

	seriesMeta := p.fetchSeriesMetadata(ctx, allItems, &warnings)

	merged := make(map[string]Record, len(allItems))
	for _, item := range allItems {
		record := NormalizePlexItem(item, seriesMeta[item.GrandparentRatingKey])
		existing, ok := merged[record.ExternalID]
		if !ok {
			merged[record.ExternalID] = record
			continue
		}
		merged[record.ExternalID] = mergeRecords(existing, record)
	}

	records := make([]Record, 0, len(merged))
	for _, record := range merged {
		records = append(records, record)
	}
	// The account watchlist rides along with the history import. Best
	// effort: a watchlist fetch failure downgrades to a warning so the
	// watch-history import still completes (issue #245).
	if p.accountToken != "" {
		items, watchlistWarnings, err := p.client.FetchWatchlist(ctx, p.accountToken)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("watchlist fetch failed: %v", err))
		} else {
			warnings = append(warnings, watchlistWarnings...)
			for _, item := range items {
				records = append(records, NormalizePlexWatchlistItem(item))
			}
		}
	}

	return records, warnings, nil
}

func (p *PlexServerProvider) fetchSeriesMetadata(ctx context.Context, items []PlexItem, warnings *[]string) map[string]*PlexItem {
	seen := make(map[string]struct{})
	var seriesKeys []string
	for _, item := range items {
		if item.Type != "episode" || item.GrandparentRatingKey == "" {
			continue
		}
		if _, ok := seen[item.GrandparentRatingKey]; ok {
			continue
		}
		seen[item.GrandparentRatingKey] = struct{}{}
		seriesKeys = append(seriesKeys, item.GrandparentRatingKey)
	}

	result := make(map[string]*PlexItem, len(seriesKeys))
	for _, key := range seriesKeys {
		meta, err := p.client.FetchMetadata(ctx, p.baseURL, p.token, key)
		if err != nil {
			slog.WarnContext(ctx, "plex history import: failed to fetch series metadata", "component", "historyimport", "rating_key", key, "error", err)
			*warnings = append(*warnings, fmt.Sprintf("failed to fetch series metadata for %s: %v", key, err))
			continue
		}
		if meta != nil {
			result[key] = meta
		}
	}
	return result
}
