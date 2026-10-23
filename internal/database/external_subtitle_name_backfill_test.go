package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

func TestRepairExternalSubtitleNames(t *testing.T) {
	stored := `[
	  {"path":"/m/Movie.en.srt","language":"en","format":"srt","title":"Movie.en.srt","forced":false,"default":false,"hearing_impaired":false},
	  {"path":"/m/Movie.sdh.en.srt","language":"sdh","format":"srt","title":"Movie.sdh.en.srt","forced":false},
	  {"path":"/m/Movie.en.hi.srt","language":"en","format":"srt","title":"Movie.en.hi.srt"},
	  {"path":"/m/Movie.ai.hr.srt","language":"ai","format":"srt","title":"Movie.ai.hr.srt","embedded_title":"kept"},
	  {"path":"/m/Movie.hi.srt","language":"hi","format":"srt"},
	  {"path":"/elsewhere/Other.sdh.srt","language":"sdh","format":"srt"},
	  {"language":"sdh"}
	]`
	repaired, ok, err := repairExternalSubtitleNames("/m/Movie.mkv", []byte(stored))
	if err != nil || !ok {
		t.Fatalf("repairExternalSubtitleNames = ok %v, err %v; want a rewrite", ok, err)
	}
	var elems []map[string]any
	if err := json.Unmarshal(repaired, &elems); err != nil {
		t.Fatalf("decoding %s: %v", repaired, err)
	}
	want := []struct {
		language any
		hearing  any
	}{
		{"en", false},
		{"en", true},
		{"en", true},
		{"hr", false},
		// Hindi: "hi" is the only language word.
		{"hi", nil},
		// Not a sidecar of this video, and no path: left alone.
		{"sdh", nil},
		{"sdh", nil},
	}
	if len(elems) != len(want) {
		t.Fatalf("element count = %d, want %d", len(elems), len(want))
	}
	for i, elem := range elems {
		if elem["language"] != want[i].language || elem["hearing_impaired"] != want[i].hearing {
			t.Errorf("element %d = language %v, hearing impaired %v; want %v, %v",
				i, elem["language"], elem["hearing_impaired"], want[i].language, want[i].hearing)
		}
	}
	if elems[3]["embedded_title"] != "kept" || elems[3]["title"] != "Movie.ai.hr.srt" || elems[3]["format"] != "srt" {
		t.Errorf("repair lost sibling fields: %v", elems[3])
	}

	if _, ok, err := repairExternalSubtitleNames("/m/Movie.mkv", repaired); ok || err != nil {
		t.Fatalf("second repair = ok %v, err %v; want nothing left to change", ok, err)
	}
	if _, _, err := repairExternalSubtitleNames("/m/Movie.mkv", []byte(`{"not":"an array"}`)); err == nil {
		t.Fatal("repair of a non-array succeeded, want an error so the row is skipped")
	}
}

