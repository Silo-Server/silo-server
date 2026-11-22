package catalog

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

// TestWorkClaimsReplacePerSourceDB pins how a book's work ids are stored:
// every edition of a book may claim the same work, the key never reaches
// media_item_provider_ids, and an answer replaces only its own source's
// claims and those with no source, except for a catalog that could not be
// asked. An answer holding only a work id, or a work id and keys that are
// never stored, leaves the provider IDs alone.
func TestWorkClaimsReplacePerSourceDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	ebookA := fmt.Sprintf("work-claims-ebook-a-%d", stamp)
	ebookB := fmt.Sprintf("work-claims-ebook-b-%d", stamp)
	audiobook := fmt.Sprintf("work-claims-audiobook-%d", stamp)
	t.Cleanup(func() {
		batchEquivExec(t, pool, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{ebookA, ebookB, audiobook})
	})
	batchEquivExec(t, pool, `
		INSERT INTO media_items (content_id, type, title, genres) VALUES
			($1, 'ebook', 'Work Claims', '{}'::text[]),
			($2, 'ebook', 'Work Claims', '{}'::text[]),
			($3, 'audiobook', 'Work Claims', '{}'::text[])`, ebookA, ebookB, audiobook)
	repo := NewProviderIDRepository(pool)
	work := fmt.Sprintf("OL%dW", stamp%1000000)
	answer := func(source string, failed []string, claims ...bookid.WorkClaim) []bookid.WorkClaims {
		return []bookid.WorkClaims{{Source: source, Claims: claims, LookupFailed: failed}}
	}
	olWork := func(value, evidence string) bookid.WorkClaim {
		return bookid.WorkClaim{Provider: bookid.KeyOpenLibraryWork, ProviderID: value, Evidence: evidence}
	}

	// Both ebook editions and the audiobook claim one work. The work key in
	// the provider-ID map, as an admin could type it, is dropped there.
	for i, id := range []string{ebookA, ebookB} {
		ids := map[string]string{"isbn": fmt.Sprintf("97803064061%02d", i), "OpenLibrary_Work": work}
		if err := repo.ReplaceByContentIDWithWorkClaims(ctx, id, ids, answer("books-metadata", nil, olWork(work, "isbn"))); err != nil {
			t.Fatalf("ReplaceByContentIDWithWorkClaims(%s): %v", id, err)
		}
		assertDurableProviderIDs(t, pool, id, map[string]string{"isbn": ids["isbn"]})
	}
	applyWorkClaims(t, pool, audiobook, "audiobook", answer("books-metadata", nil, olWork(work, "title_author")))
	assertWorkClaims(t, pool, ebookA, map[string]string{"openlibrary_work": work + "/isbn/books-metadata"})
	assertWorkClaims(t, pool, ebookB, map[string]string{"openlibrary_work": work + "/isbn/books-metadata"})
	assertWorkClaims(t, pool, audiobook, map[string]string{"openlibrary_work": work + "/title_author/books-metadata"})

	// Another capability's claim on another key stays through what follows.
	applyWorkClaims(t, pool, ebookA, "ebook", answer("ebook-metadata", nil, bookid.WorkClaim{Provider: "hardcover", ProviderID: "42"}))
	// The same capability with another value replaces its claim.
	applyWorkClaims(t, pool, ebookA, "ebook", answer("books-metadata", nil, olWork(work+"X", "isbn")))
	assertWorkClaims(t, pool, ebookA, map[string]string{
		"openlibrary_work": work + "X/isbn/books-metadata",
		"hardcover":        "42/unknown/ebook-metadata",
	})
	// An answer without the key keeps it while that catalog could not be
	// asked, and deletes it once the catalog answered.
	applyWorkClaims(t, pool, ebookA, "ebook", answer("books-metadata", []string{"openlibrary"}))
	assertWorkClaims(t, pool, ebookA, map[string]string{
		"openlibrary_work": work + "X/isbn/books-metadata",
		"hardcover":        "42/unknown/ebook-metadata",
	})
	applyWorkClaims(t, pool, ebookA, "ebook", answer("books-metadata", []string{"hardcover"}))
	assertWorkClaims(t, pool, ebookA, map[string]string{"hardcover": "42/unknown/ebook-metadata"})
	assertWorkClaims(t, pool, ebookB, map[string]string{"openlibrary_work": work + "/isbn/books-metadata"})

	// A map holding only a work key stores the claim and leaves the stored
	// provider IDs alone.
	if err := repo.ReplaceByContentIDWithWorkClaims(ctx, ebookB, map[string]string{"hardcover": "5"},
		answer("ebook-metadata", nil, bookid.WorkClaim{Provider: "hardcover", ProviderID: "5"})); err != nil {
		t.Fatalf("ReplaceByContentIDWithWorkClaims(%s, work key only): %v", ebookB, err)
	}
	assertDurableProviderIDs(t, pool, ebookB, map[string]string{"isbn": "9780306406101"})
	// So does a map whose other keys are not durable ids.
	if err := repo.ReplaceByContentIDWithWorkClaims(ctx, ebookB, map[string]string{"hardcover": "6", "_filepath": "/books/b.epub"},
		answer("ebook-metadata", nil, bookid.WorkClaim{Provider: "hardcover", ProviderID: "6"})); err != nil {
		t.Fatalf("ReplaceByContentIDWithWorkClaims(%s, work key and non-durable key): %v", ebookB, err)
	}
	assertDurableProviderIDs(t, pool, ebookB, map[string]string{"isbn": "9780306406101"})
	assertWorkClaims(t, pool, ebookB, map[string]string{
		"openlibrary_work": work + "/isbn/books-metadata",
		"hardcover":        "6/unknown/ebook-metadata",
	})

	// A claim with no source (moved by the migration) is replaced by an answer
	// that holds a work id or names a failed catalog: deleted when the answer
	// omits its key, kept while the answer reports its catalog as failed. An
	// answer that says nothing about works leaves it. A named source's claim
	// stays.
	batchEquivExec(t, pool, `DELETE FROM media_item_work_ids WHERE content_id = $1`, audiobook)
	batchEquivExec(t, pool, `
		INSERT INTO media_item_work_ids (content_id, provider, provider_id, evidence, source) VALUES
			($1, 'openlibrary_work', 'OL1W', 'unknown', ''),
			($1, 'hardcover', '7', 'unknown', '')`, audiobook)
	applyWorkClaims(t, pool, audiobook, "audiobook", answer("audiobook-metadata", nil))
	assertWorkClaims(t, pool, audiobook, map[string]string{"openlibrary_work": "OL1W/unknown/", "hardcover": "7/unknown/"})
	applyWorkClaims(t, pool, audiobook, "audiobook", answer("books-metadata", []string{"hardcover"}))
	assertWorkClaims(t, pool, audiobook, map[string]string{"hardcover": "7/unknown/"})
	batchEquivExec(t, pool, `
		INSERT INTO media_item_work_ids (content_id, provider, provider_id, evidence, source)
		VALUES ($1, 'openlibrary_work', 'OL1W', 'unknown', 'audiobook-metadata')`, audiobook)
	applyWorkClaims(t, pool, audiobook, "audiobook",
		answer("books-metadata", nil, bookid.WorkClaim{Provider: "hardcover", ProviderID: "9"}))
	assertWorkClaims(t, pool, audiobook, map[string]string{
		"openlibrary_work": "OL1W/unknown/audiobook-metadata",
		"hardcover":        "9/unknown/books-metadata",
	})
}

