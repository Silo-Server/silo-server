package sections

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Jellyfin-compatible lists built from section rows (/Items/Latest) advertise
// banner, thumb, clear art and disc art from these paths, so the shared select
// list must carry them; scanMediaItems reads them last.
func TestItemColumnsListCarriesExtraArtworkPaths(t *testing.T) {
	cols := itemColumnsList("mi")
	want := []string{"mi.banner_path", "mi.thumb_path", "mi.clearart_path", "mi.disc_path"}
	if got := cols[len(cols)-len(want):]; !slices.Equal(got, want) {
		t.Fatalf("last columns = %q, want %q", got, want)
	}
}

// Section cards are localized per viewer, and localization compares the
// target language with the item's own metadata and original languages. A
// section row without them reports every card as untranslated, even in its
// source language.
func TestScanMediaItemsCarriesMetadataLanguagesDB(t *testing.T) {
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

	id := fmt.Sprintf("section-languages-%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx,
		`INSERT INTO media_items (content_id, type, title, status, genres, default_metadata_language, original_language)
		 VALUES ($1, 'movie', 'Movie', 'matched', '{}'::text[], 'de', 'ja')`, id,
	); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, id) })

	for name, columns := range map[string]string{
		"itemColumns":                  itemColumns("mi"),
		"itemColumnsLatestMangaPoster": itemColumnsLatestMangaPoster("mi"),
	} {
		rows, err := pool.Query(ctx, `SELECT `+columns+` FROM media_items mi WHERE mi.content_id = $1`, id)
		if err != nil {
			t.Fatalf("%s: query: %v", name, err)
		}
		items, err := scanMediaItems(rows)
		rows.Close()
		if err != nil {
			t.Fatalf("%s: scan: %v", name, err)
		}
		if len(items) != 1 || items[0].DefaultMetadataLanguage != "de" || items[0].OriginalLanguage != "ja" {
			t.Fatalf("%s: scanned %+v, want metadata language de and original language ja", name, items)
		}
	}
}
