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
	childEditedTextColumns = []string{childColumnSortTitle, childColumnOriginalTitle, childColumnTagline}
	seasonEditedColumns    = slices.Concat(childEditedTextColumns, []string{childColumnImdbID, childColumnTmdbID, childColumnTvdbID})
	episodeEditedColumns   = childEditedTextColumns
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
		editedPredicates := make([]string, 0, len(target.edited))
		for _, column := range target.edited {
			editedPredicates = append(editedPredicates, column+" <> "+sqlEmptyText)
		}
		query := clearChildEditsSQL(target.table, target.columns, target.edited, true) +
			fmt.Sprintf(` WHERE series_id = $1 AND (locked_fields <> '{}' OR %s)`, strings.Join(editedPredicates, " OR "))
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

// clearChildEditsSQL is the UPDATE, without its WHERE clause, that removes a
// season's or episode's locks, empties its lockable columns (only the locked
// ones, or all of them) and empties the edited columns.
func clearChildEditsSQL(table string, columns []childLockedColumn, edited []string, onlyLocked bool) string {
	assignments := joinColumns(columns, ", ", func(c childLockedColumn) string {
		if onlyLocked {
			return fmt.Sprintf("%[1]s = CASE WHEN %[2]d = ANY(locked_fields) THEN %[3]s ELSE %[1]s END", c.column, c.field, c.empty)
		}
		return fmt.Sprintf("%s = %s", c.column, c.empty)
	})
	for _, column := range edited {
		assignments += ", " + column + " = " + sqlEmptyText
	}
	return fmt.Sprintf(`UPDATE %s SET %s, locked_fields = '{}', updated_at = NOW()`, table, assignments)
}

// ResetEdits empties a season's lockable fields and title details and removes
// its locks, for Reset to Provider. The refresh that follows writes the
// provider's values over the empty ones, so a field the provider has no value
// for stays empty. With keepText the title, overview and text title details
// stay: a reset in another language than the series' own keeps the row's own
// text, as an item's reset does.
func (r *SeasonRepository) ResetEdits(ctx context.Context, contentID string, keepText bool) error {
	if _, err := r.pool.Exec(ctx, clearChildEditsSQL(seasonsTable, resetColumns(seasonLockedColumns, keepText), resetEditedColumns(seasonEditedColumns, keepText), false)+` WHERE content_id = $1`, contentID); err != nil {
		return fmt.Errorf("resetting season %s: %w", contentID, err)
	}
	return nil
}

// ResetEdits is SeasonRepository.ResetEdits for an episode. The series' air
// dates may come from the cleared date, so they are recomputed with it.
func (r *EpisodeRepository) ResetEdits(ctx context.Context, contentID string, keepText bool) error {
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var seriesID string
		query := clearChildEditsSQL(episodesTable, resetColumns(episodeLockedColumns, keepText), resetEditedColumns(episodeEditedColumns, keepText), false) + ` WHERE content_id = $1 RETURNING series_id`
		if err := tx.QueryRow(ctx, query, contentID).Scan(&seriesID); err != nil {
			return err
		}
		return refreshSeriesAirDates(ctx, tx, []string{seriesID})
	})
	if err != nil {
		return fmt.Errorf("resetting episode %s: %w", contentID, err)
	}
	return nil
}

// resetColumns is the lockable columns a reset empties: all of them, or all
// but the title and overview.
func resetColumns(columns []childLockedColumn, keepText bool) []childLockedColumn {
	if !keepText {
		return columns
	}
	return slices.DeleteFunc(slices.Clone(columns), func(c childLockedColumn) bool {
		return c.column == childColumnTitle || c.column == childColumnOverview
	})
}

// resetEditedColumns is the edited columns a reset empties: all of them, or
// all but the text ones.
func resetEditedColumns(columns []string, keepText bool) []string {
	if !keepText {
		return columns
	}
	return slices.DeleteFunc(slices.Clone(columns), func(c string) bool {
		return slices.Contains(childEditedTextColumns, c)
	})
}
