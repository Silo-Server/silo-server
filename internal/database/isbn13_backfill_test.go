package database

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestPostgresISBN13Backfill seeds the spellings real databases hold and
// checks the exact rows the backfill leaves.
func TestPostgresISBN13Backfill(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	// The backfill reads the whole table, so the prefix and the run's ISBNs
	// keep the assertions to this test's rows.
	prefix := fmt.Sprintf("isbn13-backfill-%d-", time.Now().UnixNano())
	isbn10, isbn13 := testISBNs()
	contentIDs := seedISBN13BackfillItems(ctx, t, pool, prefix, []isbnSeed{
		// A plain ISBN-10.
		{"plain10", "ebook", "isbn", isbn10(0)},
		// A hyphenated ISBN-13 from an old audiobook plugin.
		{"hyphen13", "audiobook", "isbn", hyphenateISBN(isbn13(1))},
		// Two ebooks holding two spellings of one ISBN, neither canonical:
		// the lower content_id keeps it.
		{"dupA", "ebook", "isbn", "ISBN " + hyphenateISBN(isbn10(2))},
		{"dupB", "ebook", "isbn", isbn10(2)},
		// One ebook holds the ISBN-10, another already the ISBN-13: the
		// ISBN-13 holder keeps it although its content_id sorts later.
		{"holds10", "ebook", "isbn", isbn10(3)},
		{"holds13", "ebook", "isbn", isbn13(3)},
		// The same ISBN on an audiobook and an ebook: both keep it.
		{"audio", "audiobook", "isbn", isbn10(4)},
		{"ebook", "ebook", "isbn", hyphenateISBN(isbn13(4))},
		// A mixed-case provider key keeps its key and gets the ISBN-13.
		{"upper", "ebook", "ISBN", isbn10(5)},
		// A value that is not an ISBN stays as it is.
		{"garbage", "ebook", "isbn", prefix + "not-an-isbn"},
	})
	groupKeys := map[string]string{
		"ebook:isbn:" + isbn10(6):     "ebook:isbn:" + isbn13(6),
		"ebook:isbn:" + isbn13(7):     "ebook:isbn:" + isbn13(7),
		"ebook:isbn:4006381333931":    "ebook:isbn:4006381333931", // an EAN-13 that is not an ISBN
		"ebook:title:" + prefix + "x": "ebook:title:" + prefix + "x",
	}
	fileIDs := seedISBN13BackfillGroupKeys(ctx, t, pool, prefix, groupKeys)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for run := 1; run <= 2; run++ {
		if err := backfillISBN13(ctx, sqlDB); err != nil {
			t.Fatalf("backfillISBN13 run %d: %v", run, err)
		}
	}

	want := []string{
		"audio isbn " + isbn13(4),
		"dupA isbn " + isbn13(2),
		"ebook isbn " + isbn13(4),
		"garbage isbn " + prefix + "not-an-isbn",
		"holds13 isbn " + isbn13(3),
		"hyphen13 isbn " + isbn13(1),
		"plain10 isbn " + isbn13(0),
		"upper ISBN " + isbn13(5),
	}
	if got := readISBN13BackfillRows(ctx, t, pool, prefix, contentIDs); !slices.Equal(got, want) {
		t.Errorf("provider ids after backfill:\n got %q\nwant %q", got, want)
	}
	for seeded, fileID := range fileIDs {
		var key string
		if err := pool.QueryRow(ctx, `SELECT content_group_key FROM media_files WHERE id = $1`, fileID).Scan(&key); err != nil {
			t.Fatalf("reading group key of file %d: %v", fileID, err)
		}
		if key != groupKeys[seeded] {
			t.Errorf("group key %q became %q, want %q", seeded, key, groupKeys[seeded])
		}
	}
}

// TestPostgresISBN13BackfillRetriesUniqueViolation commits another item's
// ISBN-13 between a batch's DELETE and its UPDATE, so the UPDATE collides on
// the unique key. The batch must run again and delete the raw spelling.
func TestPostgresISBN13BackfillRetriesUniqueViolation(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	prefix := fmt.Sprintf("isbn13-backfill-retry-%d-", time.Now().UnixNano())
	isbn10, isbn13 := testISBNs()
	contentIDs := seedISBN13BackfillItems(ctx, t, pool, prefix, []isbnSeed{
		{"raw", "ebook", "isbn", isbn10(0)},
		{"holder", "ebook", "isbn", prefix + "placeholder"},
	})
	holderID := prefix + "holder"

	var attempts []int
	isbn13BackfillAfterDelete = func(ctx context.Context, attempt int) error {
		attempts = append(attempts, attempt)
		if attempt != 1 {
			return nil
		}
		// Committed on another connection, as a concurrent writer would.
		_, err := pool.Exec(ctx,
			`UPDATE media_item_provider_ids SET provider_id = $1 WHERE content_id = $2`,
			isbn13(0), holderID)
		return err
	}
	t.Cleanup(func() { isbn13BackfillAfterDelete = nil })

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	deleted, updated, err := rewriteISBN13ProviderIDBatch(ctx, sqlDB,
		[]string{"isbn"}, []string{isbn10(0), isbn13(0)}, []string{isbn13(0), isbn13(0)})
	if err != nil {
		t.Fatalf("rewriteISBN13ProviderIDBatch: %v", err)
	}
	if !slices.Equal(attempts, []int{1, 2}) {
		t.Errorf("batch attempts = %v, want [1 2]", attempts)
	}
	if deleted != 1 || updated != 0 {
		t.Errorf("deleted, updated = %d, %d; want 1, 0", deleted, updated)
	}
	want := []string{"holder isbn " + isbn13(0)}
	if got := readISBN13BackfillRows(ctx, t, pool, prefix, contentIDs); !slices.Equal(got, want) {
		t.Errorf("provider ids after backfill:\n got %q\nwant %q", got, want)
	}
}

