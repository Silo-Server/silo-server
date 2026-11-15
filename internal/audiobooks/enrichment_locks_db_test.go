package audiobooks

import (
	"os"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
)

// A match found on an enrichment retry must not overwrite what an admin
// edited and locked while the book was unmatched; fields left unlocked still
// take the provider's values.
func TestPersistKeepsAdminLockedFieldsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	contentID := "ab-locks-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, overview, tagline, genres, year, locked_fields)
		VALUES ($1, 'audiobook', 'Book', 'unmatched', 'Admin overview', 'Admin tagline', '{Admin Genre}', 2020, $2)
	`, contentID, []int{int(metadata.FieldOverview), int(metadata.FieldGenres), int(metadata.FieldReleaseDates)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })

	e := &Enricher{pool: pool, itemRepo: catalog.NewItemRepository(pool), state: newEnrichmentStateStore(pool)}
	if err := e.persist(ctx, enrichmentItemRow{ContentID: contentID}, nil, &metadata.MetadataResult{
		HasMetadata: true,
		Overview:    "Provider overview",
		Tagline:     "Provider tagline",
		Genres:      []string{"Provider Genre"},
		Year:        1999,
		ReleaseDate: "1999-01-02",
		Studios:     []string{"Provider Press"},
	}); err != nil {
		t.Fatal(err)
	}

	var overview, tagline string
	var genres, studios []string
	var year int
	var releaseDate *string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(overview, ''), COALESCE(tagline, ''), genres, studios, year, release_date::text
		FROM media_items WHERE content_id = $1
	`, contentID).Scan(&overview, &tagline, &genres, &studios, &year, &releaseDate); err != nil {
		t.Fatal(err)
	}
	if overview != "Admin overview" || tagline != "Admin tagline" {
		t.Errorf("overview, tagline = %q, %q; want the locked admin values", overview, tagline)
	}
	if !slices.Equal(genres, []string{"Admin Genre"}) {
		t.Errorf("genres = %v, want the locked admin value", genres)
	}
	if year != 2020 || releaseDate != nil {
		t.Errorf("year, release_date = %d, %v; want 2020 and no date, as locked", year, releaseDate)
	}
	if !slices.Equal(studios, []string{"Provider Press"}) {
		t.Errorf("studios = %v, want the provider value for the unlocked field", studios)
	}
}
