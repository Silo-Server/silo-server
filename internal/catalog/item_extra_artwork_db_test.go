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

// noFileVersions is a FileVersionFetcher for items without files.
type noFileVersions struct{}

func (noFileVersions) GetByContentID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
}

func (noFileVersions) GetByEpisodeID(context.Context, string) ([]*models.MediaFile, error) {
	return nil, nil
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

	// Batch detail, which Jellyfin lists read, carries the paths without
	// resolving a URL for each slot; single-item detail returns the URLs.
	resolver := &countingResolver{}
	details := NewDetailService(repo, NewEpisodeRepository(pool), NewSeasonRepository(pool), NewPersonRepository(pool), noFileVersions{})
	details.imageResolver = resolver
	batch, err := details.GetItemDetailsByIDs(ctx, []string{contentID}, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if d := batch[contentID]; d == nil || d.BannerPath != got.BannerPath || d.DiscPath != got.DiscPath || d.BannerURL != "" || d.DiscURL != "" || resolver.calls != 0 {
		t.Fatalf("batch detail = %+v after %d resolves, want the paths and no URLs", d, resolver.calls)
	}
	single, err := details.GetItemDetail(ctx, contentID, AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if single.BannerURL == "" || single.ClearArtURL == "" || single.BannerThumbhash != "th-banner" {
		t.Fatalf("single detail banner %q/%q, clear art %q", single.BannerURL, single.BannerThumbhash, single.ClearArtURL)
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

// A cache job that writes a backdrop row while a refresh replaces the item's
// backdrops finishes first, and the copy it wrote moves with its source.
func TestItemBackdropReplaceWaitsForCacheWriteDB(t *testing.T) {
	pool := newExtraArtworkTestPool(t)
	ctx := context.Background()
	items := NewItemRepository(pool)
	repo := NewItemBackdropRepository(pool)
	contentID := fmt.Sprintf("extra-backdrops-race-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, contentID)
	})
	if err := items.Upsert(ctx, &models.MediaItem{ContentID: contentID, Type: "movie", Title: "Film", Status: "matched"}); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{{SourcePath: "file:///m/fanart-1.jpg"}, {SourcePath: "file:///m/fanart-2.jpg"}})
	if err != nil || len(rows) != 2 {
		t.Fatalf("seed = %+v, %v", rows, err)
	}

	// The cache job's write for fanart-1 is in flight.
	worker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.Rollback(context.Background()) }()
	const cachedOne = "test-extra/race/a/backdrop/original.r.webp"
	if _, err := worker.Exec(ctx, `UPDATE media_item_backdrops SET path = $2, thumbhash = 'ta' WHERE id = $1`, rows[0].ID, cachedOne); err != nil {
		t.Fatal(err)
	}
	var workerPID int
	if err := worker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&workerPID); err != nil {
		t.Fatal(err)
	}

	// A refresh that read the rows earlier inserts a new first backdrop, so
	// fanart-1 moves to ordinal 2.
	type result struct {
		rows []models.ItemBackdrop
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rows, err := repo.Replace(ctx, contentID, []models.ItemBackdrop{
			{SourcePath: "file:///m/fanart-0.jpg"},
			{SourcePath: "file:///m/fanart-1.jpg"},
			{SourcePath: "file:///m/fanart-2.jpg"},
		})
		done <- result{rows, err}
	}()
	// Wait until Replace is blocked by the worker's row lock.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE pg_blocking_pids(pid) @> ARRAY[$1::int]`, workerPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("Replace finished while a cache write was in flight: %+v, %v", r.rows, r.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Replace never waited for the in-flight cache write")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := worker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.rows) != 3 || r.rows[1].SourcePath != "file:///m/fanart-1.jpg" || r.rows[1].Path != cachedOne || r.rows[1].Thumbhash != "ta" {
		t.Fatalf("rows = %+v, want fanart-1 at ordinal 2 with the copy its cache job wrote", r.rows)
	}
}