// TestPostgresExternalSubtitleNameBackfill runs the real goose provider, which
// registers the Go migration, then exercises the backfill directly over seeded
// rows.
func TestPostgresExternalSubtitleNameBackfill(t *testing.T) {
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

	var folderID int
	if err := pool.QueryRow(ctx,
		`INSERT INTO media_folders (type, name, enabled) VALUES ('movie', $1, true) RETURNING id`,
		fmt.Sprintf("external-subtitle-name-backfill-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })

	dir := fmt.Sprintf("/external-subtitle-name-backfill/%d", time.Now().UnixNano())
	seed := func(name, external string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
INSERT INTO media_files (media_folder_id, file_path, base_type, file_size, external_subtitles)
VALUES ($1, $2, 'movie', 0, $3::jsonb) RETURNING id`,
			folderID, dir+"/"+name, external).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return id
	}
	staleID := seed("Movie.mkv", fmt.Sprintf(`[
	  {"path":"%[1]s/Movie.en.srt","language":"en","format":"srt","title":"Movie.en.srt","forced":false,"default":false,"hearing_impaired":false},
	  {"path":"%[1]s/Movie.sdh.en.srt","language":"sdh","format":"srt","title":"Movie.sdh.en.srt","forced":false,"default":false,"hearing_impaired":false},
	  {"path":"%[1]s/Movie.en.forced.sdh.srt","language":"en","format":"srt","title":"Movie.en.forced.sdh.srt","forced":true,"default":false,"hearing_impaired":false}
	]`, dir))
	currentID := seed("Current.mkv", fmt.Sprintf(`[
	  {"path":"%[1]s/Current.en.sdh.srt","language":"en","format":"srt","title":"Current.en.sdh.srt","forced":false,"default":false,"hearing_impaired":true}
	]`, dir))
	emptyID := seed("Empty.mkv", `[]`)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, []int{staleID, currentID, emptyID})
	})

	// xmin changes whenever a row is updated, so an unchanged value proves the
	// backfill left the row alone.
	rowVersion := func(id int) string {
		t.Helper()
		var xmin string
		if err := pool.QueryRow(ctx, `SELECT xmin::text FROM media_files WHERE id = $1`, id).Scan(&xmin); err != nil {
			t.Fatalf("reading xmin of media file %d: %v", id, err)
		}
		return xmin
	}
	untouched := map[int]string{currentID: rowVersion(currentID), emptyID: rowVersion(emptyID)}

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := backfillExternalSubtitleNames(ctx, sqlDB); err != nil {
		t.Fatalf("backfillExternalSubtitleNames: %v", err)
	}

	read := func(id int) []map[string]any {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT external_subtitles FROM media_files WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatalf("reading media file %d: %v", id, err)
		}
		var elems []map[string]any
		if err := json.Unmarshal(raw, &elems); err != nil {
			t.Fatalf("decoding %s: %v", raw, err)
		}
		return elems
	}

	t.Run("stale sidecars take the scanner's reading", func(t *testing.T) {
		elems := read(staleID)
		want := []struct {
			language        string
			forced, hearing bool
		}{{"en", false, false}, {"en", false, true}, {"en", true, true}}
		if len(elems) != len(want) {
			t.Fatalf("sidecar count = %d, want %d", len(elems), len(want))
		}
		for i, elem := range elems {
			if elem["language"] != want[i].language || elem["forced"] != want[i].forced || elem["hearing_impaired"] != want[i].hearing {
				t.Errorf("sidecar %d = %v, want language %q, forced %v, hearing impaired %v",
					i, elem, want[i].language, want[i].forced, want[i].hearing)
			}
			if elem["format"] != "srt" || elem["default"] != false || elem["title"] == "" {
				t.Errorf("sidecar %d lost sibling fields: %v", i, elem)
			}
		}
	})

	t.Run("rows already read correctly are not rewritten", func(t *testing.T) {
		for id, before := range untouched {
			if after := rowVersion(id); after != before {
				t.Errorf("media file %d was rewritten: xmin %s -> %s", id, before, after)
			}
		}
	})

	t.Run("a row changed after it was read is left for the next pass", func(t *testing.T) {
		before := rowVersion(staleID)
		n, err := applyExternalSubtitleNameRepairs(ctx, sqlDB, []externalSubtitleNameRepair{
			{ID: int64(staleID), Stored: json.RawMessage(`[]`), Repaired: json.RawMessage(`[]`)},
		})
		if err != nil || n != 0 {
			t.Fatalf("repair from an out-of-date value wrote %d rows, err %v; want none", n, err)
		}
		if after := rowVersion(staleID); after != before {
			t.Fatalf("media file %d was rewritten from an out-of-date value", staleID)
		}
	})

	t.Run("a second run leaves repaired rows alone", func(t *testing.T) {
		before := rowVersion(staleID)
		if err := backfillExternalSubtitleNames(ctx, sqlDB); err != nil {
			t.Fatalf("second backfillExternalSubtitleNames: %v", err)
		}
		if after := rowVersion(staleID); after != before {
			t.Fatalf("media file %d was rewritten again: xmin %s -> %s", staleID, before, after)
		}
	})
}
