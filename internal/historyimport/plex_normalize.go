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
	}

	// Without a source timestamp UpdatedAt stays zero, which the import treats
	// as older than any local activity instead of overwriting newer progress.
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

// plexLegacyAgentProviders maps the pre-2020 Plex metadata agents, which
// long-lived libraries still use, to their id providers. Their items carry no
// Guid array, only one guid such as "com.plexapp.agents.imdb://tt0133093?lang=en".
var plexLegacyAgentProviders = map[string]string{
	"com.plexapp.agents.imdb":       plexGuidIMDb,
	"com.plexapp.agents.themoviedb": plexGuidTMDB,
	"com.plexapp.agents.thetvdb":    plexGuidTVDB,
}

// Plex Guid provider prefixes, as in "tmdb://603".
const (
	plexGuidIMDb = "imdb"
	plexGuidTMDB = "tmdb"
	plexGuidTVDB = "tvdb"
)

func ParsePlexGuids(guids PlexGuids, imdbID, tmdbID, tvdbID *string) {
	for _, g := range guids {
		provider, value, ok := strings.Cut(g.ID, "://")
		if !ok {
			continue
		}
		provider = strings.ToLower(provider)
		if legacy, ok := plexLegacyAgentProviders[provider]; ok {
			provider = legacy
			value, _, _ = strings.Cut(value, "?")
			// Legacy episode guids read "{series}/{season}/{episode}": the id names
			// the series, so it cannot identify the episode itself.
			if strings.Contains(value, "/") {
				continue
			}
		}
		value = strings.TrimSpace(value)
		switch provider {
		case plexGuidIMDb:
			if *imdbID == "" {
				*imdbID = value
			}
		case plexGuidTMDB:
			if *tmdbID == "" {
				*tmdbID = value
			}
		case plexGuidTVDB:
			if *tvdbID == "" {
				*tvdbID = value
			}
		}
	}
}

func hasMatchablePlexGuid(guids PlexGuids) bool {
	var imdbID, tmdbID, tvdbID string
	ParsePlexGuids(guids, &imdbID, &tmdbID, &tvdbID)
	return imdbID != "" || tmdbID != "" || tvdbID != ""
}

// applyPlexMetadataFallback fills provider ids and year that a listing omitted from a
// full metadata record. Ids are only overlaid when the listing carries none the matcher
// can use, and only the providers still missing are added, so ids Plex did
// return on the listing always win.
func applyPlexMetadataFallback(guid PlexGuids, year int, meta *PlexItem) (PlexGuids, int) {
	if meta == nil {
		return guid, year
	}
	if !hasMatchablePlexGuid(guid) && hasMatchablePlexGuid(meta.Guid) {
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
// some items without a matchable identity. firstErr, when set, names the first upstream
// failure so a systematic cause (auth, wrong URL) is visible in the run summary, and
// aborted says the sweep stopped early rather than asking about every item.
func plexUnresolvedIDsWarning(scope, noun string, unresolved, attempted int, firstErr error, aborted bool) string {
	msg := fmt.Sprintf("%s: could not resolve external ids for %d of %d %s; those items will remain unmatched%s",
		scope, unresolved, attempted, noun, plexSweepAbortedSuffix(aborted))
	if firstErr != nil {
		msg += fmt.Sprintf(" (first error: %v)", firstErr)
	}
	return msg
}

// plexSweepAbortedSuffix names the early stop in a run warning so the reader knows
// the unresolved count came from giving up, not from Plex answering about every item.
func plexSweepAbortedSuffix(aborted bool) string {
	if !aborted {
		return ""
	}
	return ", and the lookup stopped early after repeated server errors"
}
