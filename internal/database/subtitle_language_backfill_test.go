package database

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/Silo-Server/silo-server/migrations"
)

func TestSubtitleLanguageRewrites(t *testing.T) {
	got, unparseable := subtitleLanguageRewrites([]string{
		"", "en", "eng", "English", "pt-br", "en-US", "zh-Hans", "sr-Latn",
		"fre", "ger", "spa", "und", "fil", "forced", "Монгол", "x-foo", "i-klingon",
	})
	want := map[string]string{
		"eng": "en", "English": "en", "pt-br": "pt-BR",
		"fre": "fr", "ger": "de", "spa": "es", "i-klingon": "tlh",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rewrites = %v, want %v", got, want)
	}
	// Values the canonicalizer rejects are reported, never rewritten: the
	// migration cannot restore what it overwrites.
	wantUnparseable := []string{"forced", "Монгол"}
	if !reflect.DeepEqual(unparseable, wantUnparseable) {
		t.Fatalf("unparseable = %v, want %v", unparseable, wantUnparseable)
	}
}

// TestPostgresSubtitleLanguageBackfill runs the real goose provider, which
// registers the Go migration, then exercises the backfill directly over seeded
// rows spanning more than one id batch.
func TestPostgresSubtitleLanguageBackfill(t *testing.T) {
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
		fmt.Sprintf("subtitle-language-backfill-%d", time.Now().UnixNano())).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID) })

	tracks := `[{"index":0,"language":"eng","codec":"subrip"},
	            {"index":1,"language":"en-US","codec":"subrip"},
	            {"index":2,"language":"zh-Hans","codec":"subrip"},
	            {"index":3,"language":"Spanish","codec":"subrip"},
	            {"index":4,"language":"","codec":"subrip"},
	            {"index":5,"codec":"subrip"},
	            {"index":6,"language":"forced","codec":"subrip"}]`
	external := `[{"path":"/m.fre.srt","language":"fre","format":"srt"},
	              {"path":"/m.pt-br.srt","language":"pt-br","format":"srt"},
	              {"path":"/m.sr-Latn.srt","language":"sr-Latn","format":"srt"}]`
	var seededID int
	if err := pool.QueryRow(ctx, `
INSERT INTO media_files (media_folder_id, file_path, base_type, file_size, subtitle_tracks, external_subtitles)
VALUES ($1, $2, 'movie', 0, $3::jsonb, $4::jsonb) RETURNING id`,
		folderID, fmt.Sprintf("/subtitle-language-backfill/%d/mixed.mkv", time.Now().UnixNano()),
		tracks, external).Scan(&seededID); err != nil {
		t.Fatalf("seed mixed file: %v", err)
	}
	var canonicalID int
	if err := pool.QueryRow(ctx, `
INSERT INTO media_files (media_folder_id, file_path, base_type, file_size, subtitle_tracks, external_subtitles, updated_at)
VALUES ($1, $2, 'movie', 0, '[{"index":0,"language":"en"}]'::jsonb, NULL, '2020-01-01T00:00:00Z') RETURNING id`,
		folderID, fmt.Sprintf("/subtitle-language-backfill/%d/canonical.mkv", time.Now().UnixNano())).Scan(&canonicalID); err != nil {
		t.Fatalf("seed canonical file: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE id = ANY($1)`, []int{seededID, canonicalID})
	})

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := backfillSubtitleLanguages(ctx, sqlDB); err != nil {
		t.Fatalf("backfillSubtitleLanguages: %v", err)
	}

	languages := func(column string, id int) []map[string]any {
		t.Helper()
		var raw []byte
		if err := pool.QueryRow(ctx, `SELECT `+column+` FROM media_files WHERE id = $1`, id).Scan(&raw); err != nil {
			t.Fatalf("reading %s: %v", column, err)
		}
		var elems []map[string]any
		if err := json.Unmarshal(raw, &elems); err != nil {
			t.Fatalf("decoding %s %s: %v", column, raw, err)
		}
		return elems
	}

	t.Run("embedded tracks take the scanner's canonical form", func(t *testing.T) {
		elems := languages("subtitle_tracks", seededID)
		want := []any{"en", "en-US", "zh-Hans", "es", "", nil, "forced"}
		if len(elems) != len(want) {
			t.Fatalf("track count = %d, want %d", len(elems), len(want))
		}
		for i, elem := range elems {
			if got := elem["language"]; got != want[i] {
				t.Errorf("track %d language = %v, want %v", i, got, want[i])
			}
			if elem["codec"] != "subrip" || elem["index"] != float64(i) {
				t.Errorf("track %d lost sibling fields: %v", i, elem)
			}
		}
	})

	t.Run("external sidecars keep region and script", func(t *testing.T) {
		elems := languages("external_subtitles", seededID)
		want := []string{"fr", "pt-BR", "sr-Latn"}
		for i, elem := range elems {
			if elem["language"] != want[i] {
				t.Errorf("sidecar %d language = %v, want %q", i, elem["language"], want[i])
			}
		}
	})

	t.Run("already-canonical rows are not rewritten", func(t *testing.T) {
		var updatedAt time.Time
		if err := pool.QueryRow(ctx, `SELECT updated_at FROM media_files WHERE id = $1`, canonicalID).Scan(&updatedAt); err != nil {
			t.Fatalf("reading updated_at: %v", err)
		}
		if updatedAt.Year() != 2020 {
			t.Errorf("canonical row was touched: updated_at = %s", updatedAt)
		}
	})

	t.Run("second pass finds nothing to change", func(t *testing.T) {
		if err := backfillSubtitleLanguages(ctx, sqlDB); err != nil {
			t.Fatalf("second backfillSubtitleLanguages: %v", err)
		}
	})
}
