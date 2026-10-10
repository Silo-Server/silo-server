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

// An Identify that corrects a series matched to the wrong show moves it to the
// right show's id right away, taking the season and episode ids composed from
// the wrong anchor along, so the wrong show can be scanned in as its own item.
// A profile's leftover rows on the new ids merge instead of failing the move.
func TestIdentify_CorrectedSeriesMovesWithItsChildren(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	providerIDs := catalog.NewProviderIDRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)
	service := NewMetadataService(nil, nil, nil, items, providerIDs, episodes, seasons,
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		nil, nil, nil, nil, nil)

	nonce := time.Now().UnixNano() % 100_000_000
	wrongTVDB := fmt.Sprintf("%d", 600_000_000+nonce)
	rightTVDB := fmt.Sprintf("%d", 700_000_000+nonce)
	decoyTVDB := fmt.Sprintf("%d", 800_000_000+nonce)
	from, to, decoy := "series-tvdb-"+wrongTVDB, "series-tvdb-"+rightTVDB, "series-tvdb-"+decoyTVDB
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to, decoy})
	})
	mustID := func(id string, ok bool) string {
		t.Helper()
		if !ok {
			t.Fatal("content id did not compose")
		}
		return id
	}
	seedSeries := func(id, tvdb string) {
		t.Helper()
		if err := items.Upsert(ctx, &models.MediaItem{
			ContentID: id, Type: "series", Title: "Wrong show", Status: "matched", TvdbID: tvdb,
			DefaultMetadataLanguage: "en",
			Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
		}); err != nil {
			t.Fatal(err)
		}
		if err := providerIDs.ReplaceByContentID(ctx, id, map[string]string{"tvdb": tvdb}); err != nil {
			t.Fatal(err)
		}
	}
	seedEpisode := func(seriesID, id string, season, episode int) {
		t.Helper()
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: id, SeriesID: seriesID, SeasonNumber: season, EpisodeNumber: episode,
			Title: fmt.Sprintf("Episode %d", episode), DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seedSeries(from, wrongTVDB)
	oldSeason := mustID(contentid.ForSeason(from, 1))
	if err := seasons.Upsert(ctx, &models.Season{
		ContentID: oldSeason, SeriesID: from, SeasonNumber: 1, DefaultMetadataLanguage: "en", MetadataSource: "provider",
	}); err != nil {
		t.Fatal(err)
	}
	oldEpisode1 := mustID(contentid.ForEpisode(from, 1, 1))
	oldEpisode2 := mustID(contentid.ForEpisode(from, 1, 2))
	oldEpisode4 := mustID(contentid.ForEpisode(from, 1, 4))
	sonyflakeEpisode := fmt.Sprintf("1460%014d", nonce)
	seedEpisode(from, oldEpisode1, 1, 1)
	seedEpisode(from, oldEpisode2, 1, 2)
	seedEpisode(from, sonyflakeEpisode, 1, 3)
	seedEpisode(from, oldEpisode4, 1, 4)
	// Another row already holds the id episode 2 would move to, so episode 2
	// takes a fresh id and the rest of the rename goes ahead.
	takenEpisode2 := mustID(contentid.ForEpisode(to, 1, 2))
	seedSeries(decoy, decoyTVDB)
	seedEpisode(decoy, takenEpisode2, 7, 7)

	// The right show was in the library once, so a profile still has rows on
	// two of its episode ids. On episode 1 the moving progress is newer and
	// wins; on episode 4 the leftover is newer and stays. Both favorites
	// collapse into one.
	newEpisode1 := mustID(contentid.ForEpisode(to, 1, 1))
	newEpisode4 := mustID(contentid.ForEpisode(to, 1, 4))
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("reanchor-user-%d", nonce)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM user_favorites WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nonce)
	early := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	late := early.Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds, updated_at)
		VALUES ($1, $2, $3, 500, 3600, $7), ($1, $2, $4, 100, 3600, $6),
		       ($1, $2, $5, 100, 3600, $6), ($1, $2, $8, 900, 3600, $7)`,
		userID, profileID, oldEpisode1, newEpisode1, oldEpisode4, early, late, newEpisode4); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_favorites (user_id, profile_id, media_item_id) VALUES ($1, $2, $3), ($1, $2, $4)`,
		userID, profileID, oldEpisode1, newEpisode1); err != nil {
		t.Fatal(err)
	}

	remote := &remoteStubProvider{
		slug:     "tvdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right show", ProviderIDs: map[string]string{"tvdb": rightTVDB}},
	}
	result, err := service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: from, ProviderIDs: map[string]string{"tvdb": rightTVDB}, Language: "en", Mode: ModeIdentify,
	}, []Provider{remote})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.ContentID != to {
		t.Fatalf("identify result = %#v, want content id %s", result, to)
	}
	if _, err := items.GetByID(ctx, from); err == nil {
		t.Errorf("series %s still exists after the correction", from)
	}

	seriesOf := func(table, id string) string {
		t.Helper()
		var seriesID string
		err := pool.QueryRow(ctx, fmt.Sprintf(`SELECT series_id FROM %s WHERE content_id = $1`, table), id).Scan(&seriesID)
		if err != nil {
			return ""
		}
		return seriesID
	}
	for _, tc := range []struct{ table, id, wantSeries string }{
		{"seasons", mustID(contentid.ForSeason(to, 1)), to},
		{"seasons", oldSeason, ""},
		{"episodes", newEpisode1, to},
		{"episodes", oldEpisode1, ""},
		{"episodes", oldEpisode2, ""},
		{"episodes", takenEpisode2, decoy},
		{"episodes", sonyflakeEpisode, to},
		{"episodes", newEpisode4, to},
	} {
		if got := seriesOf(tc.table, tc.id); got != tc.wantSeries {
			t.Errorf("%s %s belongs to %q, want %q", tc.table, tc.id, got, tc.wantSeries)
		}
	}
	var episode2ID string
	if err := pool.QueryRow(ctx, `
		SELECT content_id FROM episodes WHERE series_id = $1 AND season_number = 1 AND episode_number = 2`, to).Scan(&episode2ID); err != nil {
		t.Fatalf("episode 2 under %s: %v", to, err)
	}
	if contentid.IsProviderAnchored(episode2ID) || contentid.IsLocal(episode2ID) {
		t.Errorf("episode 2 moved to %q, want a fresh Sonyflake id", episode2ID)
	}

	progress := map[string]float64{}
	rows, err := pool.Query(ctx, `SELECT media_item_id, position_seconds FROM user_watch_progress WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var position float64
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatal(err)
		}
		progress[id] = position
	}
	rows.Close()
	if want := map[string]float64{newEpisode1: 500, newEpisode4: 900}; fmt.Sprint(progress) != fmt.Sprint(want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}
	var favorites []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(media_item_id) FROM user_favorites WHERE user_id = $1`, userID).Scan(&favorites); err != nil {
		t.Fatal(err)
	}
	if len(favorites) != 1 || favorites[0] != newEpisode1 {
		t.Errorf("favorites = %v, want [%s]", favorites, newEpisode1)
	}
}

