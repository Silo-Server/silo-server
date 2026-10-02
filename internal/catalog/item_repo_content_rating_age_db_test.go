package catalog

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestItemRepoWritesContentRatingAgeDB covers the write side of the stored
// maturity age: an upsert derives it from the raw rating, and a later metadata
// edit rewrites it. Leaving a stale age behind would keep filtering a title by
// the certification it no longer carries.
func TestItemRepoWritesContentRatingAgeDB(t *testing.T) {
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

	contentID := fmt.Sprintf("rating-age-write-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	repo := NewItemRepository(pool)
	stored := func(t *testing.T) *int {
		t.Helper()
		var age *int
		if err := pool.QueryRow(ctx,
			`SELECT content_rating_age FROM media_items WHERE content_id = $1`,
			contentID,
		).Scan(&age); err != nil {
			t.Fatalf("reading stored rating: %v", err)
		}
		return age
	}

	// Kodi writes the country-prefixed form; the age is what makes it
	// comparable with any other system's ceiling.
	item := &models.MediaItem{ContentID: contentID, Type: "movie", Title: "Rating Age", ContentRating: "DE:16"}
	if err := repo.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if age := stored(t); age == nil || *age != 16 {
		t.Fatalf("after upsert age = %v, want 16", age)
	}

	rating := "PG-13"
	if err := repo.UpdateMetadata(ctx, contentID, &MetadataUpdate{ContentRating: &rating}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if age := stored(t); age == nil || *age != 13 {
		t.Fatalf("after edit age = %v, want 13", age)
	}

	// An explicit "not rated" edit clears the age: the row carries none, and
	// content_rating keeps the string the client displays.
	rating = "NR"
	if err := repo.UpdateMetadata(ctx, contentID, &MetadataUpdate{ContentRating: &rating}); err != nil {
		t.Fatalf("UpdateMetadata to NR: %v", err)
	}
	if age := stored(t); age != nil {
		t.Fatalf("after clearing to NR age = %v, want nil", age)
	}
}
