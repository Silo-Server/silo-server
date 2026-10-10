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

// TestApplyListFileFieldsDB: list responses report the number of present
// versions when they request MediaSourceCount, as the detail path does,
// instead of assuming one, and the first version's video size and IsHD when
// they request Width, Height, or IsHD.
func TestApplyListFileFieldsDB(t *testing.T) {
	pool, suffix := jellyfin12CompatPool(t)
	var folderID int
	if err := pool.QueryRow(context.Background(), `INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, TRUE) RETURNING id`, fmt.Sprintf("jf12-msc-%d", suffix)).Scan(&folderID); err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	multi := fmt.Sprintf("jf12-multi-%d", suffix)
	single := fmt.Sprintf("jf12-single-%d", suffix)
	missing := fmt.Sprintf("jf12-missing-%d", suffix)
	sd := fmt.Sprintf("jf12-sd-%d", suffix)
	unprobed := fmt.Sprintf("jf12-unprobed-%d", suffix)
	all := []string{multi, single, missing, sd, unprobed}
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE content_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, all)
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	for _, id := range all {
		jellyfin12Exec(t, pool, `INSERT INTO media_items (content_id, type, title) VALUES ($1, 'movie', $1)`, id)
	}
	const (
		uhd       = `[{"codec":"hevc","width":3840,"height":2160}]`
		fullHD    = `[{"codec":"h264","width":1920,"height":1080}]`
		scope720  = `[{"codec":"h264","width":1280,"height":536}]`
		standardD = `[{"codec":"mpeg2video","width":720,"height":480}]`
	)
	// The 1080p file has the lower id, but detail lists the widest version
	// first, so the 4K file supplies the list's Width and Height.
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, video_tracks) VALUES ($1, $2, $1 || '-1080p.mkv', $3::jsonb)`, multi, folderID, fullHD)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, video_tracks) VALUES ($1, $2, $1 || '-4k.mkv', $3::jsonb)`, multi, folderID, uhd)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, missing_since) VALUES ($1, $2, $1 || '-gone.mkv', now())`, multi, folderID)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, video_tracks) VALUES ($1, $2, $1 || '.mkv', $3::jsonb)`, single, folderID, scope720)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, video_tracks, missing_since) VALUES ($1, $2, $1 || '.mkv', $3::jsonb, now())`, missing, folderID, uhd)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path, video_tracks) VALUES ($1, $2, $1 || '.mkv', $3::jsonb)`, sd, folderID, standardD)
	jellyfin12Exec(t, pool, `INSERT INTO media_files (content_id, media_folder_id, file_path) VALUES ($1, $2, $1 || '.mkv')`, unprobed, folderID)

	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, browseRepo: catalog.NewBrowseRepository(pool), mapper: newMapper(codec, &config.Config{})}
	page := func() []baseItemDTO {
		return []baseItemDTO{
			{ID: codec.EncodeStringID(EncodedIDItem, multi), Type: "Movie", MediaSourceCount: 1},
			{ID: codec.EncodeStringID(EncodedIDItem, single), Type: "Movie", MediaSourceCount: 1},
			{ID: codec.EncodeStringID(EncodedIDItem, "series-x"), Type: "Series"},
			// The list mapper assumes one source for a matched item; with every
			// file gone the count must be left unset.
			{ID: codec.EncodeStringID(EncodedIDItem, missing), Type: "Movie", MediaSourceCount: 1},
			{ID: codec.EncodeStringID(EncodedIDItem, sd), Type: "Movie", MediaSourceCount: 1},
			{ID: codec.EncodeStringID(EncodedIDItem, unprobed), Type: "Movie", MediaSourceCount: 1},
		}
	}

	items := page()
	h.applyListFileFields(context.Background(), &Session{}, items, itemsQuery{requestedFields: map[string]bool{"mediasourcecount": true}})
	if items[0].MediaSourceCount != 2 || items[1].MediaSourceCount != 1 || items[2].MediaSourceCount != 0 || items[3].MediaSourceCount != 0 {
		t.Fatalf("MediaSourceCount = %d/%d/%d/%d, want 2/1/0/0", items[0].MediaSourceCount, items[1].MediaSourceCount, items[2].MediaSourceCount, items[3].MediaSourceCount)
	}
	if items[0].Width != 0 || items[0].Height != 0 || items[0].IsHD {
		t.Fatalf("video fields set without being requested: %+v", items[0])
	}

	items = page()
	h.applyListFileFields(context.Background(), &Session{}, items, itemsQuery{})
	if items[0].MediaSourceCount != 1 {
		t.Fatal("MediaSourceCount must only be computed when it is requested")
	}

	items = page()
	h.applyListFileFields(context.Background(), &Session{}, items, itemsQuery{requestedFields: map[string]bool{"width": true, "height": true, "ishd": true}})
	type video struct {
		width, height int
		hd            bool
	}
	want := []video{
		{3840, 2160, true}, // widest version, as detail lists first
		{1280, 536, false}, // Jellyfin's IsHD is 720 lines and up
		{0, 0, false},      // a series has no file
		{0, 0, false},      // every file gone
		{720, 480, false},
		{0, 0, false}, // no probed video track
	}
	for i, w := range want {
		got := video{items[i].Width, items[i].Height, items[i].IsHD}
		if got != w {
			t.Errorf("item %d video = %+v, want %+v", i, got, w)
		}
	}
	if items[0].MediaSourceCount != 1 {
		t.Fatal("MediaSourceCount must keep the list default when only video fields are requested")
	}

	// A detail-hydrated item keeps the size the detail path gave it.
	items = page()
	items[0].Width, items[0].Height = 1920, 1080
	h.applyListFileFields(context.Background(), &Session{}, items, itemsQuery{requestedFields: map[string]bool{"width": true, "height": true}})
	if items[0].Width != 1920 || items[0].Height != 1080 {
		t.Fatalf("detail-hydrated size overwritten: %dx%d", items[0].Width, items[0].Height)
	}

	items = page()
	h.applyListFileFields(context.Background(), &Session{}, items, itemsQuery{requestedFields: map[string]bool{"height": true}})
	if items[0].Height != 2160 || items[0].Width != 0 || items[0].IsHD {
		t.Fatalf("only Height requested, got Width=%d Height=%d IsHD=%v", items[0].Width, items[0].Height, items[0].IsHD)
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
