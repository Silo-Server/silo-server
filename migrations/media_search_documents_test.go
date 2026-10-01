package migrations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestMediaSearchDocumentsMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("media_search_migration_%d", time.Now().UnixNano())
	exec := func(sql string) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	exec("CREATE FUNCTION " + schema + ".normalize_search_text(text) RETURNS text LANGUAGE sql IMMUTABLE AS 'SELECT public.normalize_search_text($1)'")
	exec("CREATE TABLE " + schema + ".media_items(content_id text PRIMARY KEY,title text,original_title text,sort_title text,overview text,revision int NOT NULL DEFAULT 0)")
	exec("INSERT INTO " + schema + ".media_items SELECT lpad(i::text,5,'0'),'Café & Dune: Part Two',CASE WHEN i%2=0 THEN 'Dune Part 2' END,'Dune','A buried signal returns.',i FROM generate_series(1,2501) i")
	// Up replaces the predecessor expression indexes; Down restores them.
	exec("CREATE INDEX idx_media_items_search_title_fields ON " + schema + ".media_items USING gin (to_tsvector('simple',title))")
	exec("CREATE INDEX idx_media_items_search_overview ON " + schema + ".media_items USING gin (to_tsvector('english',overview))")
	// Model an interrupted migration after nullable columns and a failed
	// concurrent build. Duplicate titles leave the requested index invalid.
	exec("ALTER TABLE " + schema + ".media_items ADD COLUMN search_title_vector tsvector")
	// An interrupted older installation can already have the source-only
	// trigger. Up must replace its definition to cover content-ID moves.
	exec("CREATE FUNCTION " + schema + ".set_media_item_search_fields() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'")
	exec("CREATE TRIGGER trg_media_items_search_fields BEFORE INSERT OR UPDATE OF title,original_title,sort_title,overview ON " + schema + ".media_items FOR EACH ROW EXECUTE FUNCTION " + schema + ".set_media_item_search_fields()")
	_, err = db.ExecContext(t.Context(), "CREATE UNIQUE INDEX CONCURRENTLY idx_media_items_stored_search_title ON "+schema+".media_items(title)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected invalid index from failed concurrent build: %v", err)
	}
	exec("CREATE TABLE " + schema + ".backfill_transactions(tx bigint)")
	exec("CREATE FUNCTION " + schema + ".record_backfill_transaction() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN INSERT INTO " + schema + ".backfill_transactions VALUES(txid_current()); RETURN NEW; END'")
	exec("CREATE TRIGGER record_backfill_transaction AFTER UPDATE OF search_title_vector ON " + schema + ".media_items FOR EACH ROW EXECUTE FUNCTION " + schema + ".record_backfill_transaction()")
	// Move an unfilled row behind the first batch's key boundary at an
	// observable write, using the content-ID-only update used by online re-ID.
	// The destination still parses to its original revision value.
	exec("CREATE FUNCTION " + schema + ".move_pending_document() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN UPDATE " + schema + ".media_items SET content_id=''00002501'' WHERE content_id=''02501''; RETURN NEW; END'")
	exec("CREATE TRIGGER move_pending_document AFTER UPDATE OF search_title_vector ON " + schema + ".media_items FOR EACH ROW WHEN (NEW.content_id='00001') EXECUTE FUNCTION " + schema + ".move_pending_document()")

	const filename = "20260930130041_store_media_search_fields.sql"
	data, err := FS.ReadFile("sql/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL := strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(string(data))
	fixture := fstest.MapFS{filename: &fstest.MapFile{Data: []byte(fixtureSQL)}}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, fixture, goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	searchIndexes := func() []string {
		t.Helper()
		rows, err := db.QueryContext(t.Context(), `SELECT c.relname FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname LIKE 'idx_media_items_%search%' AND i.indisvalid AND i.indisready ORDER BY c.relname`, schema)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return names
	}
	assertDocuments := func() {
		t.Helper()
		var equal, preserved int
		err := db.QueryRowContext(t.Context(), `SELECT count(*) FILTER (WHERE
			original_title_normalized=public.normalize_search_text(original_title)
			AND sort_title_normalized=public.normalize_search_text(sort_title)
			AND search_title_vector=(
				setweight(to_tsvector('simple',public.normalize_search_text(title)),'A') ||
				setweight(to_tsvector('simple',public.normalize_search_text(original_title)),'A') ||
				setweight(to_tsvector('simple',public.normalize_search_text(sort_title)),'B'))
			AND search_overview_vector=to_tsvector('english',COALESCE(overview,''))),
			count(*) FILTER (WHERE revision=content_id::int) FROM `+schema+`.media_items`).Scan(&equal, &preserved)
		if err != nil || equal != 2501 || preserved != 2501 {
			t.Fatalf("backfill expression parity=%d original rows=%d want 2501: %v", equal, preserved, err)
		}
		if got := searchIndexes(); !slices.Equal(got, []string{"idx_media_items_stored_search_overview", "idx_media_items_stored_search_title"}) {
			t.Fatalf("usable search indexes=%v want only the stored-document indexes", got)
		}
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	var movedDocumentReady bool
	if err := db.QueryRowContext(t.Context(), "SELECT search_title_vector IS NOT NULL AND search_overview_vector IS NOT NULL AND original_title_normalized IS NOT NULL AND sort_title_normalized IS NOT NULL FROM "+schema+".media_items WHERE content_id='00002501'").Scan(&movedDocumentReady); err != nil || !movedDocumentReady {
		t.Fatalf("content-ID move behind the backfill boundary left an unfilled document: ready=%v error=%v", movedDocumentReady, err)
	}
	assertDocuments()
	var transactions int
	if err := db.QueryRowContext(t.Context(), "SELECT count(DISTINCT tx) FROM "+schema+".backfill_transactions").Scan(&transactions); err != nil || transactions != 3 {
		t.Fatalf("backfill transactions=%d want three bounded batches: %v", transactions, err)
	}
	// Re-run the whole nontransactional Up as Goose does after interruption.
	// Already-populated documents must not be rewritten on that retry.
	exec("DELETE FROM " + schema + ".goose_db_version WHERE version_id>0")
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertDocuments()
	var updates int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+schema+".backfill_transactions").Scan(&updates); err != nil || updates != 2500 {
		t.Fatalf("retry rewrote completed documents: updates=%d error=%v", updates, err)
	}
	// A populated document must survive re-ID without normalization work.
	// Sentinels distinguish reuse from recomputing equal source text.
	exec("UPDATE " + schema + ".media_items SET original_title_normalized='sentinel original',sort_title_normalized='sentinel sort',search_title_vector='sentinel'::tsvector,search_overview_vector='sentinel'::tsvector WHERE content_id='00001'")
	exec("UPDATE " + schema + ".media_items SET content_id='0000001' WHERE content_id='00001'")
	var reused bool
	if err := db.QueryRowContext(t.Context(), "SELECT original_title_normalized='sentinel original' AND sort_title_normalized='sentinel sort' AND search_title_vector='sentinel'::tsvector AND search_overview_vector='sentinel'::tsvector FROM "+schema+".media_items WHERE content_id='0000001'").Scan(&reused); err != nil || !reused {
		t.Fatalf("content-ID-only move recomputed a populated document: reused=%v error=%v", reused, err)
	}
	exec("UPDATE " + schema + ".media_items SET title='Law and Order',original_title=NULL,sort_title='Law & Order',overview=NULL WHERE content_id='0000001'")
	assertDocuments()
	exec("DROP TRIGGER record_backfill_transaction ON " + schema + ".media_items")
	exec("DROP TRIGGER move_pending_document ON " + schema + ".media_items")
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	var columns int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM information_schema.columns WHERE table_schema=$1 AND table_name='media_items' AND column_name IN ('original_title_normalized','sort_title_normalized','search_title_vector','search_overview_vector')`, schema).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("rollback left stored search columns=%d: %v", columns, err)
	}
	if got := searchIndexes(); !slices.Equal(got, []string{"idx_media_items_search_overview", "idx_media_items_search_title_fields"}) {
		t.Fatalf("rollback search indexes=%v want the predecessor expression indexes", got)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertDocuments()
}
