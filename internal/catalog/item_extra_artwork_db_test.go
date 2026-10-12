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

func newExtraArtworkTestPool(t *testing.T) *pgxpool.Pool {
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

// The banner, thumb, clear art and disc slots round-trip through the shared
// item projection, and the image cache writes each slot only while the row
// still holds the source it read.
func TestItemExtraArtworkSlotsRoundTripDB(t *testing.T) {
	pool := newExtraArtworkTestPool(t)
	ctx := context.Background()
	repo := NewItemRepository(pool)
	contentID := fmt.Sprintf("extra-art-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID) })

	item := &models.MediaItem{
		ContentID: contentID, Type: "movie", Title: "Film", Status: "matched",
		BannerSourcePath: "file:///m/banner.jpg?v=1-1", ThumbSourcePath: "file:///m/landscape.jpg?v=1-1",
		ClearArtSourcePath: "file:///m/clearart.png?v=1-1", DiscSourcePath: "file:///m/disc.png?v=1-1",
	}
	if err := repo.Upsert(ctx, item); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for _, slot := range []struct{ imageType, source, cached string }{
		{"banner", item.BannerSourcePath, "local/movies/x/aa/banner/original.r.webp"},
		{"thumb", item.ThumbSourcePath, "local/movies/x/bb/thumb/original.r.webp"},
		{"clearart", item.ClearArtSourcePath, "local/movies/x/cc/clearart/original.r.webp"},
		{"disc", item.DiscSourcePath, "local/movies/x/dd/disc/original.r.webp"},
	} {
		if ok, err := repo.UpdateArtworkIfSourceMatches(ctx, contentID, slot.imageType, "file:///m/other.jpg", slot.cached, "th"); err != nil || ok {
			t.Fatalf("%s update with a stale source = %v, %v; want no write", slot.imageType, ok, err)
		}
		if ok, err := repo.UpdateArtworkIfSourceMatches(ctx, contentID, slot.imageType, slot.source, slot.cached, "th-"+slot.imageType); err != nil || !ok {
			t.Fatalf("%s update = %v, %v", slot.imageType, ok, err)
		}
	}
	got, err := repo.GetByID(ctx, contentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BannerPath != "local/movies/x/aa/banner/original.r.webp" || got.BannerThumbhash != "th-banner" ||
		got.ThumbPath != "local/movies/x/bb/thumb/original.r.webp" || got.ThumbThumbhash != "th-thumb" ||
		got.ClearArtPath != "local/movies/x/cc/clearart/original.r.webp" ||
		got.DiscPath != "local/movies/x/dd/disc/original.r.webp" ||
		got.BannerSourcePath != item.BannerSourcePath || got.DiscSourcePath != item.DiscSourcePath {
		t.Fatalf("stored extra artwork = %+v", got)
	}
}

func TestItemBackdropRepositoryDB(t *testing.T) {
	pool := newExtraArtworkTestPool(t)
	ctx := context.Background()
	items := NewItemRepository(pool)
	repo := NewItemBackdropRepository(pool)
	contentID := fmt.Sprintf("extra-backdrops-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, contentID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "test-extra/"+contentID+"/%")
	})
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "movie", Title: "Film", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	cached := func(name string) string { return "test-extra/" + contentID + "/" + name + "/backdrop/original.r.webp" }

	first, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{
		{SourcePath: "file:///m/fanart-1.jpg", Path: cached("a"), Thumbhash: "ta"},
		{SourcePath: "file:///m/fanart-2.jpg"},
		{SourcePath: "file:///m/extrafanart/x.jpg", Path: cached("c")},
	})
	if err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if len(first) != 3 || first[0].Ordinal != 1 || first[2].Ordinal != 3 || first[0].Path != cached("a") || first[0].Thumbhash != "ta" {
		t.Fatalf("stored rows = %+v", first)
	}

	// A shorter list keeps each surviving ordinal's row ID, so cache jobs
	// that target it stay valid, and drops the rest.
	second, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{
		{SourcePath: "file:///m/fanart-1.jpg", Path: cached("a"), Thumbhash: "ta"},
		{SourcePath: "file:///m/fanart-3.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || second[0].ID != first[0].ID || second[1].ID != first[1].ID || second[1].SourcePath != "file:///m/fanart-3.jpg" {
		t.Fatalf("rows after shrinking = %+v (before %+v)", second, first)
	}
	// The dropped row's cached copy is queued for the revision GC.
	var queued int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path = $1 AND image_type = 'backdrop'`, cached("c")).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("GC candidates for the dropped backdrop = %d, %v", queued, err)
	}

	if ok, err := repo.UpdateArtworkIfSourceMatches(ctx, second[1].ID, "file:///m/fanart-2.jpg", cached("b"), "tb"); err != nil || ok {
		t.Fatalf("update with the replaced source = %v, %v; want no write", ok, err)
	}
	if ok, err := repo.UpdateArtworkIfSourceMatches(ctx, second[1].ID, "file:///m/fanart-3.jpg", cached("b"), "tb"); err != nil || !ok {
		t.Fatalf("update = %v, %v", ok, err)
	}
	listed, err := repo.ListByContentIDs(ctx, []string{contentID, "no-such-item"})
	if err != nil {
		t.Fatal(err)
	}
	if rows := listed[contentID]; len(rows) != 2 || rows[1].Path != cached("b") || rows[1].Thumbhash != "tb" || len(listed) != 1 {
		t.Fatalf("listed = %+v", listed)
	}

	// A refresh that read the rows before the cache job wrote keeps the job's
	// cached copy for a row whose source it did not change.
	stale, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{
		{SourcePath: "file:///m/fanart-1.jpg", Path: cached("a"), Thumbhash: "ta"},
		{SourcePath: "file:///m/fanart-3.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 2 || stale[1].Path != cached("b") || stale[1].Thumbhash != "tb" {
		t.Fatalf("rows after a stale refresh = %+v, want the cached copy kept", stale)
	}

	// A source that moves to another ordinal takes its stored cached copy
	// along, even when the caller's copy predates the cache job's write.
	shifted, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{
		{SourcePath: "file:///m/fanart-0.jpg"},
		{SourcePath: "file:///m/fanart-1.jpg"},
		{SourcePath: "file:///m/fanart-3.jpg"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(shifted) != 3 || shifted[0].Path != "" || shifted[1].Path != cached("a") || shifted[1].Thumbhash != "ta" ||
		shifted[2].Path != cached("b") || shifted[2].Thumbhash != "tb" {
		t.Fatalf("rows after shifting = %+v, want each source's cached copy to move with it", shifted)
	}

	// Deleting the item deletes its backdrops and queues their cached copies.
	if _, err := items.Delete(ctx, contentID); err != nil {
		t.Fatal(err)
	}
	if listed, err := repo.ListByContentIDs(ctx, []string{contentID}); err != nil || len(listed) != 0 {
		t.Fatalf("backdrops after deleting the item = %+v, %v", listed, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM artwork_revision_gc_candidates WHERE original_path IN ($1, $2)`, cached("a"), cached("b")).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("GC candidates after deleting the item = %d, %v", queued, err)
	}
}
