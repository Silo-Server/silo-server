package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CalendarEvent represents a single calendar entry — a movie release,
// episode air date, or season premiere.
type CalendarEvent struct {
	ContentID       string
	Type            string // "movie" | "episode" | "season_premiere"
	Title           string // movie title or series title
	EpisodeTitle    *string
	SeriesID        *string
	SeasonNumber    *int
	EpisodeNumber   *int
	AirDate         time.Time
	AirTime         *string
	AirTimezone     *string
	PosterPath      string
	PosterThumbhash string
	IsPremiere      bool
	IsFinale        bool
}

// CalendarFilter holds the parameters for a calendar query.
type CalendarFilter struct {
	Start              time.Time
	End                time.Time
	Filter             string // "all" | "favorites" | "watchlist"
	LibraryID          *int
	AllowedLibraryIDs  []int
	DisabledLibraryIDs []int
	MaxContentRating   string
	UserID             int
	ProfileID          string
}

// CalendarRepository provides calendar queries across movies, episodes, and seasons.
type CalendarRepository struct {
	pool *pgxpool.Pool
}

const calendarEventsOrderByClause = `ORDER BY air_date ASC, air_time ASC NULLS LAST, title ASC, season_number ASC NULLS FIRST, episode_number ASC NULLS FIRST`

// NewCalendarRepository creates a new CalendarRepository.
func NewCalendarRepository(pool *pgxpool.Pool) *CalendarRepository {
	return &CalendarRepository{pool: pool}
}

// ListEvents returns all calendar events within the given date range,
// ordered by air_date ascending.
func (r *CalendarRepository) ListEvents(ctx context.Context, f CalendarFilter) ([]CalendarEvent, error) {
	query, args := r.buildListEventsQuery(f)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("calendar query: %w", err)
	}
	defer rows.Close()

	var events []CalendarEvent
	for rows.Next() {
		var ev CalendarEvent
		var episodeTitle, seriesID *string
		var seasonNum, episodeNum *int
		if err := rows.Scan(
			&ev.ContentID, &ev.Type, &ev.Title, &episodeTitle, &seriesID,
			&seasonNum, &episodeNum, &ev.AirDate, &ev.AirTime, &ev.AirTimezone,
			&ev.PosterPath, &ev.PosterThumbhash,
			&ev.IsPremiere, &ev.IsFinale,
		); err != nil {
			return nil, fmt.Errorf("scanning calendar row: %w", err)
		}
		ev.EpisodeTitle = episodeTitle
		ev.SeriesID = seriesID
		ev.SeasonNumber = seasonNum
		ev.EpisodeNumber = episodeNum
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating calendar rows: %w", err)
	}

	return events, nil
}

func (r *CalendarRepository) buildListEventsQuery(f CalendarFilter) (string, []any) {
	var args []any
	argIdx := 1

	// Shared date range args.
	startArg := argIdx
	args = append(args, f.Start)
	argIdx++
	endArg := argIdx
	args = append(args, f.End)
	argIdx++

	movieBranch := r.buildMovieBranch(startArg, endArg, f, &args, &argIdx)
	filteredEpisodes := r.buildFilteredEpisodesCTE(startArg, endArg, f, &args, &argIdx)
	filteredSeasons := r.buildFilteredSeasonsCTE(startArg, endArg, f, &args, &argIdx)
	episodeBranch := r.buildEpisodeBranch()
	seasonBranch := r.buildSeasonBranch()

	query := fmt.Sprintf(`WITH filtered_episodes AS (%s),
     episode_seasons AS (
       SELECT DISTINCT series_id, season_number
       FROM filtered_episodes
     ),
     season_finales AS (
       SELECT e.series_id, e.season_number, MAX(e.episode_number) AS max_episode_number
       FROM episodes e
       JOIN episode_seasons es ON es.series_id = e.series_id AND es.season_number = e.season_number
       GROUP BY e.series_id, e.season_number
     ),
     filtered_seasons AS (%s),
     episode_one_with_air_date AS (
       SELECT DISTINCT e.series_id, e.season_number
       FROM episodes e
       JOIN filtered_seasons fs ON fs.series_id = e.series_id AND fs.season_number = e.season_number
       WHERE e.episode_number = 1 AND e.air_date IS NOT NULL
     )
SELECT content_id, type, title, episode_title, series_id,
       season_number, episode_number, air_date, air_time, air_timezone,
       poster_path, poster_thumbhash,
       is_premiere, is_finale
FROM (
%s UNION ALL %s UNION ALL %s
) combined
%s`,
		filteredEpisodes, filteredSeasons, movieBranch, episodeBranch, seasonBranch, calendarEventsOrderByClause)

	return query, args
}

