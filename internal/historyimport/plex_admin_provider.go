package historyimport

import (
	"context"
)

// PlexAdminProvider fetches watch history for a specific Plex account using an admin token.
// It uses the PMS session history API (GET /status/sessions/history/all?accountID=X)
// rather than the per-user library endpoints used by PlexServerProvider.
//
// Session history only records playback that reached the watched threshold. Titles
// marked watched without playing and resume positions are per-user library state
// the admin token cannot read, so this provider never sees them.
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
	var seriesKeys []string
	for _, item := range items {
		if item.Type == KindEpisode {
			seriesKeys = append(seriesKeys, item.GrandparentRatingKey)
		}
	}
	seriesMeta, err := fetchPlexSeriesMetadata(ctx, p.client, p.baseURL, p.token, seriesKeys, &warnings)
	if err != nil {
		return nil, warnings, err
	}
	itemMeta, err := p.fetchItemMetadata(ctx, items, seriesMeta, &warnings)
	if err != nil {
		return nil, warnings, err
	}

	// Normalize history items to Records, then deduplicate by rating key.
	merged := make(map[string]Record, len(items))
	for _, item := range items {
		item = enrichPlexHistoryItem(item, itemMeta[item.RatingKey])
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

// fetchItemMetadata resolves external provider IDs omitted by Plex's session-history
// endpoint. Only movies and episodes are looked up: the matcher rejects every other
// kind, so fetching metadata for music tracks or clips would be wasted requests.
// Episodes whose series already carries ids match on series ids plus season/episode
// numbers and are skipped. Rating keys are fetched once each, in batches, because
// one item can appear many times in the raw history.
func (p *PlexAdminProvider) fetchItemMetadata(
	ctx context.Context,
	items []PlexHistoryItem,
	seriesMeta map[string]*PlexItem,
	warnings *[]string,
) (map[string]*PlexItem, error) {
	// A rating key is resolved when any of its history entries already carries usable
	// ids (Plex is inconsistent about including Guid on history rows for one item).
	eligible := make(map[string]struct{})
	resolved := make(map[string]struct{})
	for _, item := range items {
		if item.RatingKey == "" || (item.Type != KindMovie && item.Type != KindEpisode) {
			continue
		}
		eligible[item.RatingKey] = struct{}{}
		if hasMatchablePlexGuid(item.Guid) || hasMatchableSeriesFallback(item, seriesMeta) {
			resolved[item.RatingKey] = struct{}{}
		}
	}
	var pending []string
	for _, item := range items {
		if _, ok := eligible[item.RatingKey]; !ok {
			continue
		}
		if _, ok := resolved[item.RatingKey]; ok {
			continue
		}
		pending = append(pending, item.RatingKey)
	}
	pending = uniqueNonEmpty(pending)

	sweep, err := p.client.fetchMetadataByKey(ctx, p.baseURL, p.token, pending)
	if err != nil {
		return nil, err
	}
	unresolved := 0
	for _, key := range pending {
		meta, ok := sweep.items[key]
		if !ok || !hasMatchablePlexGuid(meta.Guid) {
			unresolved++
		}
	}
	if unresolved > 0 {
		*warnings = append(*warnings, plexUnresolvedIDsWarning(
			"plex admin history", "unique items", unresolved, len(pending), sweep.firstErr, sweep.aborted))
	}
	return sweep.items, nil
}

// enrichPlexHistoryItem fills what a sparse history row omits from the item's full
// metadata, when that was fetched.
func enrichPlexHistoryItem(item PlexHistoryItem, meta *PlexItem) PlexHistoryItem {
	item.Guid, item.Year = applyPlexMetadataFallback(item.Guid, item.Year, meta)
	if meta != nil && item.Duration == 0 {
		item.Duration = meta.Duration
	}
	return item
}

func hasMatchableSeriesFallback(item PlexHistoryItem, seriesMeta map[string]*PlexItem) bool {
	if item.Type != KindEpisode || item.Index <= 0 {
		return false
	}
	series := seriesMeta[item.GrandparentRatingKey]
	return series != nil && hasMatchablePlexGuid(series.Guid)
}
