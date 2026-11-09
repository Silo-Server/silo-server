package historyimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

type JellyfinProvider struct {
	client *JellyfinClient
	auth   jellyfinLocalAuth
	// playedSince, when set, limits the played-history read to items last
	// played at or after it. Resume positions and favorites are still read in
	// full: both are short lists, and neither carries a play date to filter on.
	playedSince *time.Time
}

func NewJellyfinProvider(client *JellyfinClient, auth jellyfinLocalAuth) *JellyfinProvider {
	return &JellyfinProvider{client: client, auth: auth}
}

// WithPlayedSince limits the played-history read to items last played at or
// after since. A nil since keeps the full read.
func (p *JellyfinProvider) WithPlayedSince(since *time.Time) *JellyfinProvider {
	p.playedSince = since
	return p
}

// Stages of a Jellyfin listing, in order. A lookback restarts from its first
// page on resume: it is short, and its order checks need the whole read.
const (
	jellyfinStageLookback  = "played-since"
	jellyfinStagePlayed    = "played"
	jellyfinStageRemainder = "resumable"
	jellyfinStageFavorites = "favorites"
)

var jellyfinStages = []string{jellyfinStageLookback, jellyfinStagePlayed, jellyfinStageRemainder, jellyfinStageFavorites}

// Stream reads played and resumable movies and episodes plus favorite movies,
// shows, and episodes. Jellyfin records a whole-show or whole-season "mark
// played" on each episode, so episode leaves carry those markers too. The
// resumable list is held and merged into the played pages; favorites follow on
// their own, because a favorite only adds to favorites.
func (p *JellyfinProvider) Stream(ctx context.Context, from *StreamPosition, sink RecordSink) error {
	sink = markSinkErrors(sink)
	// Resume positions are secondary to the played history: a slow or failing
	// resume query must not discard it. Cancellation still ends the run, and so
	// does a source over the size limits. Warnings store fixed text: v1 returns
	// them verbatim, and upstream errors can carry the server's response body.
	resumable, err := p.client.FetchResumableItems(ctx, p.auth)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrUpstreamResponseTooLarge) {
			return err
		}
		slog.WarnContext(ctx, "jellyfin history import: resume positions unavailable", "component", "historyimport", "error", jellyfinWarningLogError(err))
		if err := sink(RecordBatch{Warnings: []string{warnJellyfinResumeUnavailable}}); err != nil {
			return err
		}
		resumable = nil
	}
	held, err := holdItems(ctx, resumable, func(item jellyfinItem) string { return item.ID })
	if err != nil {
		return fmt.Errorf("holding Jellyfin resumable items: %w", err)
	}
	defer held.release(ctx)

	series := newLookupCache[jellyfinItem](seriesLookupCacheEntries)
	readFull := true
	if p.playedSince != nil && (from == nil || from.Stage == jellyfinStageLookback) {
		if readFull, err = p.streamLookback(ctx, held, series, sink); err != nil {
			return err
		}
	}
	if readFull && stageReached(from, jellyfinStages, jellyfinStagePlayed) {
		err := p.client.forEachItemsPage(ctx, p.auth, "IsPlayed", jellyfinPlayableItemTypes, stageOffset(from, jellyfinStagePlayed), func(page []jellyfinItem, start int) error {
			return p.emitWatched(ctx, page, held, series, StreamPosition{Stage: jellyfinStagePlayed, Offset: start}, sink)
		})
		if err != nil {
			return err
		}
	}
	if stageReached(from, jellyfinStages, jellyfinStageRemainder) {
		for page := range slices.Chunk(held.remaining(), jellyfinPageSize) {
			if err := p.emitWatched(ctx, page, nil, series, StreamPosition{Stage: jellyfinStageRemainder}, sink); err != nil {
				return err
			}
		}
	}
	return p.streamFavorites(ctx, from, series, sink)
}

// streamLookback reads the played-since window and reports whether the full
// played listing is still needed.
func (p *JellyfinProvider) streamLookback(ctx context.Context, held *heldItems[jellyfinItem], series *lookupCache[jellyfinItem], sink RecordSink) (bool, error) {
	sorted, err := p.client.streamPlayedSince(ctx, p.auth, *p.playedSince, func(page []jellyfinItem) error {
		return p.emitWatched(ctx, page, held, series, StreamPosition{Stage: jellyfinStageLookback}, sink)
	})
	if err != nil || sorted {
		return false, err
	}
	// The server ignored the newest-first sort, or the list kept moving during
	// the read, so the early stop could miss recent plays. Read the whole
	// played history instead.
	slog.WarnContext(ctx, "jellyfin history import: played items not newest first or kept moving; reading the full played history", "component", "historyimport")
	return true, sink(RecordBatch{Warnings: []string{warnJellyfinLookbackUnsorted}})
}

func (p *JellyfinProvider) emitWatched(ctx context.Context, page []jellyfinItem, held *heldItems[jellyfinItem], series *lookupCache[jellyfinItem], resume StreamPosition, sink RecordSink) error {
	meta, err := p.resolveSeries(ctx, page, series)
	if err != nil {
		return err
	}
	records := make([]Record, 0, len(page))
	for _, item := range page {
		record := normalizeJellyfinItem(item, meta[item.SeriesID])
		if held != nil {
			if progress, ok := held.take(item.ID); ok {
				record = mergeRecords(record, normalizeJellyfinItem(progress, meta[progress.SeriesID]))
			}
		}
		records = append(records, record)
	}
	return sink(RecordBatch{Records: records, Resume: resume})
}

