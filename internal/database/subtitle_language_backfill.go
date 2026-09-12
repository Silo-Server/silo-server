package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/lang"
)

// subtitleLanguageBackfillVersion sorts after every SQL migration that shipped
// before the scanner started storing canonical subtitle language tags.
const subtitleLanguageBackfillVersion int64 = 20260912120000

// subtitleLanguageBackfillBatchSize bounds how many media_files ids one
// committed batch covers, so a large library never holds a single long
// transaction over the table.
const subtitleLanguageBackfillBatchSize = 20000

// A bounded number of convergence passes prevents an active legacy writer
// from holding startup migrations open indefinitely. If all passes still find
// rewrites, the migration returns an error and Goose leaves it unapplied for a
// later retry after the old writer has stopped.
const subtitleLanguageBackfillMaxPasses = 3

// subtitleLanguageBackfillMigration rewrites the language field of every
// stored subtitle_tracks and external_subtitles element to the form the
// scanner now writes at ingest.
//
// A Go migration rather than SQL because the canonical form is defined by
// lang.CompatibleTag (BCP 47 parsing plus the display-name table). Restating
// that in SQL means a second, partial copy of the rule that drifts from the
// scanner; an earlier SQL draft blanked valid region and script tags for that
// reason. This migration collects the distinct stored values, asks the Go
// function for each one, and pushes only the values that change back into SQL
// as a small lookup map.
//
// RunDB rather than RunTx so each id window commits on its own.
func subtitleLanguageBackfillMigration() *goose.Migration {
	return goose.NewGoMigration(
		subtitleLanguageBackfillVersion,
		&goose.GoFunc{RunDB: backfillSubtitleLanguages},
		// The previous spelling is not recorded, so there is nothing to
		// restore; a rescan re-derives every tag from the files.
		&goose.GoFunc{RunDB: func(context.Context, *sql.DB) error { return nil }},
	)
}

// subtitleLanguageColumns are the media_files jsonb arrays whose elements
// carry a "language" field.
var subtitleLanguageColumns = []string{"subtitle_tracks", "external_subtitles"}

// backfillSubtitleLanguages runs the rewrite against db.
func backfillSubtitleLanguages(ctx context.Context, db *sql.DB) error {
	// Re-read the distinct values after each sweep. This keeps the migration
	// convergent when an older scanner is still writing legacy spellings while
	// the ID windows are being processed; a single snapshot can otherwise miss
	// a row written after its window has already been committed.
	for pass := 1; pass <= subtitleLanguageBackfillMaxPasses; pass++ {
		changed, err := backfillSubtitleLanguagesPass(ctx, db, pass)
		if err != nil {
			return err
		}
		if changed == 0 {
			return nil
		}
	}
	return fmt.Errorf("subtitle language repair still found legacy values after %d passes; retry after older writers stop", subtitleLanguageBackfillMaxPasses)
}

func backfillSubtitleLanguagesPass(ctx context.Context, db *sql.DB, pass int) (int64, error) {
	stored, err := distinctSubtitleLanguages(ctx, db)
	if err != nil {
		return 0, err
	}
	rewrite, unparseable := subtitleLanguageRewrites(stored)
	if len(unparseable) > 0 {
		slog.WarnContext(ctx, "subtitle language repair: leaving values the canonicalizer cannot parse untouched",
			"values", unparseable)
	}
	if len(rewrite) == 0 {
		slog.InfoContext(ctx, "subtitle language repair pass: every stored tag is already canonical",
			"pass", pass, "distinct_values", len(stored))
		return 0, nil
	}
	slog.InfoContext(ctx, "subtitle language repair starting",
		"pass", pass, "distinct_values", len(stored), "rewrites", len(rewrite))

	mapping, err := json.Marshal(rewrite)
	if err != nil {
		return 0, fmt.Errorf("encoding subtitle language rewrite map: %w", err)
	}

	var lastID int64
	batches := 0
	changed := make(map[string]int64, len(subtitleLanguageColumns))
	for {
		batchMax, ok, err := nextSubtitleLanguageBatch(ctx, db, lastID)
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		counts, err := rewriteSubtitleLanguageBatch(ctx, db, string(mapping), lastID, batchMax)
		if err != nil {
			return 0, err
		}
		batches++
		for column, n := range counts {
			changed[column] += n
		}
		lastID = batchMax
		slog.InfoContext(ctx, "subtitle language repair batch committed",
			"batch", batches, "through_media_file_id", batchMax,
			"subtitle_tracks_rows", counts["subtitle_tracks"],
			"external_subtitles_rows", counts["external_subtitles"])
	}
	slog.InfoContext(ctx, "subtitle language repair finished",
		"pass", pass, "batches", batches,
		"subtitle_tracks_rows", changed["subtitle_tracks"],
		"external_subtitles_rows", changed["external_subtitles"])
	return changed["subtitle_tracks"] + changed["external_subtitles"], nil
}

