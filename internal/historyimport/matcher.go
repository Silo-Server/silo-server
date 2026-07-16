package historyimport

import (
	"context"
	"fmt"
	"strings"
)

type matcherRepository interface {
	MatchMediaByExternalID(ctx context.Context, kind, column, value string) ([]mediaLookupRow, error)
	MatchMediaByTitleYear(ctx context.Context, kind, title string, year int) ([]mediaLookupRow, error)
	MatchEpisodeByExternalID(ctx context.Context, column, value string) ([]mediaLookupRow, error)
	MatchEpisodeBySeries(ctx context.Context, seriesID string, seasonNumber, episodeNumber int) (*Match, error)
}

type Matcher struct {
	repo matcherRepository
}

func NewMatcher(repo matcherRepository) *Matcher {
	return &Matcher{repo: repo}
}

func (m *Matcher) Match(ctx context.Context, record Record) (*Match, string, error) {
	switch record.Kind {
	case KindMovie:
		return m.matchMovie(ctx, record)
	case KindEpisode:
		return m.matchEpisode(ctx, record)
	case KindSeries:
		return m.matchSeries(ctx, record)
	default:
		return nil, "unsupported item kind", nil
	}
}

func (m *Matcher) matchMovie(ctx context.Context, record Record) (*Match, string, error) {
	match, reason, err := m.matchMedia(ctx, KindMovie, record.TMDBID, record.IMDbID, "", record.Title, record.Year, false)
	if err != nil {
		return nil, "", err
	}
	return match, reason, nil
}

func (m *Matcher) matchSeries(ctx context.Context, record Record) (*Match, string, error) {
	match, reason, err := m.matchMedia(ctx, KindSeries, record.TMDBID, record.IMDbID, record.TVDBID, record.Title, record.Year, record.PreferTMDB)
	if err != nil {
		return nil, "", err
	}
	return match, reason, nil
}

func (m *Matcher) matchEpisode(ctx context.Context, record Record) (*Match, string, error) {
	attempts := make([]string, 0, 4)
	for _, candidate := range []struct {
		column string
		value  string
		label  string
	}{
		{column: "tvdb_id", value: record.TVDBID, label: "tvdb_id"},
		{column: "tmdb_id", value: record.TMDBID, label: "tmdb_id"},
		{column: "imdb_id", value: record.IMDbID, label: "imdb_id"},
	} {
		if candidate.value == "" {
			continue
		}
		rows, err := m.repo.MatchEpisodeByExternalID(ctx, candidate.column, candidate.value)
		if err != nil {
			return nil, "", err
		}
		if len(rows) == 1 {
			return &Match{
				MediaItemID: rows[0].ContentID,
				Kind:        KindEpisode,
				Title:       rows[0].Title,
				Year:        rows[0].Year,
			}, "", nil
		}
		if len(rows) > 1 {
			return nil, fmt.Sprintf("ambiguous episode %s match for %q (%d rows)", candidate.label, candidate.value, len(rows)), nil
		}
		attempts = append(attempts, fmt.Sprintf("no episode %s match for %q", candidate.label, candidate.value))
	}

	if record.EpisodeNumber <= 0 {
		attempts = append(attempts, "missing season or episode number")
		return nil, strings.Join(attempts, "; "), nil
	}

	seriesRecord := Record{
		Kind:   KindSeries,
		Title:  record.SeriesTitle,
		Year:   record.SeriesYear,
		TMDBID: record.SeriesTMDBID,
		IMDbID: record.SeriesIMDbID,
		TVDBID: record.SeriesTVDBID,
	}
	seriesMatch, reason, err := m.matchSeries(ctx, seriesRecord)
	if err != nil {
		return nil, "", err
	}
	if seriesMatch == nil {
		attempts = append(attempts, "series match failed: "+reason)
		return nil, strings.Join(attempts, "; "), nil
	}

	match, err := m.repo.MatchEpisodeBySeries(ctx, seriesMatch.MediaItemID, record.SeasonNumber, record.EpisodeNumber)
	if err != nil {
		return nil, "", err
	}
	if match == nil {
		attempts = append(attempts, fmt.Sprintf("no matching episode for S%02dE%02d", record.SeasonNumber, record.EpisodeNumber))
		return nil, strings.Join(attempts, "; "), nil
	}
	return match, "", nil
}

func (m *Matcher) matchMedia(ctx context.Context, kind, tmdbID, imdbID, tvdbID, title string, year int, preferTMDB bool) (*Match, string, error) {
	attempts := make([]string, 0, 4)
	candidates := []struct {
		column string
		value  string
		label  string
	}{
		{column: "tmdb_id", value: tmdbID, label: "tmdb_id"},
		{column: "imdb_id", value: imdbID, label: "imdb_id"},
	}
	if kind == KindSeries {
		candidates = []struct {
			column string
			value  string
			label  string
		}{
			{column: "tvdb_id", value: tvdbID, label: "tvdb_id"},
			{column: "tmdb_id", value: tmdbID, label: "tmdb_id"},
			{column: "imdb_id", value: imdbID, label: "imdb_id"},
		}
		if preferTMDB {
			candidates[0], candidates[1] = candidates[1], candidates[0]
		}
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		rows, err := m.repo.MatchMediaByExternalID(ctx, kind, candidate.column, candidate.value)
		if err != nil {
			return nil, "", err
		}
		if len(rows) == 1 {
			return &Match{
				MediaItemID: rows[0].ContentID,
				Kind:        kind,
				Title:       rows[0].Title,
				Year:        rows[0].Year,
			}, "", nil
		}
		if len(rows) > 1 {
			return nil, fmt.Sprintf("ambiguous %s match for %q (%d rows)", candidate.label, candidate.value, len(rows)), nil
		}
		attempts = append(attempts, fmt.Sprintf("no %s match for %q", candidate.label, candidate.value))
	}

	if title == "" {
		if len(attempts) == 0 {
			return nil, "missing identifiers and title", nil
		}
		return nil, strings.Join(attempts, "; "), nil
	}

	rows, err := m.repo.MatchMediaByTitleYear(ctx, kind, title, year)
	if err != nil {
		return nil, "", err
	}
	if len(rows) == 1 {
		return &Match{
			MediaItemID: rows[0].ContentID,
			Kind:        kind,
			Title:       rows[0].Title,
			Year:        rows[0].Year,
		}, "", nil
	}
	if len(rows) > 1 {
		return nil, fmt.Sprintf("ambiguous exact title/year match for %s (%d rows)", describeTitleYear(title, year), len(rows)), nil
	}
	attempts = append(attempts, fmt.Sprintf("no exact title/year match for %s", describeTitleYear(title, year)))
	return nil, strings.Join(attempts, "; "), nil
}

func describeTitleYear(title string, year int) string {
	if year > 0 {
		return fmt.Sprintf("%q (%d)", title, year)
	}
	return fmt.Sprintf("%q", title)
}