// An Identify that corrects a series onto a show already in the library merges
// into it. A wrong-anchor episode the target lacks takes its profile state to
// the id the target composes for it, so the state neither stays on an id the
// wrong show mints again nor is lost.
func TestIdentify_CorrectionOntoExistingSeriesMovesUnpairedEpisodeState(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	providerIDs := catalog.NewProviderIDRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)
	service := NewMetadataService(nil, nil, nil, items, providerIDs, episodes, seasons,
		catalog.NewLibraryItemRepository(pool), catalog.NewFolderRepository(pool),
		nil, nil, nil, nil, nil)

	nonce := time.Now().UnixNano() % 100_000_000
	wrongTVDB := fmt.Sprintf("%d", 610_000_000+nonce)
	rightTVDB := fmt.Sprintf("%d", 710_000_000+nonce)
	from, to := "series-tvdb-"+wrongTVDB, "series-tvdb-"+rightTVDB
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{from, to})
	})
	mustID := func(id string, ok bool) string {
		t.Helper()
		if !ok {
			t.Fatal("content id did not compose")
		}
		return id
	}
	for _, series := range []struct{ id, tvdb string }{{from, wrongTVDB}, {to, rightTVDB}} {
		if err := items.Upsert(ctx, &models.MediaItem{
			ContentID: series.id, Type: "series", Title: "Show", Status: "matched", TvdbID: series.tvdb,
			DefaultMetadataLanguage: "en",
			Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
		}); err != nil {
			t.Fatal(err)
		}
		if err := providerIDs.ReplaceByContentID(ctx, series.id, map[string]string{"tvdb": series.tvdb}); err != nil {
			t.Fatal(err)
		}
	}
	seedEpisode := func(seriesID, id string, season, episode int) {
		t.Helper()
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: id, SeriesID: seriesID, SeasonNumber: season, EpisodeNumber: episode,
			Title: fmt.Sprintf("Episode %d", episode), DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}); err != nil {
			t.Fatal(err)
		}
	}
	oldPaired := mustID(contentid.ForEpisode(from, 1, 1))
	oldUnpaired := mustID(contentid.ForEpisode(from, 2, 3))
	newPaired := mustID(contentid.ForEpisode(to, 1, 1))
	newUnpaired := mustID(contentid.ForEpisode(to, 2, 3))
	seedEpisode(from, oldPaired, 1, 1)
	seedEpisode(from, oldUnpaired, 2, 3)
	seedEpisode(to, newPaired, 1, 1)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("reanchor-merge-user-%d", nonce)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_watch_progress WHERE user_id = $1`, userID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, userID)
	})
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", nonce)
	if _, err := pool.Exec(ctx, `
		INSERT INTO user_watch_progress (user_id, profile_id, media_item_id, position_seconds, duration_seconds)
		VALUES ($1, $2, $3, 100, 3600), ($1, $2, $4, 200, 3600)`,
		userID, profileID, oldPaired, oldUnpaired); err != nil {
		t.Fatal(err)
	}

	remote := &remoteStubProvider{
		slug:     "tvdb",
		metadata: &MetadataResult{HasMetadata: true, Title: "Right show", ProviderIDs: map[string]string{"tvdb": rightTVDB}},
	}
	result, err := service.ProcessWithProviders(ctx, ProcessRequest{
		ContentID: from, ProviderIDs: map[string]string{"tvdb": rightTVDB}, Language: "en", Mode: ModeIdentify,
	}, []Provider{remote})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.ContentID != to {
		t.Fatalf("identify result = %#v, want content id %s", result, to)
	}

	progress := map[string]float64{}
	rows, err := pool.Query(ctx, `SELECT media_item_id, position_seconds FROM user_watch_progress WHERE user_id = $1`, userID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var position float64
		if err := rows.Scan(&id, &position); err != nil {
			t.Fatal(err)
		}
		progress[id] = position
	}
	rows.Close()
	if want := map[string]float64{newPaired: 100, newUnpaired: 200}; fmt.Sprint(progress) != fmt.Sprint(want) {
		t.Errorf("progress = %v, want %v", progress, want)
	}
}

// Availability rows are insert-only history, so a target id can still have
// rows after its item was deleted. The bulk rename merges them into the moving
// rows, keeping the earliest timestamps, instead of failing on the unique keys:
// the logical key under the target series, and the primary key for a leftover
// row that holds a moving episode's new id under another series.
func TestRenameContentIDsMergesLeftoverAvailability(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	nonce := time.Now().UnixNano() % 100_000_000
	libraryID := int(900_000_000 + nonce%100_000_000)
	fromSeries, toSeries := fmt.Sprintf("series-tvdb-%d", 600_000_000+nonce), fmt.Sprintf("series-tvdb-%d", 700_000_000+nonce)
	fromMovie, toMovie := fmt.Sprintf("local-%028d", nonce), fmt.Sprintf("movie-tmdb-%d", 700_000_000+nonce)
	fromEpisode, _ := contentid.ForEpisode(fromSeries, 1, 2)
	toEpisode, _ := contentid.ForEpisode(toSeries, 1, 2)
	strayEpisode, _ := contentid.ForEpisode(fromSeries, 1, 3)
	strayTarget, _ := contentid.ForEpisode(toSeries, 1, 3)
	straySeries := fmt.Sprintf("series-tvdb-%d", 800_000_000+nonce)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM episode_availability WHERE library_id = $1`, libraryID)
		_, _ = pool.Exec(bg, `DELETE FROM movie_availability WHERE library_id = $1`, libraryID)
	})
	early := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	late := early.Add(24 * time.Hour)
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_availability (library_id, episode_id, series_id, season_number, episode_number, episode_key, available_at, created_at)
		VALUES ($1, $2, $3, 1, 2, 1000002, $5, $5), ($1, $4, $6, 1, 2, 1000002, $7, $7)`,
		libraryID, fromEpisode, fromSeries, toEpisode, late, toSeries, early); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO episode_availability (library_id, episode_id, series_id, season_number, episode_number, episode_key, available_at, created_at)
		VALUES ($1, $2, $3, 1, 3, 1000003, $5, $5), ($1, $4, $6, 9, 9, 9000009, $7, $7)`,
		libraryID, strayEpisode, fromSeries, strayTarget, late, straySeries, early); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO movie_availability (library_id, item_id, available_at, created_at)
		VALUES ($1, $2, $4, $4), ($1, $3, $5, $5)`,
		libraryID, fromMovie, toMovie, late, early); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `SELECT silo_rename_content_ids($1, $2)`,
		[]string{fromSeries, fromEpisode, strayEpisode, fromMovie},
		[]string{toSeries, toEpisode, strayTarget, toMovie}); err != nil {
		t.Fatalf("rename with leftover availability: %v", err)
	}

	var episodes, strays, movies int
	var episodeAt, strayAt, movieAt time.Time
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(available_at) FROM episode_availability
		WHERE library_id = $1 AND episode_id = $2 AND series_id = $3`, libraryID, toEpisode, toSeries).Scan(&episodes, &episodeAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(available_at) FROM episode_availability
		WHERE library_id = $1 AND episode_id = $2`, libraryID, strayTarget).Scan(&strays, &strayAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*), min(available_at) FROM movie_availability
		WHERE library_id = $1 AND item_id = $2`, libraryID, toMovie).Scan(&movies, &movieAt); err != nil {
		t.Fatal(err)
	}
	if episodes != 1 || !episodeAt.Equal(early) {
		t.Errorf("episode availability = %d rows at %v, want 1 at %v", episodes, episodeAt, early)
	}
	if strays != 1 || !strayAt.Equal(early) {
		t.Errorf("availability for %s = %d rows at %v, want 1 at %v", strayTarget, strays, strayAt, early)
	}
	if movies != 1 || !movieAt.Equal(early) {
		t.Errorf("movie availability = %d rows at %v, want 1 at %v", movies, movieAt, early)
	}
}
