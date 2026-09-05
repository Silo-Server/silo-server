package historyimport

import (
	"fmt"
	"strings"
	"time"
)

func NormalizePlexItem(item PlexItem, series *PlexItem) Record {
	record := Record{
		ExternalID:      item.RatingKey,
		Title:           item.Title,
		Year:            item.Year,
		Played:          item.ViewCount > 0,
		PlayCount:       item.ViewCount,
		PositionSeconds: float64(item.ViewOffset) / 1000,
		DurationSeconds: float64(item.Duration) / 1000,
		UpdatedAt:       time.Now().UTC(),
	}

	if item.LastViewedAt > 0 {
		t := time.Unix(item.LastViewedAt, 0).UTC()
		record.LastPlayedAt = &t
		record.UpdatedAt = t
	}

	ParsePlexGuids(item.Guid, &record.IMDbID, &record.TMDBID, &record.TVDBID)

	switch item.Type {
	case "movie":
		record.Kind = KindMovie
	case "episode":
		record.Kind = KindEpisode
		record.SeriesTitle = item.GrandparentTitle
		record.SeasonNumber = item.ParentIndex
		record.EpisodeNumber = item.Index
		if series != nil {
			ParsePlexGuids(series.Guid, &record.SeriesIMDbID, &record.SeriesTMDBID, &record.SeriesTVDBID)
			record.SeriesYear = series.Year
			if record.SeriesTitle == "" {
				record.SeriesTitle = series.Title
			}
		}
	default:
		record.Kind = item.Type
	}

	return record
}

// NormalizePlexWatchlistItem maps an account-watchlist entry to an import
// record: movie or series identity only, flagged Watchlisted, and carrying
// no watch state (a watchlist entry says "want to watch", not "watched").
func NormalizePlexWatchlistItem(item PlexItem) Record {
	record := Record{
		ExternalID:  item.RatingKey,
		Title:       item.Title,
		Year:        item.Year,
		Watchlisted: true,
		UpdatedAt:   time.Now().UTC(),
	}
	ParsePlexGuids(item.Guid, &record.IMDbID, &record.TMDBID, &record.TVDBID)
	switch item.Type {
	case "movie":
		record.Kind = KindMovie
	case "show":
		record.Kind = KindSeries
	default:
		record.Kind = item.Type
	}
	return record
}

func NormalizePlexHistoryItem(item PlexHistoryItem, series *PlexItem) Record {
	record := Record{
		ExternalID:      item.RatingKey,
		Title:           item.Title,
		Year:            item.Year,
		Played:          true,
		PlayCount:       1,
		DurationSeconds: float64(item.Duration) / 1000,
		UpdatedAt:       time.Now().UTC(),
	}

	if item.ViewedAt > 0 {
		t := time.Unix(item.ViewedAt, 0).UTC()
		record.LastPlayedAt = &t
		record.UpdatedAt = t
	}

	ParsePlexGuids(item.Guid, &record.IMDbID, &record.TMDBID, &record.TVDBID)

	switch item.Type {
	case "movie":
		record.Kind = KindMovie
	case "episode":
		record.Kind = KindEpisode
		record.SeriesTitle = item.GrandparentTitle
		record.SeasonNumber = item.ParentIndex
		record.EpisodeNumber = item.Index
		if series != nil {
			ParsePlexGuids(series.Guid, &record.SeriesIMDbID, &record.SeriesTMDBID, &record.SeriesTVDBID)
			record.SeriesYear = series.Year
			if record.SeriesTitle == "" {
				record.SeriesTitle = series.Title
			}
		}
	default:
		record.Kind = item.Type
	}

	return record
}

func ParsePlexGuids(guids PlexGuids, imdbID, tmdbID, tvdbID *string) {
	for _, g := range guids {
		provider, value, ok := strings.Cut(g.ID, "://")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(provider) {
		case "imdb":
			if *imdbID == "" {
				*imdbID = value
			}
		case "tmdb":
			if *tmdbID == "" {
				*tmdbID = value
			}
		case "tvdb":
			if *tvdbID == "" {
				*tvdbID = value
			}
		}
	}
}

func hasMatchablePlexGuidForKind(guids PlexGuids, kind string) bool {
	var imdbID, tmdbID, tvdbID string
	ParsePlexGuids(guids, &imdbID, &tmdbID, &tvdbID)
	if kind == KindMovie {
		return imdbID != "" || tmdbID != ""
	}
	return imdbID != "" || tmdbID != "" || tvdbID != ""
}

// applyPlexMetadataFallback fills provider ids and year that a listing omitted from a
// full metadata record. Ids are only overlaid when the listing carries none the matcher
// can use for kind, and only the providers still missing are added, so ids Plex did
// return on the listing always win.
func applyPlexMetadataFallback(guid PlexGuids, year int, kind string, meta *PlexItem) (PlexGuids, int) {
	if meta == nil {
		return guid, year
	}
	if !hasMatchablePlexGuidForKind(guid, kind) && hasMatchablePlexGuidForKind(meta.Guid, kind) {
		guid = overlayMissingPlexGuids(guid, meta.Guid)
	}
	if year == 0 {
		year = meta.Year
	}
	return guid, year
}

func overlayMissingPlexGuids(existing, metadata PlexGuids) PlexGuids {
	var existingIMDbID, existingTMDBID, existingTVDBID string
	ParsePlexGuids(existing, &existingIMDbID, &existingTMDBID, &existingTVDBID)
	var metadataIMDbID, metadataTMDBID, metadataTVDBID string
	ParsePlexGuids(metadata, &metadataIMDbID, &metadataTMDBID, &metadataTVDBID)

	result := append(PlexGuids(nil), existing...)
	if existingIMDbID == "" && metadataIMDbID != "" {
		result = append(result, PlexGuid{ID: "imdb://" + metadataIMDbID})
	}
	if existingTMDBID == "" && metadataTMDBID != "" {
		result = append(result, PlexGuid{ID: "tmdb://" + metadataTMDBID})
	}
	if existingTVDBID == "" && metadataTVDBID != "" {
		result = append(result, PlexGuid{ID: "tvdb://" + metadataTVDBID})
	}
	return result
}

// plexUnresolvedIDsWarning is the shared wording for a best-effort id sweep that left
// some items on title/year matching. firstErr, when set, names the first upstream
// failure so a systematic cause (auth, wrong URL) is visible in the run summary.
func plexUnresolvedIDsWarning(scope, noun string, unresolved, attempted int, firstErr error) string {
	msg := fmt.Sprintf("%s: could not resolve external ids for %d of %d %s; those fall back to exact title/year matching",
		scope, unresolved, attempted, noun)
	if firstErr != nil {
		msg += fmt.Sprintf(" (first error: %v)", firstErr)
	}
	return msg
}
