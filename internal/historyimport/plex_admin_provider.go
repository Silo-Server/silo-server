package historyimport

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
// Rating keys are fetched once each, in batches, because one item can appear many
// times in the raw history. Individual failures stay best-effort so exact title/year
// matching can still run; only context cancellation aborts the sweep.
func (p *PlexAdminProvider) fetchItemMetadata(
	ctx context.Context,
	items []PlexHistoryItem,
	seriesMeta map[string]*PlexItem,
	warnings *[]string,
) (map[string]*PlexItem, error) {
	// A rating key is resolved when any of its history entries already carries usable
	// ids (Plex is inconsistent about including Guid on history rows for one item).
	kinds := make(map[string]string)
	resolved := make(map[string]struct{})
	for _, item := range items {
		if item.RatingKey == "" || (item.Type != KindMovie && item.Type != KindEpisode) {
			continue
		}
		kinds[item.RatingKey] = item.Type
		if hasMatchablePlexGuidForKind(item.Guid, item.Type) || hasMatchableSeriesFallback(item, seriesMeta) {
			resolved[item.RatingKey] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(kinds))
	var pending []string
	for _, item := range items {
		if _, ok := kinds[item.RatingKey]; !ok {
			continue
		}
		if _, ok := resolved[item.RatingKey]; ok {
			continue
		}
		if _, ok := seen[item.RatingKey]; ok {
			continue
		}
		seen[item.RatingKey] = struct{}{}
		pending = append(pending, item.RatingKey)
	}

	result := make(map[string]*PlexItem, len(pending))
	var firstErr error
	noteErr := func(err error, keys []string) {
		slog.WarnContext(ctx, "plex admin history import: failed to fetch item metadata",
			"component", "historyimport", "rating_keys", keys, "error", err)
		if firstErr == nil {
			firstErr = err
		}
	}

	for start := 0; start < len(pending); start += plexMetadataBatchSize {
		batch := pending[start:min(start+plexMetadataBatchSize, len(pending))]
		metas, err := p.client.FetchMetadataBatch(ctx, p.baseURL, p.token, batch)
		if err == nil {
			for i := range metas {
				result[metas[i].RatingKey] = &metas[i]
			}
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if len(batch) == 1 {
			noteErr(err, batch)
			continue
		}
		// Plex can return 404 for a whole batch when only one key was deleted.
		// Retry that case per key, but do not multiply systematic failures such as
		// authentication errors, outages, or timeouts into one request per item.
		noteErr(err, batch)
		if !isPlexHTTPStatus(err, http.StatusNotFound) {
			continue
		}
		for _, key := range batch {
			meta, err := p.client.FetchMetadata(ctx, p.baseURL, p.token, key)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				noteErr(err, []string{key})
				continue
			}
			if meta != nil {
				result[key] = meta
			}
		}
	}

	unresolved := 0
	for _, key := range pending {
		meta, ok := result[key]
		if !ok || !hasMatchablePlexGuidForKind(meta.Guid, kinds[key]) {
			unresolved++
		}
	}
	if unresolved > 0 {
		*warnings = append(*warnings, plexUnresolvedIDsWarning(
			"plex admin history", "unique items", unresolved, len(pending), firstErr))
	}
	return result, nil
}

func enrichPlexHistoryItem(item PlexHistoryItem, meta *PlexItem) PlexHistoryItem {
	item.Guid, item.Year = applyPlexMetadataFallback(item.Guid, item.Year, item.Type, meta)
	return item
}

func hasMatchableSeriesFallback(item PlexHistoryItem, seriesMeta map[string]*PlexItem) bool {
	if item.Type != KindEpisode || item.Index <= 0 {
		return false
	}
	series := seriesMeta[item.GrandparentRatingKey]
	return series != nil && hasMatchablePlexGuidForKind(series.Guid, KindSeries)
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