func (p *JellyfinProvider) streamFavorites(ctx context.Context, from *StreamPosition, series *lookupCache[jellyfinItem], sink RecordSink) error {
	if !stageReached(from, jellyfinStages, jellyfinStageFavorites) {
		return nil
	}
	seriesFailed := false
	err := p.client.forEachItemsPage(ctx, p.auth, "IsFavorite", jellyfinFavoriteItemTypes, stageOffset(from, jellyfinStageFavorites), func(page []jellyfinItem, start int) error {
		var warnings []string
		meta := map[string]jellyfinItem{}
		if !seriesFailed {
			var err error
			meta, err = p.resolveSeries(ctx, page, series)
			switch {
			case errors.Is(err, ErrUpstreamResponseTooLarge) || ctx.Err() != nil:
				return err
			case err != nil:
				// Like the favorites query, a failure here cannot discard the
				// watch history. Favorite episodes keep only their own IDs.
				slog.WarnContext(ctx, "jellyfin history import: favorite series metadata unavailable", "component", "historyimport", "error", jellyfinWarningLogError(err))
				warnings = append(warnings, warnJellyfinFavoriteSeriesUnavailable)
				seriesFailed = true
				meta = map[string]jellyfinItem{}
			}
		}
		records := make([]Record, 0, len(page))
		for _, item := range page {
			record := normalizeJellyfinItem(item, meta[item.SeriesID])
			record.Favorite = true
			record.FavoriteOnly = true
			// A lookback may have skipped this item's play, so only a full read
			// has already counted it.
			record.RepeatOfWatch = p.playedSince == nil && (item.UserData.Played || item.UserData.PlaybackPositionTicks > 0)
			records = append(records, record)
		}
		return sink(RecordBatch{Records: records, Warnings: warnings, Resume: StreamPosition{Stage: jellyfinStageFavorites, Offset: start}})
	})
	if err != nil {
		if errors.Is(err, ErrUpstreamResponseTooLarge) || ctx.Err() != nil || isSinkError(err) {
			return err
		}
		slog.WarnContext(ctx, "jellyfin history import: favorites unavailable", "component", "historyimport", "error", jellyfinWarningLogError(err))
		return sink(RecordBatch{Warnings: []string{warnJellyfinFavoritesUnavailable}})
	}
	return nil
}

func jellyfinWarningLogError(err error) string {
	return warningLogError("jellyfin", err)
}

// warningLogError renders a non-fatal fetch error from source for the log. An
// HTTP error keeps only its status: its body is up to 2 KB of server text that
// can echo credentials in forms logredact.SanitizeText doesn't recognize, such
// as JSON-escaped quotes.
func warningLogError(source string, err error) string {
	if status := UpstreamHTTPStatus(err); status != 0 {
		return fmt.Sprintf("%s http %d", source, status)
	}
	return logredact.SanitizeText(logredact.SanitizeURLError(err).Error())
}

// resolveSeries returns the series of a page's episodes, through the cache.
func (p *JellyfinProvider) resolveSeries(ctx context.Context, items []jellyfinItem, cache *lookupCache[jellyfinItem]) (map[string]jellyfinItem, error) {
	result := map[string]jellyfinItem{}
	seen := map[string]struct{}{}
	ids := []string{}
	for _, item := range items {
		if !strings.EqualFold(item.Type, "episode") || strings.TrimSpace(item.SeriesID) == "" {
			continue
		}
		if _, ok := seen[item.SeriesID]; ok {
			continue
		}
		seen[item.SeriesID] = struct{}{}
		if series, ok := cache.get(item.SeriesID); ok {
			result[item.SeriesID] = series
		} else {
			ids = append(ids, item.SeriesID)
		}
	}
	if len(ids) == 0 {
		return result, nil
	}
	seriesItems, err := p.client.FetchItemsByIDs(ctx, p.auth, ids, "Series")
	if err != nil {
		return nil, err
	}
	for _, item := range seriesItems {
		cache.put(item.ID, item)
		result[item.ID] = item
	}
	return result, nil
}

func normalizeJellyfinItem(item jellyfinItem, series jellyfinItem) Record {
	// A missing LastPlayedDate leaves UpdatedAt zero, which the import treats
	// as older than any local activity instead of as "now".
	record := Record{ExternalID: item.ID, Title: item.Name, Year: item.ProductionYear, Played: item.UserData.Played, PlayCount: item.UserData.PlayCount, PositionSeconds: ticksToSeconds(item.UserData.PlaybackPositionTicks), DurationSeconds: ticksToSeconds(item.RunTimeTicks), Favorite: item.UserData.IsFavorite}
	if item.UserData.LastPlayedDate != nil {
		record.LastPlayedAt = item.UserData.LastPlayedDate
		record.UpdatedAt = item.UserData.LastPlayedDate.UTC()
	}
	record.IMDbID = providerID(item.ProviderIDs, "imdb")
	record.TMDBID = providerID(item.ProviderIDs, "tmdb")
	record.TVDBID = providerID(item.ProviderIDs, "tvdb")
	switch strings.ToLower(item.Type) {
	case "movie":
		record.Kind = KindMovie
	case "episode":
		record.Kind = KindEpisode
		record.SeriesTitle = item.SeriesName
		if record.SeriesTitle == "" {
			record.SeriesTitle = series.Name
		}
		record.SeriesIMDbID = providerID(series.ProviderIDs, "imdb")
		record.SeriesTMDBID = providerID(series.ProviderIDs, "tmdb")
		record.SeriesTVDBID = providerID(series.ProviderIDs, "tvdb")
		record.SeriesYear = series.ProductionYear
		record.SeasonNumber = item.ParentIndexNumber
		record.EpisodeNumber = item.IndexNumber
	default:
		record.Kind = strings.ToLower(item.Type)
	}
	return record
}
