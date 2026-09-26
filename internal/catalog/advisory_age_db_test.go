package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

func newAdvisoryAgeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func advisoryTestItem(contentID string) *models.MediaItem {
	age := 13
	return &models.MediaItem{
		ContentID:      contentID,
		Type:           "movie",
		Title:          "Jaws",
		SortTitle:      "jaws",
		Year:           1975,
		ContentRating:  "PG",
		AdvisoryAge:    &age,
		AdvisorySource: "commonsense",
		Status:         "matched",
	}
}

// TestAdvisoryAgeSurvivesWriteAndRead covers the column end to end: the write
// path stores both halves, every shared read projection returns them, and the
// advisory stays independent of the certification that drives ceilings.
func TestAdvisoryAgeSurvivesWriteAndRead(t *testing.T) {
	pool := newAdvisoryAgeTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("advisory-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	if err := repo.Upsert(ctx, advisoryTestItem(contentID)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	stored, err := repo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.AdvisoryAge == nil || *stored.AdvisoryAge != 13 {
		t.Fatalf("AdvisoryAge = %v, want 13", stored.AdvisoryAge)
	}
	if stored.AdvisorySource != "commonsense" {
		t.Fatalf("AdvisorySource = %q, want \"commonsense\"", stored.AdvisorySource)
	}

	// The advisory is display only. #1359's stored ceiling age comes from the
	// certification and nothing else, so a PG title stays at PG's age even
	// though its advisory says 13.
	var ceilingAge *int
	if err := pool.QueryRow(ctx,
		`SELECT content_rating_age FROM media_items WHERE content_id = $1`, contentID).Scan(&ceilingAge); err != nil {
		t.Fatalf("reading content_rating_age: %v", err)
	}
	if ceilingAge == nil || *ceilingAge != 8 {
		t.Fatalf("content_rating_age = %v, want 8 (PG); the advisory must never feed the ceiling", ceilingAge)
	}

	// Batched reads use the same shared column list as the single-item read.
	batch, err := repo.GetByIDs(ctx, []string{contentID})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("GetByIDs returned %d items, want 1", len(batch))
	}
	if batch[0].AdvisoryAge == nil || *batch[0].AdvisoryAge != 13 || batch[0].AdvisorySource != "commonsense" {
		t.Fatalf("batched advisory = (%v, %q), want (13, \"commonsense\")",
			batch[0].AdvisoryAge, batch[0].AdvisorySource)
	}
}

// TestAdvisoryAgeAbsentReadsAsNoAdvisory guards the nullable column against
// the plain-string scan on AdvisorySource: an item written without an advisory
// has to read back cleanly rather than failing on a NULL.
func TestAdvisoryAgeAbsentReadsAsNoAdvisory(t *testing.T) {
	pool := newAdvisoryAgeTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("advisory-none-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})

	item := advisoryTestItem(contentID)
	item.AdvisoryAge = nil
	item.AdvisorySource = ""
	if err := repo.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	stored, err := repo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.AdvisoryAge != nil {
		t.Fatalf("AdvisoryAge = %v, want nil", *stored.AdvisoryAge)
	}
	if stored.AdvisorySource != "" {
		t.Fatalf("AdvisorySource = %q, want empty", stored.AdvisorySource)
	}

	var rawAge, rawSource *string
	if err := pool.QueryRow(ctx,
		`SELECT advisory_age::text, advisory_source FROM media_items WHERE content_id = $1`,
		contentID).Scan(&rawAge, &rawSource); err != nil {
		t.Fatalf("reading raw advisory columns: %v", err)
	}
	// NULL, not a zero row: "no advisory" is one state in the column.
	if rawAge != nil || rawSource != nil {
		t.Fatalf("stored advisory = (%v, %v), want both NULL", rawAge, rawSource)
	}
}

// TestItemDetailKeepsAdvisoryOffTheV1Wire guards the frozen /api/v1 contract.
// ItemDetail is the v1 detail body as well as the input to the v2 renderer, so
// the advisory rides the Go struct with json:"-" the way OriginalLanguage does.
func TestItemDetailKeepsAdvisoryOffTheV1Wire(t *testing.T) {
	age := 13
	encoded, err := json.Marshal(ItemDetail{
		ContentID:      "advisory-1",
		Type:           "movie",
		Title:          "Jaws",
		ContentRating:  "PG",
		AdvisoryAge:    &age,
		AdvisorySource: "commonsense",
	})
	if err != nil {
		t.Fatalf("marshaling ItemDetail: %v", err)
	}
	for _, key := range []string{"advisory_age", "advisory_source", "AdvisoryAge", "AdvisorySource"} {
		if bytes.Contains(encoded, []byte(key)) {
			t.Fatalf("ItemDetail leaked %q onto the frozen v1 contract: %s", key, encoded)
		}
	}
	if !bytes.Contains(encoded, []byte(`"content_rating":"PG"`)) {
		t.Fatalf("ItemDetail lost content_rating: %s", encoded)
	}
}
