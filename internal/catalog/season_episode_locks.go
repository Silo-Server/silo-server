package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The upsert assignments a refresh writes a season's or episode's own fields
// through. They read the row's locks inside the write, because a refresh reads
// the row long before it writes: an edit saved in between must still win.
var (
	seasonLockableAssignments  = lockedAssignments(seasonsTable, seasonLockedColumns)
	episodeLockableAssignments = lockedAssignments(episodesTable, episodeLockedColumns)
)

const (
	seasonsTable  = "seasons"
	episodesTable = "episodes"

	childColumnTitle         = "title"
	childColumnOverview      = "overview"
	childColumnRuntime       = "runtime"
	childColumnSortTitle     = "sort_title"
	childColumnOriginalTitle = "original_title"
	childColumnTagline       = "tagline"
	childColumnImdbID        = "imdb_id"
	childColumnTmdbID        = "tmdb_id"
	childColumnTvdbID        = "tvdb_id"
	sqlEmptyText             = "''"
	sqlNull                  = "NULL"

	// The titles metadata's fallbackSeasonTitle and fallbackEpisodeTitle give a
	// row the provider has no title for. A cleared row the new show doesn't
	// have gets no refresh write, and lists show its title as stored.
	sqlDefaultSeasonTitle  = "CASE WHEN season_number = 0 THEN 'Specials' ELSE 'Season ' || season_number END"
	sqlDefaultEpisodeTitle = "'Episode ' || episode_number"
)

// childLockedColumn pairs a lockable column with its lock and the value it
// takes when the edit it held is cleared.
type childLockedColumn struct {
	column string
	field  int
	empty  string
}

// Artwork locks on the parent series instead, and numbers can't lock because
// refreshes find these rows by them. Seasons have no runtime.
var (
	seasonLockedColumns = []childLockedColumn{
		{childColumnTitle, fieldNameLocked, sqlDefaultSeasonTitle},
		{childColumnOverview, fieldOverviewLocked, sqlEmptyText},
		{columnAirDate, fieldReleaseDatesLocked, sqlNull},
	}
	episodeLockedColumns = []childLockedColumn{
		{childColumnTitle, fieldNameLocked, sqlDefaultEpisodeTitle},
		{childColumnOverview, fieldOverviewLocked, sqlEmptyText},
		{columnAirDate, fieldReleaseDatesLocked, sqlNull},
		{childColumnRuntime, fieldRuntimeLocked, sqlNull},
	}
)

// The title details only an admin writes on a season or episode. No refresh
// writes them, so they need no lock, but they describe one show like a locked
// field does. An episode's provider IDs are not among them: refreshes write those.
var (
	seasonEditedColumns = []string{
		childColumnSortTitle, childColumnOriginalTitle, childColumnTagline,
		childColumnImdbID, childColumnTmdbID, childColumnTvdbID,
	}
	episodeEditedColumns = []string{childColumnSortTitle, childColumnOriginalTitle, childColumnTagline}
)

func lockedAssignments(table string, columns []childLockedColumn) string {
	return joinColumns(columns, ",\n", func(c childLockedColumn) string {
		return fmt.Sprintf("%[1]s = CASE WHEN %[2]d = ANY(%[3]s.locked_fields) THEN %[3]s.%[1]s ELSE EXCLUDED.%[1]s END",
			c.column, c.field, table)
	})
}

func joinColumns(columns []childLockedColumn, sep string, render func(childLockedColumn) string) string {
	rendered := make([]string, 0, len(columns))
	for _, c := range columns {
		rendered = append(rendered, render(c))
	}
	return strings.Join(rendered, sep)
}

// lockableFields keeps the requested locks that columns can honor.
func lockableFields(fields []int, columns []childLockedColumn) []int {
	kept := []int{}
	for _, c := range columns {
		if slices.Contains(fields, c.field) {
			kept = append(kept, c.field)
		}
	}
	return kept
}

// childOverviewApplies reports whether a localized overview shows over a season
// or episode's own. Provider text doesn't cover a locked overview. A manual or
// AI translation does: it is a translation for that language, not a provider's
// competing text.
func childOverviewApplies(locks []int, source string) bool {
	return !slices.Contains(locks, fieldOverviewLocked) || source != models.LocalizationSourceProvider
}

// ClearSeriesChildEditsTx removes every season and episode lock on seriesID,
// resets the locked values and empties the title details, for a series that now
// names a different show. The edits describe the old show, so they must not stay
// even when the new show's provider has nothing to replace them with.
func ClearSeriesChildEditsTx(ctx context.Context, tx pgx.Tx, seriesID string) error {
	for _, target := range []struct {
		table   string
		columns []childLockedColumn
		edited  []string
	}{{seasonsTable, seasonLockedColumns, seasonEditedColumns}, {episodesTable, episodeLockedColumns, episodeEditedColumns}} {
		assignments := joinColumns(target.columns, ", ", func(c childLockedColumn) string {
			return fmt.Sprintf("%[1]s = CASE WHEN %[2]d = ANY(locked_fields) THEN %[3]s ELSE %[1]s END", c.column, c.field, c.empty)
		})
		editedPredicates := make([]string, 0, len(target.edited))
		clearAssignments := make([]string, 0, len(target.edited))
		for _, column := range target.edited {
			editedPredicates = append(editedPredicates, column+" <> ''")
			clearAssignments = append(clearAssignments, column+" = ''")
		}
		query := fmt.Sprintf(`UPDATE %s SET %s, %s, locked_fields = '{}', updated_at = NOW()
			WHERE series_id = $1 AND (locked_fields <> '{}' OR %s)`,
			target.table, assignments, strings.Join(clearAssignments, ", "), strings.Join(editedPredicates, " OR "))
		if _, err := tx.Exec(ctx, query, seriesID); err != nil {
			return fmt.Errorf("clearing %s edits of series %s: %w", target.table, seriesID, err)
		}
	}
	// The cleared air dates may be what the series' own dates came from, and the
	// new show's provider may return no episode to recompute them.
	if err := refreshSeriesAirDates(ctx, tx, []string{seriesID}); err != nil {
		return fmt.Errorf("clearing edits of series %s: %w", seriesID, err)
	}
	return nil
}
