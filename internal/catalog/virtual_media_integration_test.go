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

	// Verify two virtual files were inserted
	var count int
	pool.QueryRow(ctx, "SELECT count(*) FROM media_files WHERE content_id=$1 AND container='virtual'", res.MediaID).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 variants, got %d", count)
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
