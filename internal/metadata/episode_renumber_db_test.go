package metadata

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/models"
)

// When an order change makes two episodes trade numbers, each row takes the
// other's numbers and content ID, and a profile's watch progress stays on
// the episode it was recorded for.
func TestApplyEpisodeRenumberDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)

	nonce := time.Now().UnixNano() % 100_000_000
	tvdb := fmt.Sprintf("%d", 610_000_000+nonce)
	series := "series-tvdb-" + tvdb
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	mustID := func(id string, ok bool) string {
		t.Helper()
		if !ok {
			t.Fatal("content id did not compose")
		}
		return id
	}
	if err := items.Upsert(ctx, &models.MediaItem{
		ContentID: series, Type: "series", Title: "Renumbered show", Status: "matched", TvdbID: tvdb,
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := seasons.Upsert(ctx, &models.Season{
		ContentID: mustID(contentid.ForSeason(series, 1)), SeriesID: series, SeasonNumber: 1,
		DefaultMetadataLanguage: "en", MetadataSource: "provider",
	}); err != nil {
		t.Fatal(err)
	}
	pilot := mustID(contentid.ForEpisode(series, 1, 1))
	second := mustID(contentid.ForEpisode(series, 1, 2))
	for _, e := range []struct {
		id, tvdbID, title string
		number            int
	}{{pilot, "t1", "Pilot", 1}, {second, "t2", "Second", 2}} {
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: e.id, SeriesID: series, SeasonNumber: 1, EpisodeNumber: e.number, TvdbID: e.tvdbID + tvdb,
			Title: e.title, DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("renumber-user-%d", nonce)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nonce)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, 500, 3600, now())`, userID, profileID, pilot); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyEpisodeRenumber(ctx, tx, series, []episodeMove{
		{ContentID: pilot, FromSeason: 1, FromEpisode: 1, ToSeason: 1, ToEpisode: 2},
		{ContentID: second, FromSeason: 1, FromEpisode: 2, ToSeason: 1, ToEpisode: 1},
	}); err != nil {
		t.Fatalf("applyEpisodeRenumber = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	for _, want := range []struct {
		id, title string
		number    int
	}{{second, "Pilot", 2}, {pilot, "Second", 1}} {
		var title string
		var number int
		if err := pool.QueryRow(ctx, `SELECT title, episode_number FROM episodes WHERE content_id = $1`, want.id).
			Scan(&title, &number); err != nil {
			t.Fatalf("episode %s: %v", want.id, err)
		}
		if title != want.title || number != want.number {
			t.Fatalf("episode %s = %q E%d, want %q E%d", want.id, title, number, want.title, want.number)
		}
	}
	var position int
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`,
		userID, second).Scan(&position); err != nil || position != 500 {
		t.Fatalf("pilot's progress on its new id %s = %d (%v), want 500", second, position, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`,
		userID, pilot).Scan(&left); err != nil || left != 0 {
		t.Fatalf("progress rows left on %s = %d (%v), want 0", pilot, left, err)
	}
}

// Bleach after the blend: the old TMDB-numbered S1E21 row is the same
// episode as TVDB's S2E1 row. Merging moves the duplicate's watch progress
// and file onto the TVDB row and removes the duplicate.
func TestApplyEpisodeMergesDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)

	nonce := time.Now().UnixNano() % 100_000_000
	tvdb := fmt.Sprintf("%d", 620_000_000+nonce)
	series := "series-tvdb-" + tvdb
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	if err := items.Upsert(ctx, &models.MediaItem{
		ContentID: series, Type: "series", Title: "Bleach", Status: "matched", TvdbID: tvdb,
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		id, _ := contentid.ForSeason(series, n)
		if err := seasons.Upsert(ctx, &models.Season{
			ContentID: id, SeriesID: series, SeasonNumber: n, DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}
	keep, _ := contentid.ForEpisode(series, 2, 1)
	duplicate, _ := contentid.ForEpisode(series, 1, 21)
	for _, e := range []struct {
		id             string
		season, number int
		tvdbID, tmdbID string
	}{{keep, 2, 1, "t21" + tvdb, ""}, {duplicate, 1, 21, "", "m21" + tvdb}} {
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: e.id, SeriesID: series, SeasonNumber: e.season, EpisodeNumber: e.number,
			TvdbID: e.tvdbID, TmdbID: e.tmdbID, Title: "Enter!", DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}
	folder := insertTestFolder(t, pool, "series")
	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (media_folder_id, file_path, file_size, content_id, episode_id, season_number, episode_number)
		VALUES ($1, $2, 1, $3, $4, 1, 21) RETURNING id`,
		folder, fmt.Sprintf("/tv/Bleach-%d/Bleach - S01E21.mkv", nonce), series, duplicate).Scan(&fileID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id = $1`, fileID) })

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("merge-user-%d", nonce)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nonce)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, 700, 1440, now())`, userID, profileID, duplicate); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyEpisodeMerges(ctx, tx, series, []episodeMerge{{From: duplicate, Into: keep}}); err != nil {
		t.Fatalf("applyEpisodeMerges = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var gone int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episodes WHERE content_id = $1`, duplicate).Scan(&gone); err != nil || gone != 0 {
		t.Fatalf("duplicate rows left = %d (%v), want 0", gone, err)
	}
	var position int
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`,
		userID, keep).Scan(&position); err != nil || position != 700 {
		t.Fatalf("progress on %s = %d (%v), want 700", keep, position, err)
	}
	var episodeID string
	var season, number int
	if err := pool.QueryRow(ctx, `SELECT episode_id, season_number, episode_number FROM media_files WHERE id = $1`, fileID).
		Scan(&episodeID, &season, &number); err != nil {
		t.Fatal(err)
	}
	if episodeID != keep || season != 2 || number != 1 {
		t.Fatalf("file linked to %s S%dE%d, want %s S2E1", episodeID, season, number, keep)
	}
}

// A row that is not moving can hold the composed ID a moving row needs: its
// ID no longer matches its numbers after earlier history. The moving row then
// takes a fresh ID, and its watch progress still follows it.
func TestApplyEpisodeRenumberTakenIDDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)

	nonce := time.Now().UnixNano() % 100_000_000
	tvdb := fmt.Sprintf("%d", 630_000_000+nonce)
	series := "series-tvdb-" + tvdb
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	if err := items.Upsert(ctx, &models.MediaItem{
		ContentID: series, Type: "series", Title: "Re-anchored show", Status: "matched", TvdbID: tvdb,
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	mover, _ := contentid.ForEpisode(series, 1, 1)
	holder, _ := contentid.ForEpisode(series, 1, 2) // sits at S1E5
	for _, e := range []struct {
		id     string
		number int
	}{{mover, 1}, {holder, 5}} {
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: e.id, SeriesID: series, SeasonNumber: 1, EpisodeNumber: e.number,
			Title: fmt.Sprintf("E%d", e.number), DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("taken-user-%d", nonce)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nonce)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, 300, 1440, now())`, userID, profileID, mover); err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := applyEpisodeRenumber(ctx, tx, series, []episodeMove{
		{ContentID: mover, FromSeason: 1, FromEpisode: 1, ToSeason: 1, ToEpisode: 2},
	}); err != nil {
		t.Fatalf("applyEpisodeRenumber = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var moved string
	if err := pool.QueryRow(ctx, `SELECT content_id FROM episodes WHERE series_id = $1 AND season_number = 1 AND episode_number = 2`,
		series).Scan(&moved); err != nil {
		t.Fatalf("no row at S1E2: %v", err)
	}
	if moved == holder || moved == mover {
		t.Fatalf("moved row id = %s, want a fresh id", moved)
	}
	var position int
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`,
		userID, moved).Scan(&position); err != nil || position != 300 {
		t.Fatalf("progress on %s = %d (%v), want 300", moved, position, err)
	}
}
