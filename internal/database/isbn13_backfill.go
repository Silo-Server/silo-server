package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

// isbn13BackfillVersion sorts after every migration that shipped before ISBNs
// were stored as ISBN-13.
const isbn13BackfillVersion int64 = 20261119213533

// isbn13BackfillBatchSize bounds how many ISBNs, with every stored spelling of
// each, one committed provider-ID batch rewrites, and how many media_files
// rows one group-key batch reads.
const isbn13BackfillBatchSize = 1000

// isbn13BackfillBatchAttempts bounds how many times one provider-ID batch runs
// when a writer stores a competing ISBN-13 between its ranking and its write.
const isbn13BackfillBatchAttempts = 3

// isbn13BackfillAfterDelete, when set, runs inside each provider-ID batch
// between its DELETE and its UPDATE. Tests use it to commit a competing row
// at that point; it is nil in production.
var isbn13BackfillAfterDelete func(ctx context.Context, attempt int) error

// ebookISBNGroupKeyPrefix is the content_group_key prefix the ebook scanner
// gives a file grouped by its ISBN.
const ebookISBNGroupKeyPrefix = "ebook:isbn:"

// isbn13BackfillMigration rewrites every stored ISBN as the ISBN-13 that
// bookid.CanonicalISBN gives it: media_item_provider_ids rows under the isbn
// key (in any case) and media_files.content_group_key values "ebook:isbn:<isbn>".
// The other content_group_key columns only hold video groups.
//
// Before this, the scanner kept an ISBN-10 as ten characters and old plugins
// stored ISBNs with hyphens or labels, while the code now compares ISBNs as 13
// digits. Values that do not parse are left as they are.
//
// Two items of one type cannot hold one ISBN (the provider, provider_id,
// item_type key), so where several spellings of one ISBN sit on different
// items of a type, one row keeps it: the row already holding the ISBN-13, else
// the lowest content_id. The others are deleted, which is what the scanner
// does today when a second file's ISBN is already owned.
//
// Go rather than SQL because the rule is bookid.CanonicalISBN; restating it in
// SQL would be a second copy that drifts. RunDB rather than RunTx so each batch
// commits on its own.
//
// A row an older release writes after the backfill (upgrades before 1.0 are
// not rolling; see "Cluster cutover policy" in docs/architecture/api-contract.md)
// is still compared as equal at admission. The item's next metadata write
// stores the ISBN-13, unless another item of the type already holds it: that
// write then fails on the unique key until an admin resolves the duplicate.
func isbn13BackfillMigration() *goose.Migration {
	m := goose.NewGoMigration(
		isbn13BackfillVersion,
		&goose.GoFunc{RunDB: backfillISBN13},
		// The spellings replaced are not recorded, so there is nothing to
		// restore; every reader accepts the ISBN-13.
		&goose.GoFunc{RunDB: func(context.Context, *sql.DB) error { return nil }},
	)
	m.Source = fmt.Sprintf("%d_isbn13_backfill.go", isbn13BackfillVersion)
	return m
}

func backfillISBN13(ctx context.Context, db *sql.DB) error {
	if err := backfillISBN13ProviderIDs(ctx, db); err != nil {
		return err
	}
	return backfillISBN13GroupKeys(ctx, db)
}