// TestSeparateBookWorkIDsMigrationDB replays the migration in a scratch
// schema over legacy rows: book work keys move with their casing folded and
// one row kept per item and key, other rows stay, and the CHECK then rejects
// a work key on a book row.
func TestSeparateBookWorkIDsMigrationDB(t *testing.T) {
	pool := newBatchEquivTestPool(t)
	ctx := t.Context()
	schema := pgx.Identifier{fmt.Sprintf("work_ids_migration_%d", time.Now().UnixNano())}.Sanitize()
	local := func(sql string) string { return strings.ReplaceAll(sql, "public.", schema+".") }
	exec := func(sql string, args ...any) error {
		_, err := pool.Exec(ctx, local(sql), args...)
		return err
	}
	must := func(sql string, args ...any) {
		t.Helper()
		if err := exec(sql, args...); err != nil {
			t.Fatalf("%v\n%s", err, sql)
		}
	}
	migration, err := os.ReadFile("../../migrations/sql/20261119214004_separate_book_work_ids.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(migration), "-- +goose Down")
	must("CREATE SCHEMA " + schema)
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	must(`CREATE TABLE public.media_items (content_id text COLLATE "C" PRIMARY KEY, type text NOT NULL);
		CREATE TABLE public.media_item_provider_ids (
			content_id text COLLATE "C" NOT NULL REFERENCES public.media_items(content_id) ON UPDATE CASCADE ON DELETE CASCADE,
			provider text NOT NULL, provider_id text NOT NULL, item_type text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (content_id, provider), UNIQUE (provider, provider_id, item_type));
		INSERT INTO public.media_items VALUES ('ebook', 'ebook'), ('audiobook', 'audiobook'), ('movie', 'movie');
		INSERT INTO public.media_item_provider_ids (content_id, provider, provider_id, item_type, updated_at) VALUES
			('ebook', 'isbn', '9780306406157', 'ebook', now()),
			('ebook', 'Hardcover', ' 41 ', 'ebook', now() - interval '1 day'),
			('ebook', 'hardcover', '42', 'ebook', now()),
			('audiobook', 'OpenLibrary_Work', 'OL1W', 'audiobook', now()),
			('movie', 'hardcover', '7', 'movie', now());`)
	for _, statement := range migrationStatements(up) {
		must(statement)
	}

	rows, err := pool.Query(ctx, local(`
		SELECT content_id || ':' || provider || '=' || provider_id || '/' || evidence || '/' || source
		FROM public.media_item_work_ids ORDER BY 1`))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"audiobook:openlibrary_work=OL1W/unknown/", "ebook:hardcover=42/unknown/"}; !equalStrings(claims, want) {
		t.Fatalf("work ids = %q, want %q", claims, want)
	}
	rows, err = pool.Query(ctx, local(`
		SELECT content_id || ':' || provider || '=' || provider_id FROM public.media_item_provider_ids ORDER BY 1`))
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ebook:isbn=9780306406157", "movie:hardcover=7"}; !equalStrings(left, want) {
		t.Fatalf("provider ids = %q, want %q", left, want)
	}

	err = exec(`INSERT INTO public.media_item_provider_ids (content_id, provider, provider_id, item_type) VALUES ('ebook', 'HardCover', '43', 'ebook')`)
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.ConstraintName != "media_item_provider_ids_book_work_ids_check" {
		t.Fatalf("inserting a work key on a book row: err = %v, want the work-id CHECK", err)
	}
}

func equalStrings(got, want []string) bool {
	return strings.Join(got, "\n") == strings.Join(want, "\n")
}

func applyWorkClaims(t *testing.T, pool *pgxpool.Pool, contentID, itemType string, claims []bookid.WorkClaims) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // a no-op once committed
	if err := ApplyWorkClaimsTx(ctx, tx, contentID, itemType, claims); err != nil {
		t.Fatalf("ApplyWorkClaimsTx(%s): %v", contentID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// assertWorkClaims compares an item's work ids, each as value/evidence/source.
func assertWorkClaims(t *testing.T, pool *pgxpool.Pool, contentID string, want map[string]string) {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT provider, provider_id || '/' || evidence || '/' || source FROM media_item_work_ids WHERE content_id = $1`, contentID)
	if err != nil {
		t.Fatalf("read work ids of %s: %v", contentID, err)
	}
	got := map[string]string{}
	for rows.Next() {
		var provider, claim string
		if err := rows.Scan(&provider, &claim); err != nil {
			t.Fatal(err)
		}
		got[provider] = claim
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, want) {
		t.Fatalf("work ids of %s = %v, want %v", contentID, got, want)
	}
}
