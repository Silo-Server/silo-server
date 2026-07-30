package catalog

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newVirtualMediaTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to connect to db: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// Clean out related tables for isolation.
	pool.Exec(ctx, "DELETE FROM media_files")
	pool.Exec(ctx, "TRUNCATE public.episodes CASCADE")
	pool.Exec(ctx, "DELETE FROM seasons")
	pool.Exec(ctx, "DELETE FROM media_item_libraries")
	pool.Exec(ctx, "DELETE FROM media_items")
	pool.Exec(ctx, "DELETE FROM media_folders")

	return pool
}

func TestVirtualMediaVariantsUpsert(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	reg := NewVirtualMediaRegistrar(pool)

	pool.Exec(ctx, "INSERT INTO media_folders(id,name,type,enabled) VALUES(999,'TestVirtual','mixed',true)")

	in := VirtualMedia{
		LibraryID: "999", MediaType: "movie", Title: "Test Movie", TMDBID: "m1",
		RuntimeMinutes: 120,
		Variants: []VirtualMediaVariant{
			{VirtualURI: "virtual://m1/1080p", Resolution: "1080p", CodecVideo: "h264", RuntimeMinutes: 120},
			{VirtualURI: "virtual://m1/4k", Resolution: "4k", CodecVideo: "hevc", HDR: "hdr10", RuntimeMinutes: 120},
		},
	}
	res, err := reg.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	// Verify two virtual files were inserted and default container is "virtual"
	var count int
	pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1 AND container='virtual'", res.MediaID).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 variants, got %d", count)
	}

	// Test variant with supplied container and file size
	inWithSupplied := VirtualMedia{
		LibraryID: "999", MediaType: "movie", Title: "Custom Variant Movie", TMDBID: "m2",
		RuntimeMinutes: 120,
		Variants: []VirtualMediaVariant{
			{VirtualURI: "virtual://m2/1080p", Resolution: "1080p", CodecVideo: "h264", Container: "mkv", FileSize: 104857600},
		},
	}
	resSupplied, err := reg.Upsert(ctx, inWithSupplied)
	if err != nil {
		t.Fatalf("upsert custom variant failed: %v", err)
	}
	var container string
	var fileSize int64
	err = pool.QueryRow(ctx, "SELECT container, file_size FROM media_files WHERE content_id=$1 AND file_path=$2", resSupplied.MediaID, "virtual://m2/1080p").Scan(&container, &fileSize)
	if err != nil {
		t.Fatalf("failed to query custom variant file: %v", err)
	}
	if container != "virtual" {
		t.Fatalf("expected container 'virtual', got %q", container)
	}
	if fileSize != 104857600 {
		t.Fatalf("expected file_size 104857600, got %d", fileSize)
	}

	// Verify idempotency
	in.Overview = "Updated overview"
	_, err = reg.Upsert(ctx, in)
	if err != nil {
		t.Fatalf("second upsert failed: %v", err)
	}
	pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", res.MediaID).Scan(&count)
	if count != 2 {
		t.Fatalf("idempotent upsert should not add duplicates, got %d", count)
	}
	var overview string
	pool.QueryRow(ctx, "SELECT overview FROM media_items WHERE content_id=$1", res.MediaID).Scan(&overview)
	if overview != "Updated overview" {
		t.Fatalf("expected 'Updated overview', got %q", overview)
	}

	// Test episode variants
	series := VirtualMedia{
		LibraryID: "999", MediaType: "series", Title: "Test Series", TMDBID: "s1",
		Episodes: []VirtualEpisode{
			{
				SeasonNumber: 1, EpisodeNumber: 1, Title: "Ep 1",
				Variants: []VirtualMediaVariant{
					{VirtualURI: "virtual://s1/1/1/a", Resolution: "1080p"},
					{VirtualURI: "virtual://s1/1/1/b", Resolution: "720p"},
				},
			},
		},
	}
	resSeries, err := reg.Upsert(ctx, series)
	if err != nil {
		t.Fatalf("upsert series failed: %v", err)
	}
	if resSeries.EpisodesUpserted != 1 {
		t.Fatalf("expected 1 episode, got %d", resSeries.EpisodesUpserted)
	}
	pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1", resSeries.MediaID).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 episode variants, got %d", count)
	}
}

// Installation-scoped purges must not delete rows belonging to another
// installation. Ownership currently lives on media_items (one owner per
// content ID); this test pins that invariant until per-file ownership is
// introduced.
func TestPurgeVirtualPlaybackItemsKeepsOtherInstallation(t *testing.T) {
	pool := newVirtualMediaTestPool(t)
	ctx := context.Background()
	const contentID = "movie-tmdb-purge-owner-b"
	if _, err := pool.Exec(ctx, `INSERT INTO media_folders(id,name,type,enabled) VALUES(998,'PurgeTest','mixed',true)`); err != nil {
		t.Fatalf("seed purge test folder: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_items(content_id,type,title,virtual_owner_installation_id,virtual_source) VALUES($1,'movie','Purge Test',22,'provider-b')`, contentID); err != nil {
		t.Fatalf("seed other-installation item: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_files(content_id,media_folder_id,file_path,file_size,container,probe_source) VALUES($1,998,'virtual://movie/purge-owner-b',0,'virtual','virtual')`, contentID); err != nil {
		t.Fatalf("seed other-installation virtual item: %v", err)
	}

	files, items, err := (&ItemRepository{pool: pool}).PurgeVirtualPlaybackItems(ctx, VirtualPurgeOptions{InstallationID: 11})
	if err != nil {
		t.Fatalf("scoped purge failed: %v", err)
	}
	if files != 0 || items != 0 {
		t.Fatalf("purge of installation 11 removed files=%d items=%d owned by installation 22", files, items)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE file_path='virtual://movie/purge-owner-b'").Scan(&count); err != nil {
		t.Fatalf("check preserved file: %v", err)
	}
	if count != 1 {
		t.Fatalf("other installation's virtual file count=%d, want 1", count)
	}
}