// backfillISBN13ProviderIDs rewrites the stored ISBN provider IDs in batches
// of ISBNs. Each batch carries every spelling of its ISBNs, so all the rows
// that compete for one ISBN are ranked together.
func backfillISBN13ProviderIDs(ctx context.Context, db *sql.DB) error {
	providers, err := queryStrings(ctx, db, `
SELECT DISTINCT provider
  FROM public.media_item_provider_ids
 WHERE lower(btrim(provider)) = $1`, bookid.ISBNProvider)
	if err != nil {
		return fmt.Errorf("listing ISBN provider keys: %w", err)
	}
	if len(providers) == 0 {
		return nil
	}
	// A value of 13 digits starting 978 or 979 is already its own ISBN-13 or
	// is not an ISBN, so it is never rewritten and is not loaded. A row holding
	// the ISBN-13 is still ranked: isbn13Spellings adds each ISBN-13 to its
	// own spellings.
	stored, err := queryStrings(ctx, db, `
SELECT DISTINCT provider_id
  FROM public.media_item_provider_ids
 WHERE provider = ANY($1)
   AND provider_id !~ '^97[89][0-9]{10}$'`, providers)
	if err != nil {
		return fmt.Errorf("listing stored ISBNs: %w", err)
	}

	spellings := isbn13Spellings(stored)
	canonical := make([]string, 0, len(spellings))
	for isbn := range spellings {
		canonical = append(canonical, isbn)
	}
	slices.Sort(canonical)

	var deleted, updated int64
	for start := 0; start < len(canonical); start += isbn13BackfillBatchSize {
		var raws, isbns []string
		for _, isbn := range canonical[start:min(start+isbn13BackfillBatchSize, len(canonical))] {
			for _, raw := range spellings[isbn] {
				raws = append(raws, raw)
				isbns = append(isbns, isbn)
			}
		}
		d, u, err := rewriteISBN13ProviderIDBatch(ctx, db, providers, raws, isbns)
		if err != nil {
			return err
		}
		deleted += d
		updated += u
	}
	slog.InfoContext(ctx, "ISBN-13 backfill: provider IDs rewritten",
		"isbns", len(canonical), "rows_updated", updated, "duplicate_rows_deleted", deleted)
	return nil
}

// isbn13Spellings maps each ISBN-13 that a stored value other than itself
// parses to, to every stored value that parses to it, the ISBN-13 included.
func isbn13Spellings(stored []string) map[string][]string {
	spellings := make(map[string][]string)
	for _, raw := range stored {
		if isbn, ok := bookid.CanonicalISBN(raw); ok && isbn != raw {
			spellings[isbn] = append(spellings[isbn], raw)
		}
	}
	for isbn, raws := range spellings {
		spellings[isbn] = append(raws, isbn)
	}
	return spellings
}

// rewriteISBN13ProviderIDBatch deletes all but one row per (provider,
// item_type, ISBN) among the rows holding a spelling of a batch ISBN, then
// stores the ISBN-13 on the rows left. raws[i] is a spelling of isbns[i].
//
// A writer that stores an ISBN-13 between the ranking and the write makes the
// UPDATE fail on the unique key. The batch then rolls back and runs again: the
// new ranking sees that writer's row as the holder and deletes the spelling
// that collided with it.
func rewriteISBN13ProviderIDBatch(ctx context.Context, db *sql.DB, providers, raws, isbns []string) (int64, int64, error) {
	for attempt := 1; ; attempt++ {
		deleted, updated, err := rewriteISBN13ProviderIDBatchOnce(ctx, db, providers, raws, isbns, attempt)
		if err == nil || attempt == isbn13BackfillBatchAttempts || !isUniqueViolation(err) {
			return deleted, updated, err
		}
		slog.WarnContext(ctx, "ISBN-13 backfill: batch hit a unique violation, retrying",
			"attempt", attempt, "error", err)
	}
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	pgErr, ok := errors.AsType[*pgconn.PgError](err)
	return ok && pgErr.Code == "23505"
}

