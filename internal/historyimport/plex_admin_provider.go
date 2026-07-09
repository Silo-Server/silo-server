package historyimport

import (
	"context"
	"fmt"
	"log/slog"
)

// PlexAdminProvider fetches watch history for a specific Plex account using an admin token.
// It uses the PMS session history API (GET /status/sessions/history/all?accountID=X)
// rather than the per-user library endpoints used by PlexServerProvider.
type PlexAdminProvider struct {
	client    *PlexClient
	baseURL   string
	token     string
	accountID string
}

// NewPlexAdminProvider returns a PlexAdminProvider that will fetch watch history
// for accountID using the given admin token against the PMS at baseURL.
func NewPlexAdminProvider(client *PlexClient, baseURL, token, accountID string) *PlexAdminProvider {
	return &PlexAdminProvider{
		client:    client,
		baseURL:   baseURL,
		token:     token,
		accountID: accountID,
	}
}

// Fetch satisfies the Provider interface. It fetches all history entries for the
// configured account, enriches episodes with series metadata, and returns normalized Records.
func (p *PlexAdminProvider) Fetch(ctx context.Context) ([]Record, []string, error) {
	items, err := p.client.FetchUserHistory(ctx, p.baseURL, p.token, p.accountID)
	if err != nil {
		return nil, nil, err
	}

	var warnings []string

	// Enrich episodes with series-level metadata (for external IDs on the series).
	seriesMeta := p.fetchSeriesMetadata(ctx, items, &warnings)

	// Normalize history items to Records, then deduplicate by rating key.
	merged := make(map[string]Record, len(items))
	for _, item := range items {
		record := NormalizePlexHistoryItem(item, seriesMeta[item.GrandparentRatingKey])
		if record.ExternalID == "" {
			continue
		}
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
	return records, warnings, nil
}

// fetchSeriesMetadata fetches metadata for all unique series referenced by episode items.
func (p *PlexAdminProvider) fetchSeriesMetadata(ctx context.Context, items []PlexHistoryItem, warnings *[]string) map[string]*PlexItem {
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
			slog.WarnContext(ctx, "plex admin history import: failed to fetch series metadata", "component", "historyimport", "rating_key", key, "error", err)
			*warnings = append(*warnings, fmt.Sprintf("failed to fetch series metadata for %s: %v", key, err))
			continue
		}
		if meta != nil {
			result[key] = meta
		}
	}
	return result
}
