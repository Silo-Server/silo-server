package jellycompat

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

func jellyfin12CompatPool(t *testing.T) (*pgxpool.Pool, int64) {
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
	return pool, time.Now().UnixNano()
}

func jellyfin12Exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// TestApplyListMediaSourceCountsDB: list responses that request
// MediaSourceCount report the number of present versions, as the detail path
// does, instead of assuming one.
func TestApplyListMediaSourceCountsDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	var folderID int
	if err := pool.QueryRow(context.Background(), `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, fmt.Sprintf("jf12-msc-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	multi := fmt.Sprintf("jf12-multi-%d", suffix)
	single := fmt.Sprintf("jf12-single-%d", suffix)
	missing := fmt.Sprintf("jf12-missing-%d", suffix)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id = ANY($1)`, []string{multi, single, missing})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{multi, single, missing})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, id := range []string{multi, single, missing} {
		jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id)
	}
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path) VALUES ($1, $2, $1 || '-4k.mkv'), ($1, $2, $1 || '-1080p.mkv')`, multi, folderID)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $1 || '-gone.mkv', now())`, multi, folderID)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path) VALUES ($1, $2, $1 || '.mkv')`, single, folderID)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $1 || '.mkv', now())`, missing, folderID)

	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, browseRepo: catalog.NewBrowseRepository(pool), mapper: newMapper(codec, &config.Config{})}
	items := []baseItemDTO{
		{ID: codec.EncodeStringID(EncodedIDItem, multi), Type: "Movie", MediaSourceCount: 1},
		{ID: codec.EncodeStringID(EncodedIDItem, single), Type: "Movie", MediaSourceCount: 1},
		{ID: codec.EncodeStringID(EncodedIDItem, "series-x"), Type: "Series"},
		// The list mapper assumes one source for a matched item; with every
		// file gone the count must be left unset.
		{ID: codec.EncodeStringID(EncodedIDItem, missing), Type: "Movie", MediaSourceCount: 1},
	}
	h.applyListMediaSourceCounts(context.Background(), &Session{}, items, itemsQuery{requestedFields: map[string]bool{"mediasourcecount": true}})
	if items[0].MediaSourceCount != 2 || items[1].MediaSourceCount != 1 || items[2].MediaSourceCount != 0 || items[3].MediaSourceCount != 0 {
		t.Fatalf("MediaSourceCount = %d/%d/%d/%d, want 2/1/0/0", items[0].MediaSourceCount, items[1].MediaSourceCount, items[2].MediaSourceCount, items[3].MediaSourceCount)
	}

	items[0].MediaSourceCount = 1
	h.applyListMediaSourceCounts(context.Background(), &Session{}, items, itemsQuery{})
	if items[0].MediaSourceCount != 1 {
		t.Fatal("counts must only be computed when MediaSourceCount is requested")
	}
}

// TestEpisodeTargetSeasonPosterDB: the episode target loader carries the
// season poster so episode DTOs can point ParentPrimaryImage at the season.
func TestEpisodeTargetSeasonPosterDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	series := fmt.Sprintf("jf12-series-%d", suffix)
	withPoster := fmt.Sprintf("jf12-season1-%d", suffix)
	noPoster := fmt.Sprintf("jf12-season2-%d", suffix)
	ep1 := fmt.Sprintf("jf12-ep1-%d", suffix)
	ep2 := fmt.Sprintf("jf12-ep2-%d", suffix)
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = $1`, series)
		_, _ = pool.Exec(ctx, `DELETE FROM seasons WHERE series_id = $1`, series)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title, poster_path, genres, content_rating, backdrop_path, logo_path, status) VALUES ($1, 'series', 'Show', 'series/poster.jpg', '{}', '', '', '', 'matched')`, series)
	jellyfin12Exec(t, pool, `INSERT INTO seasons (content_id, series_id, season_number, title, poster_path, poster_thumbhash) VALUES ($1, $2, 1, 'Season 1', 'season1/poster.jpg', 'th1')`, withPoster, series)
	jellyfin12Exec(t, pool, `INSERT INTO seasons (content_id, series_id, season_number, title) VALUES ($1, $2, 2, 'Season 2')`, noPoster, series)
	jellyfin12Exec(t, pool, `INSERT INTO episodes (content_id, series_id, season_id, season_number, episode_number, title, overview, runtime, still_path, imdb_id, tmdb_id, tvdb_id) VALUES ($1, $2, $3, 1, 1, 'Pilot', '', 0, '', '', '', ''), ($4, $2, $5, 2, 1, 'Return', '', 0, '', '', '', '')`, ep1, series, withPoster, ep2, noPoster)

	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, browseRepo: catalog.NewBrowseRepository(pool), mapper: newMapper(codec, &config.Config{Auth: config.AuthConfig{JWTSecret: "secret"}})}
	targets, err := h.fetchCompatEpisodeTargetsByContentIDs(context.Background(), &Session{}, []string{ep1, ep2}, nil)
	if err != nil {
		t.Fatalf("fetch targets: %v", err)
	}
	first, second := targets[ep1], targets[ep2]
	if first.SeasonImages.ContentID != withPoster || first.SeasonImages.PosterPath != "season1/poster.jpg" || first.SeasonImages.PosterThumbhash != "th1" || first.SeasonImages.UpdatedAt.IsZero() {
		t.Fatalf("season poster not loaded: %+v", first.SeasonImages)
	}
	if second.SeasonImages.ContentID != "" {
		t.Fatalf("season without poster should carry no season images: %+v", second.SeasonImages)
	}
}