func rewriteISBN13ProviderIDBatchOnce(ctx context.Context, db *sql.DB, providers, raws, isbns []string, attempt int) (int64, int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: begin batch: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
WITH spelling(raw, isbn) AS (
        SELECT * FROM unnest($2::text[], $3::text[])),
     held AS (
        SELECT p.content_id, p.provider, p.provider_id,
               row_number() OVER (
                   PARTITION BY p.provider, p.item_type, s.isbn
                   ORDER BY (p.provider_id = s.isbn) DESC, p.content_id) AS rank
          FROM public.media_item_provider_ids p
          JOIN spelling s ON s.raw = p.provider_id
         WHERE p.provider = ANY($1))
DELETE FROM public.media_item_provider_ids p
 USING held h
 WHERE h.rank > 1
   AND p.content_id = h.content_id
   AND p.provider = h.provider
   AND p.provider_id = h.provider_id`, providers, raws, isbns)
	if err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: deleting duplicate ISBN rows: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: deleting duplicate ISBN rows: %w", err)
	}

	if isbn13BackfillAfterDelete != nil {
		if err := isbn13BackfillAfterDelete(ctx, attempt); err != nil {
			return 0, 0, fmt.Errorf("ISBN-13 backfill: test hook: %w", err)
		}
	}

	// Every spelling left is the only one of its ISBN for its provider key and
	// item type, unless a writer stored the ISBN-13 since the ranking. Then
	// this fails on the unique key and the caller runs the batch again.
	res, err = tx.ExecContext(ctx, `
UPDATE public.media_item_provider_ids p
   SET provider_id = s.isbn,
       updated_at = NOW()
  FROM unnest($2::text[], $3::text[]) AS s(raw, isbn)
 WHERE p.provider = ANY($1)
   AND p.provider_id = s.raw
   AND s.raw <> s.isbn`, providers, raws, isbns)
	if err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: storing ISBN-13 values: %w", err)
	}
	updated, err := res.RowsAffected()
	if err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: storing ISBN-13 values: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("ISBN-13 backfill: commit batch: %w", err)
	}
	return deleted, updated, nil
}

// backfillISBN13GroupKeys rewrites "ebook:isbn:<isbn>" group keys whose ISBN
// is not already an ISBN-13, one batch of media_files rows at a time. Files of
// one book that were grouped apart stay on their items; later files group by
// the ISBN-13.
func backfillISBN13GroupKeys(ctx context.Context, db *sql.DB) error {
	var lastID, total int64
	for {
		batch, last, err := loadISBN13GroupKeyBatch(ctx, db, lastID)
		if err != nil {
			return err
		}
		if last == lastID {
			break
		}
		lastID = last
		if len(batch.ids) == 0 {
			continue
		}
		res, err := db.ExecContext(ctx, `
UPDATE public.media_files f
   SET content_group_key = k.new_key
  FROM unnest($1::bigint[], $2::text[], $3::text[]) AS k(id, old_key, new_key)
 WHERE f.id = k.id
   AND f.content_group_key = k.old_key`, batch.ids, batch.olds, batch.news)
		if err != nil {
			return fmt.Errorf("ISBN-13 backfill: rewriting group keys: %w", err)
		}
		changed, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("ISBN-13 backfill: rewriting group keys: %w", err)
		}
		total += changed
	}
	slog.InfoContext(ctx, "ISBN-13 backfill: ebook group keys rewritten", "rows", total)
	return nil
}

type isbn13GroupKeyBatch struct {
	ids        []int64
	olds, news []string
}

// loadISBN13GroupKeyBatch reads the next ISBN group keys after lastID and
// returns the ones to rewrite and the last id read (lastID when none is left).
func loadISBN13GroupKeyBatch(ctx context.Context, db *sql.DB, lastID int64) (isbn13GroupKeyBatch, int64, error) {
	var batch isbn13GroupKeyBatch
	rows, err := db.QueryContext(ctx, `
SELECT id, content_group_key
  FROM public.media_files
 WHERE id > $1
   AND starts_with(content_group_key, $2)
 ORDER BY id
 LIMIT $3`, lastID, ebookISBNGroupKeyPrefix, isbn13BackfillBatchSize)
	if err != nil {
		return batch, lastID, fmt.Errorf("ISBN-13 backfill: loading group keys after media_files id %d: %w", lastID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var key string
		if err := rows.Scan(&id, &key); err != nil {
			return batch, lastID, fmt.Errorf("ISBN-13 backfill: scanning group keys: %w", err)
		}
		lastID = id
		raw := strings.TrimPrefix(key, ebookISBNGroupKeyPrefix)
		if isbn, ok := bookid.CanonicalISBN(raw); ok && isbn != raw {
			batch.ids = append(batch.ids, id)
			batch.olds = append(batch.olds, key)
			batch.news = append(batch.news, ebookISBNGroupKeyPrefix+isbn)
		}
	}
	if err := rows.Err(); err != nil {
		return batch, lastID, fmt.Errorf("ISBN-13 backfill: loading group keys: %w", err)
	}
	return batch, lastID, nil
}

func queryStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}