type isbnSeed struct{ item, itemType, provider, value string }

func seedISBN13BackfillItems(ctx context.Context, t *testing.T, pool *pgxpool.Pool, prefix string, seeds []isbnSeed) []string {
	t.Helper()
	var contentIDs []string
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, contentIDs)
	})
	for _, seed := range seeds {
		contentID := prefix + seed.item
		contentIDs = append(contentIDs, contentID)
		if _, err := pool.Exec(ctx,
			`INSERT INTO media_items (content_id, type, title) VALUES ($1, $2, $1)`,
			contentID, seed.itemType); err != nil {
			t.Fatalf("seed item %s: %v", seed.item, err)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
VALUES ($1, $2, $3, $4)`, contentID, seed.itemType, seed.provider, seed.value); err != nil {
			t.Fatalf("seed provider id for %s: %v", seed.item, err)
		}
	}
	return contentIDs
}

// seedISBN13BackfillGroupKeys stores one ebook file per group key and returns
// each file's id by the key it was seeded with.
func seedISBN13BackfillGroupKeys(ctx context.Context, t *testing.T, pool *pgxpool.Pool, prefix string, keys map[string]string) map[string]int64 {
	t.Helper()
	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('ebook', $1, true) RETURNING id`,
		prefix+"folder").Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })
	fileIDs := make(map[string]int64, len(keys))
	n := 0
	for key := range keys {
		n++
		var fileID int64
		if err := pool.QueryRow(ctx, `
INSERT INTO media_files (media_folder_id, file_path, base_type, file_size, content_group_key)
VALUES ($1, $2, 'ebook', 0, $3) RETURNING id`,
			folderID, fmt.Sprintf("/%s%d.epub", prefix, n), key).Scan(&fileID); err != nil {
			t.Fatalf("seed media file %s: %v", key, err)
		}
		fileIDs[key] = fileID
	}
	return fileIDs
}

// readISBN13BackfillRows lists the seeded items' provider IDs as
// "<item> <provider> <value>", sorted.
func readISBN13BackfillRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool, prefix string, contentIDs []string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT substr(content_id, $2), provider, provider_id
  FROM media_item_provider_ids
 WHERE content_id = ANY($1)
 ORDER BY content_id, provider`, contentIDs, len(prefix)+1)
	if err != nil {
		t.Fatalf("reading provider ids: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var item, provider, value string
		if err := rows.Scan(&item, &provider, &value); err != nil {
			t.Fatalf("scanning provider id: %v", err)
		}
		got = append(got, item+" "+provider+" "+value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading provider ids: %v", err)
	}
	return got
}

// testISBNs returns the ISBN-10 and ISBN-13 of the n-th book of a run, a 978
// ISBN whose nine-digit body is unique to the run, so rows other runs left
// cannot collide with it. The check digits are computed here, independently
// of the code under test.
func testISBNs() (isbn10, isbn13 func(n int) string) {
	base := time.Now().UnixNano() / 1000 % 10_000_000
	body := func(n int) string { return fmt.Sprintf("%07d%02d", base, n) }
	isbn10 = func(n int) string {
		b, sum := body(n), 0
		for i := 0; i < 9; i++ {
			sum += int(b[i]-'0') * (10 - i)
		}
		if check := (11 - sum%11) % 11; check < 10 {
			return b + strconv.Itoa(check)
		}
		return b + "X"
	}
	isbn13 = func(n int) string {
		b, sum := "978"+body(n), 0
		for i := 0; i < 12; i++ {
			d := int(b[i] - '0')
			if i%2 == 1 {
				d *= 3
			}
			sum += d
		}
		return b + strconv.Itoa((10-sum%10)%10)
	}
	return isbn10, isbn13
}

// hyphenateISBN writes an ISBN with hyphens, as old plugins stored it.
func hyphenateISBN(isbn string) string {
	if len(isbn) == 13 {
		return isbn[:3] + "-" + isbn[3:4] + "-" + isbn[4:7] + "-" + isbn[7:12] + "-" + isbn[12:]
	}
	return isbn[:1] + "-" + isbn[1:4] + "-" + isbn[4:9] + "-" + isbn[9:]
}
