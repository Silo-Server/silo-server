package historyimport

import (
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