func (r *CalendarRepository) buildMovieBranch(startArg, endArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		"mi.type = 'movie'",
		fmt.Sprintf("mi.release_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
	}
	r.appendLibraryExistsClauses("mi.content_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendPersonalFilterClause("mi.content_id", f, &conditions, args, argIdx)

	return fmt.Sprintf(`SELECT mi.content_id, 'movie'::text AS type,
       mi.title, NULL::text AS episode_title, NULL::text AS series_id,
       NULL::int AS season_number, NULL::int AS episode_number,
       mi.release_date AS air_date, NULL::text AS air_time, NULL::text AS air_timezone,
       mi.poster_path, mi.poster_thumbhash,
       FALSE AS is_premiere, FALSE AS is_finale
FROM media_items mi
WHERE %s`, strings.Join(conditions, " AND "))
}

func (r *CalendarRepository) buildFilteredEpisodesCTE(startArg, endArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		fmt.Sprintf("e.air_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
		"e.season_number > 0", // exclude specials
	}
	r.appendLibraryExistsClauses("e.series_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendPersonalFilterClause("e.series_id", f, &conditions, args, argIdx)

	return fmt.Sprintf(`SELECT e.content_id, e.series_id, e.season_number,
       e.episode_number, e.title AS episode_title, e.air_date,
       mi.title AS title, mi.air_time, mi.air_timezone,
       mi.poster_path, mi.poster_thumbhash
FROM episodes e
JOIN media_items mi ON mi.content_id = e.series_id
WHERE %s`, strings.Join(conditions, " AND "))
}

func (r *CalendarRepository) buildEpisodeBranch() string {
	return `SELECT fe.content_id, 'episode'::text AS type,
       fe.title, fe.episode_title, fe.series_id,
       fe.season_number, fe.episode_number,
       fe.air_date, fe.air_time, fe.air_timezone,
       fe.poster_path, fe.poster_thumbhash,
       (fe.episode_number = 1) AS is_premiere,
       (fe.episode_number = sf.max_episode_number) AS is_finale
FROM filtered_episodes fe
LEFT JOIN season_finales sf ON sf.series_id = fe.series_id AND sf.season_number = fe.season_number`
}

func (r *CalendarRepository) buildFilteredSeasonsCTE(startArg, endArg int, f CalendarFilter, args *[]any, argIdx *int) string {
	conditions := []string{
		fmt.Sprintf("s.air_date BETWEEN $%d::date AND $%d::date", startArg, endArg),
		"s.season_number > 0", // exclude specials
	}
	r.appendLibraryExistsClauses("s.series_id", f, &conditions, args, argIdx)
	r.appendContentRatingClause("mi", f, &conditions, args, argIdx)
	r.appendPersonalFilterClause("s.series_id", f, &conditions, args, argIdx)

	return fmt.Sprintf(`SELECT s.content_id, s.series_id, s.season_number,
       s.title AS episode_title, s.air_date, mi.title AS title, mi.air_time, mi.air_timezone,
       COALESCE(NULLIF(s.poster_path, ''), mi.poster_path) AS poster_path,
       COALESCE(NULLIF(s.poster_thumbhash, ''), mi.poster_thumbhash) AS poster_thumbhash
FROM seasons s
JOIN media_items mi ON mi.content_id = s.series_id
WHERE %s`, strings.Join(conditions, " AND "))
}

func (r *CalendarRepository) buildSeasonBranch() string {
	return `SELECT fs.content_id, 'season_premiere'::text AS type,
       fs.title, fs.episode_title, fs.series_id,
       fs.season_number, NULL::int AS episode_number,
       fs.air_date, fs.air_time, fs.air_timezone,
       fs.poster_path, fs.poster_thumbhash,
       TRUE AS is_premiere, FALSE AS is_finale
FROM filtered_seasons fs
LEFT JOIN episode_one_with_air_date e1
       ON e1.series_id = fs.series_id AND e1.season_number = fs.season_number
WHERE e1.series_id IS NULL`
}

// appendContentRatingClause adds content rating ceiling enforcement.
func (r *CalendarRepository) appendContentRatingClause(miAlias string, f CalendarFilter, conditions *[]string, args *[]any, argIdx *int) {
	if f.MaxContentRating != "" {
		applyAccessFilter(miAlias, AccessFilter{MaxContentRating: f.MaxContentRating}, conditions, args, argIdx)
	}
}

// appendLibraryExistsClauses adds library-scoping via EXISTS subqueries.
// Using EXISTS instead of JOIN avoids row multiplication when an item
// belongs to multiple libraries, eliminating the need for DISTINCT.
func (r *CalendarRepository) appendLibraryExistsClauses(contentIDExpr string, f CalendarFilter, conditions *[]string, args *[]any, argIdx *int) {
	if f.LibraryID != nil {
		if f.AllowedLibraryIDs != nil {
			allowed := false
			for _, id := range f.AllowedLibraryIDs {
				if id == *f.LibraryID {
					allowed = true
					break
				}
			}
			if !allowed {
				*conditions = append(*conditions, "1 = 0")
				return
			}
		}

		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = $%d)",
			contentIDExpr, *argIdx,
		))
		*args = append(*args, *f.LibraryID)
		*argIdx++
	} else if f.AllowedLibraryIDs != nil {
		if len(f.AllowedLibraryIDs) == 0 {
			*conditions = append(*conditions, "1 = 0")
			return
		}
		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s AND mil.media_folder_id = ANY($%d))",
			contentIDExpr, *argIdx,
		))
		*args = append(*args, f.AllowedLibraryIDs)
		*argIdx++
	} else {
		// No library restrictions but still need to verify the item is in at least one library.
		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM media_item_libraries mil WHERE mil.content_id = %s)",
			contentIDExpr,
		))
	}

	if len(f.DisabledLibraryIDs) == 0 {
		return
	}

	*conditions = append(*conditions, fmt.Sprintf(
		"NOT EXISTS (SELECT 1 FROM media_item_libraries mil_disabled WHERE mil_disabled.content_id = %s AND mil_disabled.media_folder_id = ANY($%d))",
		contentIDExpr, *argIdx,
	))
	*args = append(*args, f.DisabledLibraryIDs)
	*argIdx++
}

// appendPersonalFilterClause adds favorites/watchlist EXISTS subqueries.
func (r *CalendarRepository) appendPersonalFilterClause(itemIDExpr string, f CalendarFilter, conditions *[]string, args *[]any, argIdx *int) {
	switch f.Filter {
	case "favorites":
		userArg := *argIdx
		*args = append(*args, f.UserID)
		*argIdx++
		profileArg := *argIdx
		*args = append(*args, f.ProfileID)
		*argIdx++
		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM user_favorites uf WHERE uf.user_id = $%d AND uf.profile_id = $%d AND uf.media_item_id = %s)",
			userArg, profileArg, itemIDExpr,
		))
	case "watchlist":
		userArg := *argIdx
		*args = append(*args, f.UserID)
		*argIdx++
		profileArg := *argIdx
		*args = append(*args, f.ProfileID)
		*argIdx++
		*conditions = append(*conditions, fmt.Sprintf(
			"EXISTS (SELECT 1 FROM user_watchlist uw WHERE uw.user_id = $%d AND uw.profile_id = $%d AND uw.media_item_id = %s)",
			userArg, profileArg, itemIDExpr,
		))
	}
}