// subtitleLanguageRewrites returns, for each stored value whose canonical form
// differs, that canonical form. Already-canonical and empty values are left
// out so the SQL rewrite touches only rows that change.
//
// Values the canonicalizer rejects are returned separately and never
// rewritten: a repair migration has no way to recover an overwritten value,
// so a stored tag it cannot parse (a grandfathered BCP 47 form, or a token the
// old scanner accepted verbatim) is left for a rescan to re-derive rather than
// erased. The scanner's own write path still applies the strict rule to new
// ingests.
func subtitleLanguageRewrites(stored []string) (rewrite map[string]string, unparseable []string) {
	rewrite = make(map[string]string)
	for _, value := range stored {
		if value == "" {
			continue
		}
		canonical := lang.CompatibleTag(value)
		switch {
		case canonical == "":
			unparseable = append(unparseable, value)
		case canonical != value:
			rewrite[value] = canonical
		}
	}
	sort.Strings(unparseable)
	return rewrite, unparseable
}

// distinctSubtitleLanguages lists every distinct language string stored in
// either subtitle array. Elements without a string language are skipped; the
// rewrite never touches them.
func distinctSubtitleLanguages(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT t->>'language'
  FROM public.media_files mf,
       jsonb_array_elements(CASE WHEN jsonb_typeof(mf.subtitle_tracks) = 'array' THEN mf.subtitle_tracks ELSE '[]'::jsonb END) t
 WHERE jsonb_typeof(t->'language') = 'string'
UNION
SELECT DISTINCT t->>'language'
  FROM public.media_files mf,
       jsonb_array_elements(CASE WHEN jsonb_typeof(mf.external_subtitles) = 'array' THEN mf.external_subtitles ELSE '[]'::jsonb END) t
 WHERE jsonb_typeof(t->'language') = 'string'`)
	if err != nil {
		return nil, fmt.Errorf("listing stored subtitle languages: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scanning stored subtitle language: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing stored subtitle languages: %w", err)
	}
	return values, nil
}

// nextSubtitleLanguageBatch returns the highest media_files id in the next id
// window after lastID, or ok=false once the table is exhausted.
func nextSubtitleLanguageBatch(ctx context.Context, db *sql.DB, lastID int64) (int64, bool, error) {
	var batchMax sql.NullInt64
	err := db.QueryRowContext(ctx, `
SELECT max(id) FROM (
  SELECT id FROM public.media_files WHERE id > $1 ORDER BY id LIMIT $2
) q`, lastID, subtitleLanguageBackfillBatchSize).Scan(&batchMax)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("locating next subtitle language batch after id %d: %w", lastID, err)
	}
	if !batchMax.Valid {
		return 0, false, nil
	}
	return batchMax.Int64, true, nil
}

// rewriteSubtitleLanguageBatch rewrites both arrays for ids in (lastID,
// batchMax] inside one transaction and reports the rows changed per column.
// mapping is a JSON object from stored value to canonical value; an element
// is rewritten only when its language is a key of that object.
func rewriteSubtitleLanguageBatch(ctx context.Context, db *sql.DB, mapping string, lastID, batchMax int64) (map[string]int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning subtitle language batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	counts := make(map[string]int64, len(subtitleLanguageColumns))
	for _, column := range subtitleLanguageColumns {
		// column is one of two fixed identifiers, never user input.
		query := fmt.Sprintf(`
UPDATE public.media_files mf
   SET %[1]s = (
     SELECT jsonb_agg(
              CASE WHEN $1::jsonb ? (elem->>'language')
                   THEN jsonb_set(elem, '{language}', $1::jsonb -> (elem->>'language'))
                   ELSE elem END
              ORDER BY ord)
       FROM jsonb_array_elements(mf.%[1]s) WITH ORDINALITY x(elem, ord))
 WHERE mf.id > $2 AND mf.id <= $3
   AND jsonb_typeof(mf.%[1]s) = 'array'
   AND EXISTS (
     SELECT 1 FROM jsonb_array_elements(mf.%[1]s) t
      WHERE $1::jsonb ? (t->>'language'))`, column)
		result, err := tx.ExecContext(ctx, query, mapping, lastID, batchMax)
		if err != nil {
			return nil, fmt.Errorf("rewriting %s languages for media_files %d..%d: %w", column, lastID, batchMax, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("counting %s rows for media_files %d..%d: %w", column, lastID, batchMax, err)
		}
		counts[column] = n
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing subtitle language batch through id %d: %w", batchMax, err)
	}
	return counts, nil
}
