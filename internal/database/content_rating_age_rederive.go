package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/access"
)

// contentRatingAgeRederiveVersion sorts after every migration that shipped
// before access.Normalize understood a country code written with a dash
// ("SE-7", "DE-16").
const contentRatingAgeRederiveVersion int64 = 20261118200650

// contentRatingAgeRederiveBatchSize bounds how many media_items rows one
// committed update rewrites. It is small because each series row also
// rebuilds that series' episode catalog entries inside the same statement.
const contentRatingAgeRederiveBatchSize = 500

// contentRatingAgeRederiveMigration re-derives media_items.content_rating_age
// for every row stored as access.UnrecognizedRatingAge whose rating string
// access.StoredRating now resolves.
//
// A rescan does not do this on its own: the age is written only when an
// item's metadata is written, and a matched title is not rewritten unless its
// metadata changes. Without this repair, a title rated "SE-7" before the
// dashed form was understood stays hidden from every profile with a
// content-rating ceiling. Only rows at the unrecognized age can change,
// because a string Normalize already resolved resolves the same way now.
// Updating a series row fires trg_episode_catalog_entries_series, which copies
// the age to that series' episode_catalog_entries. A node still running an
// older release can store the unrecognized age again during a rolling upgrade;
// that row converges on its next metadata write, as before this repair.
//
// Go rather than SQL because the rule is access.Normalize; restating it in SQL
// would be a second copy that drifts. RunDB rather than RunTx so each batch
// commits on its own.
func contentRatingAgeRederiveMigration() *goose.Migration {
	m := goose.NewGoMigration(
		contentRatingAgeRederiveVersion,
		&goose.GoFunc{RunDB: rederiveContentRatingAges},
		// The rows held only the unrecognized age before, so there is nothing
		// to restore; the next metadata write derives the age again anyway.
		&goose.GoFunc{RunDB: func(context.Context, *sql.DB) error { return nil }},
	)
	m.Source = fmt.Sprintf("%d_content_rating_age_rederive.go", contentRatingAgeRederiveVersion)
	return m
}

// rederiveContentRatingAges rewrites each stored rating string that now
// resolves, one batch at a time.
func rederiveContentRatingAges(ctx context.Context, db *sql.DB) error {
	stored, err := unrecognizedContentRatings(ctx, db)
	if err != nil {
		return err
	}
	rewrites := contentRatingAgeRewrites(stored)
	if len(rewrites) == 0 {
		slog.InfoContext(ctx, "content rating age repair: no unrecognized rating resolves now",
			"distinct_values", len(stored))
		return nil
	}
	var total int64
	for raw, age := range rewrites {
		n, err := rewriteContentRatingAge(ctx, db, raw, age)
		if err != nil {
			return err
		}
		total += n
	}
	slog.InfoContext(ctx, "content rating age repair finished",
		"distinct_values", len(stored), "rewritten_values", len(rewrites), "rows", total)
	return nil
}

// contentRatingAgeRewrites returns, for each stored rating string that no
// longer resolves to the unrecognized age, the age StoredRating gives it now
// (nil for an explicit unrated marker such as "US-NR").
func contentRatingAgeRewrites(stored []string) map[string]*int {
	rewrites := make(map[string]*int)
	for _, raw := range stored {
		age := access.StoredRating(raw)
		if age != nil && *age == access.UnrecognizedRatingAge {
			continue
		}
		rewrites[raw] = age
	}
	return rewrites
}

// unrecognizedContentRatings lists the distinct rating strings stored with the
// unrecognized age.
func unrecognizedContentRatings(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
SELECT DISTINCT content_rating
  FROM public.media_items
 WHERE content_rating_age = $1
   AND content_rating IS NOT NULL`, access.UnrecognizedRatingAge)
	if err != nil {
		return nil, fmt.Errorf("listing unrecognized content ratings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scanning unrecognized content rating: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing unrecognized content ratings: %w", err)
	}
	return values, nil
}

// rewriteContentRatingAge stores age on every row rated raw that still holds
// the unrecognized age, in committed batches, and returns the rows changed. A
// row whose rating or age changed since it was listed is left alone.
func rewriteContentRatingAge(ctx context.Context, db *sql.DB, raw string, age *int) (int64, error) {
	var stored any // NULL for an unrated marker
	if age != nil {
		stored = *age
	}
	var total int64
	for {
		res, err := db.ExecContext(ctx, `
UPDATE public.media_items
   SET content_rating_age = $2
 WHERE content_id IN (
        SELECT content_id
          FROM public.media_items
         WHERE content_rating = $1
           AND content_rating_age = $3
         LIMIT $4)
   AND content_rating = $1
   AND content_rating_age = $3`,
			raw, stored, access.UnrecognizedRatingAge, contentRatingAgeRederiveBatchSize)
		if err != nil {
			return total, fmt.Errorf("re-deriving the age of content rating %q: %w", raw, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("re-deriving the age of content rating %q: %w", raw, err)
		}
		total += n
		if n > 0 {
			continue
		}
		// A row a concurrent write changed under the batch is skipped, so
		// even an empty batch can leave rows it never selected. Stop only
		// once none is left.
		var remaining bool
		if err := db.QueryRowContext(ctx, `
SELECT EXISTS (
        SELECT 1
          FROM public.media_items
         WHERE content_rating = $1
           AND content_rating_age = $2)`,
			raw, access.UnrecognizedRatingAge).Scan(&remaining); err != nil {
			return total, fmt.Errorf("re-deriving the age of content rating %q: %w", raw, err)
		}
		if !remaining {
			return total, nil
		}
	}
}
