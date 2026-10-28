package catalog

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// artworkLockFixture is a series with one season and one episode, each
// showing artwork, read the way a refresh reads them before it writes.
type artworkLockFixture struct {
	pool     *pgxpool.Pool
	items    *ItemRepository
	seasons  *SeasonRepository
	episodes *EpisodeRepository
	details  *DetailService
	seriesID string
	item     *models.MediaItem
	season   *models.Season
	episode  *models.Episode
}

func newArtworkLockFixture(t *testing.T, name string) *artworkLockFixture {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)
	f := &artworkLockFixture{
		pool:     pool,
		items:    NewItemRepository(pool),
		seasons:  NewSeasonRepository(pool),
		episodes: NewEpisodeRepository(pool),
		seriesID: fmt.Sprintf("%s-%d", name, time.Now().UnixNano()),
	}
	f.details = NewDetailService(f.items, f.episodes, f.seasons, NewPersonRepository(pool), noFileVersions{})
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, f.seriesID)
		_, _ = pool.Exec(ctx, `DELETE FROM artwork_revision_gc_candidates WHERE original_path LIKE $1`, "test-lock/"+f.seriesID+"/%")
	})

	if err := f.items.Upsert(ctx, &models.MediaItem{
		ContentID: f.seriesID, Type: "series", Title: "Show", Status: "matched",
		PosterPath: f.path("old-poster"), PosterSourcePath: "https://img.example/old-poster.jpg",
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.seasons.Upsert(ctx, &models.Season{
		ContentID: f.seriesID + "-s1", SeriesID: f.seriesID, SeasonNumber: 1, PosterPath: f.path("old-season"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.episodes.Upsert(ctx, &models.Episode{
		ContentID: f.seriesID + "-s1e1", SeriesID: f.seriesID, SeasonNumber: 1, EpisodeNumber: 1, StillPath: f.path("old-still"),
	}); err != nil {
		t.Fatal(err)
	}
	f.item, f.season, f.episode = f.read(t)
	return f
}

func (f *artworkLockFixture) path(name string) string {
	return "test-lock/" + f.seriesID + "/" + name + "/original.r.webp"
}

func (f *artworkLockFixture) read(t *testing.T) (*models.MediaItem, *models.Season, *models.Episode) {
	t.Helper()
	ctx := context.Background()
	item, err := f.items.GetByID(ctx, f.seriesID)
	if err != nil {
		t.Fatal(err)
	}
	season, err := f.seasons.GetBySeriesAndNumber(ctx, f.seriesID, 1)
	if err != nil {
		t.Fatal(err)
	}
	episode, err := f.episodes.GetBySeriesAndNumber(ctx, f.seriesID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	return item, season, episode
}

func (f *artworkLockFixture) seasonSelection() ArtworkSelection {
	return ArtworkSelection{
		TargetType: artworkTargetSeason, TargetContentID: f.season.ContentID, ParentContentID: f.seriesID,
		ImageType: "poster", StoredPath: f.path("picked-season"), SourcePath: "upload://season", LockField: fieldImagesLocked,
	}
}

func (f *artworkLockFixture) episodeSelection() ArtworkSelection {
	return ArtworkSelection{
		TargetType: artworkTargetEpisode, TargetContentID: f.episode.ContentID, ParentContentID: f.seriesID,
		ImageType: "still", StoredPath: f.path("picked-still"), SourcePath: "upload://still", LockField: fieldImagesLocked,
	}
}

// A refresh decides what to keep from the item it read. An admin image
// published after that read and before the refresh's write locks the images,
// so the write must keep the published image instead of restoring the one it
// read, and must not queue the published image for cleanup. A locked slot
// that is still empty fills, as the refresh merge does.
func TestRefreshKeepsArtworkPublishedAfterItsReadDB(t *testing.T) {
	f := newArtworkLockFixture(t, "artwork-lock")
	ctx := context.Background()
	for _, sel := range []ArtworkSelection{
		{TargetContentID: f.seriesID, ImageType: "poster", StoredPath: f.path("picked-poster"), SourcePath: "upload://poster", LockField: fieldImagesLocked},
		f.seasonSelection(),
		{
			TargetType: artworkTargetSeason, TargetContentID: f.season.ContentID, ParentContentID: f.seriesID,
			ImageType: "backdrop", StoredPath: f.path("picked-season-backdrop"), SourcePath: "upload://season-backdrop", LockField: fieldImagesLocked,
		},
		f.episodeSelection(),
	} {
		if err := f.details.PublishArtworkSelection(ctx, sel); err != nil {
			t.Fatalf("publish %s %s: %v", sel.TargetType, sel.ImageType, err)
		}
	}

	// The refresh writes what it read, plus a logo for the empty slot.
	f.item.LogoPath = f.path("new-logo")
	for _, write := range []struct {
		name string
		run  func() error
	}{
		{"item upsert", func() error { return f.items.Upsert(ctx, f.item) }},
		{"season upsert", func() error { return f.seasons.Upsert(ctx, f.season) }},
		{"season bulk upsert", func() error { return f.seasons.BulkUpsert(ctx, []*models.Season{f.season}) }},
		{"episode upsert", func() error { return f.episodes.Upsert(ctx, f.episode) }},
		{"episode bulk upsert", func() error { return f.episodes.BulkUpsert(ctx, f.seriesID, []*models.Episode{f.episode}) }},
	} {
		if err := write.run(); err != nil {
			t.Fatalf("%s: %v", write.name, err)
		}
	}

	item, season, episode := f.read(t)
	if item.PosterPath != f.path("picked-poster") || item.PosterSourcePath != "upload://poster" {
		t.Errorf("item poster = %q from %q, want the published one", item.PosterPath, item.PosterSourcePath)
	}
	if item.LogoPath != f.path("new-logo") {
		t.Errorf("item logo = %q, want the empty locked slot filled", item.LogoPath)
	}
	if season.PosterPath != f.path("picked-season") || season.PosterSourcePath != "upload://season" {
		t.Errorf("season poster = %q from %q, want the published one", season.PosterPath, season.PosterSourcePath)
	}
	// Refresh never writes a season backdrop, so the published one stays
	// whether or not the images lock holds.
	if season.BackdropPath != f.path("picked-season-backdrop") || season.BackdropSourcePath != "upload://season-backdrop" {
		t.Errorf("season backdrop = %q from %q, want the published one", season.BackdropPath, season.BackdropSourcePath)
	}
	if episode.StillPath != f.path("picked-still") || episode.StillSourcePath != "upload://still" {
		t.Errorf("episode still = %q from %q, want the published one", episode.StillPath, episode.StillSourcePath)
	}
	for _, name := range []string{"picked-poster", "picked-season", "picked-season-backdrop", "picked-still"} {
		var due bool
		if err := f.pool.QueryRow(ctx, `SELECT next_attempt_at IS NOT NULL FROM artwork_revision_gc_candidates WHERE original_path = $1`, f.path(name)).Scan(&due); err != nil {
			t.Fatalf("%s revision: %v", name, err)
		}
		if due {
			t.Errorf("%s is queued for cleanup while the catalog shows it", name)
		}
	}
}

// A refresh write that starts while a publication of the same season or
// episode is still committing waits for it, and must then see the images lock
// the publication set on the series rather than its own older snapshot.
func TestRefreshWaitingOnAPublicationKeepsItDB(t *testing.T) {
	f := newArtworkLockFixture(t, "artwork-lock-wait")
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		sel   ArtworkSelection
		write func() error
		shown func(*models.Season, *models.Episode) string
	}{
		{
			name:  "season",
			sel:   f.seasonSelection(),
			write: func() error { return f.seasons.BulkUpsert(ctx, []*models.Season{f.season}) },
			shown: func(s *models.Season, _ *models.Episode) string { return s.PosterPath },
		},
		{
			name:  "episode",
			sel:   f.episodeSelection(),
			write: func() error { return f.episodes.BulkUpsert(ctx, f.seriesID, []*models.Episode{f.episode}) },
			shown: func(_ *models.Season, e *models.Episode) string { return e.StillPath },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `UPDATE media_items SET locked_fields = '{}' WHERE content_id = $1`, f.seriesID); err != nil {
				t.Fatal(err)
			}
			// Holding the series row stops the publication after it has
			// written the season or episode, before it locks the images.
			hold, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = hold.Rollback(ctx) }()
			var holdPID uint32
			if err := hold.QueryRow(ctx, `SELECT pg_backend_pid() FROM media_items WHERE content_id = $1 FOR SHARE`, f.seriesID).Scan(&holdPID); err != nil {
				t.Fatal(err)
			}

			published := make(chan error, 1)
			go func() { published <- f.details.PublishArtworkSelection(ctx, tc.sel) }()
			publisher := waitForLockWaiter(t, f.pool, holdPID)
			written := make(chan error, 1)
			go func() { written <- tc.write() }()
			waitForLockWaiter(t, f.pool, publisher)

			if err := hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-published; err != nil {
				t.Fatalf("publish: %v", err)
			}
			if err := <-written; err != nil {
				t.Fatalf("write: %v", err)
			}
			_, season, episode := f.read(t)
			if got := tc.shown(season, episode); got != tc.sel.StoredPath {
				t.Fatalf("%s artwork = %q, want the published %q", tc.name, got, tc.sel.StoredPath)
			}
		})
	}
}

// waitForLockWaiter waits until a session waits on a lock pid holds and
// returns that session's pid.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool, pid uint32) uint32 {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiter uint32
		err := pool.QueryRow(context.Background(),
			`SELECT pid FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, int32(pid)).Scan(&waiter)
		if err == nil {
			return waiter
		}
		time.Sleep(10 * time.Millisecond) //nolint:forbidigo // polls pg_stat_activity until the lock wait is observable
	}
	t.Fatalf("no session waited on %d", pid)
	return 0
}

// A season or episode the refresh's write would insert can be created and
// published by another writer before the write reaches it. The write then
// updates a row it never locked, and must still see the images lock that
// writer committed.
func TestRefreshKeepsArtworkOnARowCreatedAndPublishedMeanwhileDB(t *testing.T) {
	f := newArtworkLockFixture(t, "artwork-lock-created")
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		create string
		write  func() error
		shown  func() (string, error)
	}{
		{
			name: "season",
			create: `INSERT INTO seasons (content_id, series_id, season_number, poster_path, poster_source_path, poster_thumbhash)
				VALUES ($1 || '-s2', $1, 2, $2, '', '')`,
			write: func() error {
				return f.seasons.BulkUpsert(ctx, []*models.Season{{SeriesID: f.seriesID, SeasonNumber: 2, PosterPath: f.path("refresh-season")}})
			},
			shown: func() (string, error) {
				s, err := f.seasons.GetBySeriesAndNumber(ctx, f.seriesID, 2)
				if err != nil {
					return "", err
				}
				return s.PosterPath, nil
			},
		},
		{
			name: "episode",
			create: `INSERT INTO episodes (content_id, series_id, season_number, episode_number, still_path, still_source_path, still_thumbhash)
				VALUES ($1 || '-s1e2', $1, 1, 2, $2, '', '')`,
			write: func() error {
				return f.episodes.BulkUpsert(ctx, f.seriesID, []*models.Episode{{SeriesID: f.seriesID, SeasonNumber: 1, EpisodeNumber: 2, StillPath: f.path("refresh-still")}})
			},
			shown: func() (string, error) {
				e, err := f.episodes.GetBySeriesAndNumber(ctx, f.seriesID, 1, 2)
				if err != nil {
					return "", err
				}
				return e.StillPath, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.pool.Exec(ctx, `UPDATE media_items SET locked_fields = '{}' WHERE content_id = $1`, f.seriesID); err != nil {
				t.Fatal(err)
			}
			// The other writer creates the row and publishes its artwork in
			// one transaction, so the refresh's lock query cannot see the row
			// and its insert waits on it.
			other, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = other.Rollback(ctx) }()
			picked := f.path("picked-" + tc.name)
			if _, err := other.Exec(ctx, tc.create, f.seriesID, picked); err != nil {
				t.Fatal(err)
			}
			if _, err := other.Exec(ctx, `UPDATE media_items SET locked_fields = ARRAY[$2::int] WHERE content_id = $1`, f.seriesID, fieldImagesLocked); err != nil {
				t.Fatal(err)
			}
			var otherPID uint32
			if err := other.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&otherPID); err != nil {
				t.Fatal(err)
			}

			written := make(chan error, 1)
			go func() { written <- tc.write() }()
			waitForLockWaiter(t, f.pool, otherPID)
			if err := other.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-written; err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := tc.shown()
			if err != nil {
				t.Fatal(err)
			}
			if got != picked {
				t.Fatalf("%s artwork = %q, want the published %q", tc.name, got, picked)
			}
		})
	}
}
