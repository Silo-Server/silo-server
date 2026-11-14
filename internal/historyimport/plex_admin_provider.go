package historyimport

import (
	"context"
	"hash/fnv"
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

const plexStageHistory = "history"

// Stream reads the account's history, requested newest play first. An item
// appears once per play; only its newest play is imported, which the seen set
// preserves across pages. It keeps each item's newest time rather than trusting
// the sort, so a server that ignores it still imports the newest play.
func (p *PlexAdminProvider) Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error {
	series := newPlexSeriesLookup(p.client, p.baseURL, p.token)
	var ids plexItemIDLookup
	seen := make(map[uint64]int64)
	// A resume reads the pages before its checkpoint again, without applying
	// them, because they say which items already imported their newest play.
	resumeAt := stageOffset(from, plexStageHistory)
	err := p.client.forEachUserHistoryPage(ctx, p.baseURL, p.token, p.accountID, 0, func(page []PlexHistoryItem, start int) error {
		if start < resumeAt {
			markSeenHistory(page, seen)
			return nil
		}
		records, err := p.normalizePage(ctx, page, series, &ids, seen)
		if err != nil {
			return err
		}
		return sink(RecordBatch{Records: records, Resume: StreamPosition{Stage: plexStageHistory, Offset: start}})
	})
	if err != nil {
		return err
	}
	var warnings []string
	if warning := series.warning(); warning != "" {
		warnings = append(warnings, warning)
	}
	if ids.unresolved > 0 {
		warnings = append(warnings, plexUnresolvedIDsWarning(plexAdminHistoryIDScope, "unique items", ids.unresolved, ids.asked, ids.firstErr, ids.aborted))
	}
	if len(warnings) == 0 {
		return nil
	}
	return sink(RecordBatch{Warnings: warnings})
}

// historySeenKey hashes a rating key, so the seen set costs the same for any
// key length.
func historySeenKey(ratingKey string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(ratingKey))
	return hash.Sum64()
}

// keepNewestPlay records item as seen and reports whether it is newer than any
// play of the same item seen so far.
func keepNewestPlay(item PlexHistoryItem, seen map[uint64]int64) bool {
	key := historySeenKey(item.RatingKey)
	newest, ok := seen[key]
	if ok && item.ViewedAt <= newest {
		return false
	}
	if ok || len(seen) < maxSeenHistoryItems {
		seen[key] = item.ViewedAt
	}
	return true
}

func markSeenHistory(page []PlexHistoryItem, seen map[uint64]int64) {
	for _, item := range page {
		if item.RatingKey != "" {
			keepNewestPlay(item, seen)
		}
	}
}

func (p *PlexAdminProvider) normalizePage(ctx context.Context, rows []PlexHistoryItem, series *plexSeriesLookup, ids *plexItemIDLookup, seen map[uint64]int64) ([]Record, error) {
	// Plex is inconsistent about including Guid on history rows for one item,
	// so a row borrows ids from another row of the item on the page.
	guids := make(map[string]PlexGuids, len(rows))
	for _, item := range rows {
		if hasMatchablePlexGuid(item.Guid) {
			guids[item.RatingKey] = item.Guid
		}
	}
	page := make([]PlexHistoryItem, 0, len(rows))
	for _, item := range rows {
		if item.RatingKey == "" || !keepNewestPlay(item, seen) {
			continue
		}
		if borrowed, found := guids[item.RatingKey]; found && !hasMatchablePlexGuid(item.Guid) {
			item.Guid = borrowed
		}
		page = append(page, item)
	}

	// Enrich episodes with series-level metadata (for external IDs on the series).
	seriesKeys := make([]string, 0, len(page))
	for _, item := range page {
		if item.Type == KindEpisode {
			seriesKeys = append(seriesKeys, item.GrandparentRatingKey)
		}
	}
	seriesMeta, err := series.resolve(ctx, seriesKeys)
	if err != nil {
		return nil, err
	}
	itemMeta, err := p.fetchItemMetadata(ctx, page, seriesMeta, ids)
	if err != nil {
		return nil, err
	}
	records := make([]Record, 0, len(page))
	for _, item := range page {
		item = enrichPlexHistoryItem(item, itemMeta[item.RatingKey])
		records = append(records, NormalizePlexHistoryItem(item, seriesMeta[item.GrandparentRatingKey]))
	}
	return records, nil
}

// plexItemIDLookup totals a run's per-item id lookups for one warning.
type plexItemIDLookup struct {
	asked, unresolved int
	firstErr          error
	aborted           bool
	failureStreak     int
}

// fetchItemMetadata resolves external provider IDs omitted by Plex's session-history
// endpoint. Only movies and episodes are looked up: the matcher rejects every other
// kind, so fetching metadata for music tracks or clips would be wasted requests.
// Episodes whose series already carries ids match on series ids plus season/episode
// numbers and are skipped. After a sweep gives up, later pages stop asking.
func (p *PlexAdminProvider) fetchItemMetadata(
	ctx context.Context,
	items []PlexHistoryItem,
	seriesMeta map[string]*PlexItem,
	ids *plexItemIDLookup,
) (map[string]*PlexItem, error) {
	var pending []string
	for _, item := range items {
		if item.Type != KindMovie && item.Type != KindEpisode {
			continue
		}
		if !hasMatchablePlexGuid(item.Guid) && !hasMatchableSeriesFallback(item, seriesMeta) {
			pending = append(pending, item.RatingKey)
		}
	}
	pending = uniqueNonEmpty(pending)
	if len(pending) == 0 {
		return nil, nil
	}
	ids.asked += len(pending)
	if ids.aborted {
		ids.unresolved += len(pending)
		return nil, nil
	}
	sweep, err := p.client.fetchMetadataByKey(ctx, p.baseURL, p.token, pending, &ids.failureStreak)
	if err != nil {
		return nil, err
	}
	for _, key := range pending {
		meta, ok := sweep.items[key]
		if !ok || !hasMatchablePlexGuid(meta.Guid) {
			ids.unresolved++
		}
	}
	if ids.firstErr == nil {
		ids.firstErr = sweep.firstErr
	}
	ids.aborted = ids.aborted || sweep.aborted || ids.failureStreak >= plexMetadataFailureStreakLimit
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
