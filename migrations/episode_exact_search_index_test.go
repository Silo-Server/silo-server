package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Run the concurrent migration through Goose in an isolated schema. The long
// metadata fixture must fit every new key that replaces the predecessor key.
func TestEpisodeExactSearchIndexMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := fmt.Sprintf("episode_exact_migration_%d", time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	})
	// These generated fields keep the maintained catalog invariants while
	// isolating the index migration from unrelated metadata and file triggers.
	exec("CREATE TABLE " + schema + `.episode_catalog_entries (
		media_folder_id bigint NOT NULL, episode_id text NOT NULL,
		year integer NOT NULL, title text NOT NULL,
		sort_key text GENERATED ALWAYS AS (LOWER(title)) STORED,
		search_title_normalized text GENERATED ALWAYS AS (public.normalize_search_text(title)) STORED,
		PRIMARY KEY (media_folder_id, episode_id))`)
	exec("CREATE INDEX predecessor_year ON " + schema + ".episode_catalog_entries (media_folder_id, year DESC, sort_key, episode_id)")
	const libraryID int64 = 3_000_000_000
	rng := rand.New(rand.NewPCG(20260930, 134737))
	longTitle := func() string {
		title := make([]byte, 1400)
		for i := range title {
			title[i] = 'a' + byte(rng.IntN(26))
		}
		return string(title)
	}
	exec("INSERT INTO "+schema+".episode_catalog_entries(media_folder_id,episode_id,year,title) VALUES($1,'long-before',2024,$2),($1,'duplicate-year',2024,$2)", libraryID, longTitle())
	var width int
	if err := db.QueryRowContext(t.Context(), "SELECT octet_length(title) FROM "+schema+".episode_catalog_entries WHERE episode_id='long-before'").Scan(&width); err != nil || width != 1400 {
		t.Fatalf("predecessor accepted title width=%d, want1400: %v", width, err)
	}
	// Model an interrupted concurrent build with the new migration's name.
	// Goose must remove its invalid artifact before IF NOT EXISTS can succeed.
	const indexName = "idx_episode_catalog_entries_exact_search_page"
	_, err = db.ExecContext(t.Context(), "CREATE UNIQUE INDEX CONCURRENTLY "+indexName+" ON "+schema+".episode_catalog_entries(year)")
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); !ok || pgErr.Code != "23505" {
		t.Fatalf("expected failed concurrent index build: %v", err)
	}
	var invalid bool
	if err := db.QueryRowContext(t.Context(), "SELECT NOT indisvalid FROM pg_index WHERE indexrelid=$1::regclass", schema+"."+indexName).Scan(&invalid); err != nil || !invalid {
		t.Fatalf("failed build did not leave an invalid artifact: invalid=%v err=%v", invalid, err)
	}
	const filename = "20260930134737_index_exact_episode_search.sql"
	data, err := FS.ReadFile("sql/" + filename)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSQL := strings.NewReplacer("public.", schema+".", "'public'", "'"+schema+"'").Replace(string(data))
	provider, err := goose.NewProvider(goose.DialectPostgres, db,
		fstest.MapFS{filename: &fstest.MapFile{Data: []byte(fixtureSQL)}},
		goose.WithTableName(schema+".goose_db_version"))
	if err != nil {
		t.Fatal(err)
	}
	assertIndex := func() int64 {
		t.Helper()
		var oid int64
		var usable bool
		var definition string
		err := db.QueryRowContext(t.Context(), `SELECT c.oid::bigint,
			i.indisvalid AND i.indisready AND NOT i.indisunique, pg_get_indexdef(c.oid)
			FROM pg_class c JOIN pg_index i ON i.indexrelid=c.oid
			WHERE c.oid=$1::regclass`, schema+"."+indexName).Scan(&oid, &usable, &definition)
		if err != nil || !usable || !strings.Contains(definition, "hashtext(search_title_normalized)") || !strings.Contains(definition, "lower(title), episode_id") {
			t.Fatalf("exact index unusable or wrong key: usable=%v definition=%s err=%v", usable, definition, err)
		}
		return oid
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndex()
	exec("UPDATE "+schema+".episode_catalog_entries SET title=$1 WHERE episode_id='long-before'", longTitle())
	exec("INSERT INTO "+schema+".episode_catalog_entries(media_folder_id,episode_id,year,title) VALUES($1,'long-after',2025,$2)", libraryID, longTitle())
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+schema+".episode_catalog_entries WHERE octet_length(title)=1400").Scan(&width); err != nil || width != 3 {
		t.Fatalf("long metadata inserts/updates preserved %d rows, want3: %v", width, err)
	}
	// Find a real collision efficiently, without storing the candidate catalog.
	// The larger title is the requested identity, so a missing equality recheck
	// would return the smaller colliding title before LIMIT 1.
	var other, wanted string
	for _, candidates := range []int{100_000, 500_000} {
		err = db.QueryRowContext(t.Context(), `SELECT min(title),max(title)
			FROM (SELECT 'collision' || md5(i::text) AS title,
				hashtext('collision' || md5(i::text)) AS hash
				FROM generate_series(1,$1) i) candidates
			GROUP BY hash HAVING count(*)>1 ORDER BY hash LIMIT 1`, candidates).Scan(&other, &wanted)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatalf("no deterministic hash collision fixture: %v", err)
	}
	var collision bool
	if err := db.QueryRowContext(t.Context(), `SELECT $1<>$2 AND hashtext($1)=hashtext($2)
		AND public.normalize_search_text($1)=$1 AND public.normalize_search_text($2)=$2`, other, wanted).Scan(&collision); err != nil || !collision {
		t.Fatalf("invalid normalized hash collision: collision=%v err=%v", collision, err)
	}
	exec("INSERT INTO "+schema+".episode_catalog_entries(media_folder_id,episode_id,year,title) VALUES($1,'collision-other',2024,$2),($1,'collision-wanted',2024,$3)", libraryID+1, other, wanted)
	exec("SET enable_seqscan=off")
	var got string
	hashOnly := "SELECT episode_id FROM " + schema + ".episode_catalog_entries WHERE media_folder_id=$1::bigint AND hashtext(search_title_normalized)=hashtext($2::text) ORDER BY LOWER(title),episode_id LIMIT 1"
	if err := db.QueryRowContext(t.Context(), hashOnly, libraryID+1, wanted).Scan(&got); err != nil || got != "collision-other" {
		t.Fatalf("collision would not expose missing pre-limit recheck: got=%s err=%v", got, err)
	}
	rechecked := "SELECT episode_id FROM " + schema + ".episode_catalog_entries WHERE media_folder_id=$1::bigint AND hashtext(search_title_normalized)=hashtext($2::text) AND search_title_normalized=$2::text ORDER BY LOWER(title),episode_id LIMIT 1"
	if err := db.QueryRowContext(t.Context(), rechecked, libraryID+1, wanted).Scan(&got); err != nil || got != "collision-wanted" {
		t.Fatalf("pre-limit equality admitted a hash collision: got=%s err=%v", got, err)
	}
	var plan []byte
	if err := db.QueryRowContext(t.Context(), "EXPLAIN (FORMAT JSON) "+rechecked, libraryID+1, wanted).Scan(&plan); err != nil || !strings.Contains(string(plan), indexName) {
		t.Fatalf("collision recheck did not use exact index: %s err=%v", plan, err)
	}
	exec("SET enable_seqscan=on")
	if _, err := provider.Down(t.Context()); err != nil {
		t.Fatal(err)
	}
	var removed, predecessorUsable bool
	if err := db.QueryRowContext(t.Context(), `SELECT to_regclass($1) IS NULL,
		(SELECT indisvalid AND indisready FROM pg_index WHERE indexrelid=$2::regclass)`, schema+"."+indexName, schema+".predecessor_year").Scan(&removed, &predecessorUsable); err != nil || !removed || !predecessorUsable {
		t.Fatalf("down exact removed=%v predecessor usable=%v err=%v", removed, predecessorUsable, err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	oid := assertIndex()
	// Retry an already committed Up without rebuilding its valid index.
	exec("DELETE FROM " + schema + ".goose_db_version WHERE version_id>0")
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := assertIndex(); got != oid {
		t.Fatalf("valid index rebuilt on retry: oid=%d want%d", got, oid)
	}
	t.Log("1400-byte metadata, bigint libraries, failed concurrent retry, down/reapply, valid retry, and real hash collision recheck passed")
}
