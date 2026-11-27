package metadata

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)

	series, tvdb, nonce := insertRenumberTestSeries(t, pool, 610_000_000, "Renumbered show")
	mustID := func(id string, ok bool) string {
		t.Helper()
		if !ok {
			t.Fatal("content id did not compose")
		}
		return id
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

	userID := insertRenumberTestProgress(t, pool, "renumber", nonce, pilot, 500, 3600)

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
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)

	series, tvdb, nonce := insertRenumberTestSeries(t, pool, 620_000_000, "Bleach")
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

	userID := insertRenumberTestProgress(t, pool, "merge", nonce, duplicate, 700, 1440)

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
	// The file keeps the numbers its name gives; only its link moves.
	if episodeID != keep || season != 1 || number != 21 {
		t.Fatalf("file linked to %s S%dE%d, want %s with its own S1E21", episodeID, season, number, keep)
	}
}

// A row that is not moving can hold the composed ID a moving row needs: its
// ID no longer matches its numbers after earlier history. The moving row then
// takes a fresh ID, and its watch progress still follows it.
func TestApplyEpisodeRenumberTakenIDDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	episodes := catalog.NewEpisodeRepository(pool)

	series, _, nonce := insertRenumberTestSeries(t, pool, 630_000_000, "Re-anchored show")
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
	userID := insertRenumberTestProgress(t, pool, "taken", nonce, mover, 300, 1440)

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

// After an order change and back, S02E01.mkv rode its row to S17E1 and a
// placeholder row held S03E22.mkv. Relinking puts each file on the episode
// its name gives, moves library membership with it, and retires the emptied
// placeholder into the real episode with its watch progress.
func TestRelinkSeriesFilesDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	episodes := catalog.NewEpisodeRepository(pool)
	svc := &MetadataService{folderRepo: catalog.NewFolderRepository(pool)}

	series, _, nonce := insertRenumberTestSeries(t, pool, 640_000_000, "Bleach")
	s2e1, _ := contentid.ForEpisode(series, 2, 1)
	s17e1, _ := contentid.ForEpisode(series, 17, 1)
	s3e22, _ := contentid.ForEpisode(series, 3, 22)
	placeholder, _ := contentid.ForEpisode(series, 3, 42)
	for _, e := range []struct {
		id, source     string
		season, number int
	}{{s2e1, "provider", 2, 1}, {s17e1, "provider", 17, 1}, {s3e22, "provider", 3, 22}, {placeholder, "scanner_fallback", 3, 42}} {
		if err := episodes.Upsert(ctx, &models.Episode{
			ContentID: e.id, SeriesID: series, SeasonNumber: e.season, EpisodeNumber: e.number,
			Title: "E", DefaultMetadataLanguage: "en", MetadataSource: e.source,
		}); err != nil {
			t.Fatal(err)
		}
	}
	folder := insertTestFolder(t, pool, "series")
	fileOn := func(name, episodeID string, season, number int) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, content_id, episode_id, season_number, episode_number)
			VALUES ($1, $2, 1, $3, $4, $5, $6) RETURNING id`,
			folder, fmt.Sprintf("/tv/Bleach-%d/%s", nonce, name), series, episodeID, season, number).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO episode_libraries (episode_id, media_folder_id) VALUES ($1, $2)
			ON CONFLICT DO NOTHING`, episodeID, folder); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE id = $1`, id) })
		return id
	}
	rode := fileOn("Season 02/Bleach - S02E01.mkv", s17e1, 17, 1)
	parked := fileOn("Season 03/Bleach - S03E22.mkv", placeholder, 3, 42)

	userID := insertRenumberTestProgress(t, pool, "relink", nonce, placeholder, 300, 1440)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	relinked, retired, err := svc.relinkSeriesFiles(ctx, tx, series)
	if err != nil {
		t.Fatalf("relinkSeriesFiles = %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if relinked != 2 || retired != 1 {
		t.Fatalf("relinked %d, retired %d; want 2, 1", relinked, retired)
	}
	for _, want := range []struct {
		file    int
		episode string
	}{{rode, s2e1}, {parked, s3e22}} {
		var got string
		if err := pool.QueryRow(ctx, `SELECT episode_id FROM media_files WHERE id = $1`, want.file).Scan(&got); err != nil || got != want.episode {
			t.Fatalf("file %d on %q (%v), want %s", want.file, got, err, want.episode)
		}
	}
	var members []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(episode_id ORDER BY episode_id) FROM episode_libraries WHERE media_folder_id = $1`,
		folder).Scan(&members); err != nil {
		t.Fatal(err)
	}
	if want := []string{s2e1, s3e22}; fmt.Sprint(members) != fmt.Sprint(want) {
		t.Fatalf("library members = %v, want %v", members, want)
	}
	var position int
	if err := pool.QueryRow(ctx, `SELECT position_seconds FROM user_watch_progress WHERE user_id = $1 AND media_item_id = $2`,
		userID, s3e22).Scan(&position); err != nil || position != 300 {
		t.Fatalf("placeholder progress on %s = %d (%v), want 300", s3e22, position, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM episodes WHERE content_id = $1`, placeholder).Scan(&left); err != nil || left != 0 {
		t.Fatalf("placeholder rows left = %d (%v), want 0", left, err)
	}
}

// An arrangement saves as the series' custom layout and reads back as stored:
// seasons with the admin's titles, episodes at the positions given. A save
// based on an older revision is refused, so a second admin's arrangement is
// not overwritten unseen; regular seasons must run 1…N.
func TestSaveCustomSeasonsDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	items := catalog.NewItemRepository(pool)
	svc := &MetadataService{itemRepo: items, dbPool: pool}

	series, _, _ := insertRenumberTestSeries(t, pool, 650_000_000, "Arranged show")
	save := CustomSeasonsSave{
		Settings: EpisodeOrderSettings{Provider: "tvdb"},
		Seasons: []CustomSeasonsSeason{
			{Number: 1, Title: "Arc one", Episodes: []CustomSeasonsEpisode{{Key: "tvdb:c"}, {Key: "tvdb:a"}}},
			{Number: 2, Overview: "Mine", Episodes: []CustomSeasonsEpisode{{Key: "tvdb:b"}}},
			{Number: 0, Episodes: []CustomSeasonsEpisode{{Key: "tvdb:x"}}},
		},
	}
	zero := int64(0)
	revision, err := svc.SaveCustomSeasons(ctx, series, save, &zero, 0)
	if err != nil {
		t.Fatalf("SaveCustomSeasons = %v", err)
	}
	layout, ok, err := loadCustomLayout(ctx, pool, series)
	if err != nil || !ok {
		t.Fatalf("loadCustomLayout = %v, %v", ok, err)
	}
	wantSlots := map[string]episodeSlot{"tvdb:c": {1, 1}, "tvdb:a": {1, 2}, "tvdb:b": {2, 1}, "tvdb:x": {0, 1}}
	if fmt.Sprint(layout.Slots) != fmt.Sprint(wantSlots) {
		t.Fatalf("slots = %v, want %v", layout.Slots, wantSlots)
	}
	if want := []customSeason{{Number: 1, Title: "Arc one"}, {Number: 2, Overview: "Mine"}}; fmt.Sprint(layout.Seasons) != fmt.Sprint(want) {
		t.Fatalf("seasons = %+v, want %+v", layout.Seasons, want)
	}
	order, err := svc.seriesEpisodeOrder(ctx, series)
	if err != nil || order.Layout != episodeLayoutCustom || order.Custom == nil || order.Provider != "tvdb" {
		t.Fatalf("seriesEpisodeOrder = %+v, %v; want the custom layout", order, err)
	}

	if _, err := svc.SaveCustomSeasons(ctx, series, save, &zero, 0); !errors.Is(err, ErrEpisodeOrderConflict) {
		t.Fatalf("save on a stale revision = %v, want ErrEpisodeOrderConflict", err)
	}
	gap := save
	gap.Seasons = []CustomSeasonsSeason{{Number: 1}, {Number: 3}}
	if _, err := svc.SaveCustomSeasons(ctx, series, gap, &revision, 0); !errors.Is(err, ErrEpisodeOrderInvalid) {
		t.Fatalf("save with a season gap = %v, want ErrEpisodeOrderInvalid", err)
	}

	// A season poster is named by an upload recorded for this series; a save
	// cannot point a season at any other image.
	foreign := save
	foreign.Seasons = []CustomSeasonsSeason{{Number: 1, Poster: CustomSeasonPoster{Source: "upload://sha256:not-ours"},
		Episodes: []CustomSeasonsEpisode{{Key: "tvdb:a"}}}}
	if _, err := svc.SaveCustomSeasons(ctx, series, foreign, &revision, 0); !errors.Is(err, ErrEpisodeOrderInvalid) {
		t.Fatalf("save with a poster not uploaded for the series = %v, want ErrEpisodeOrderInvalid", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO series_custom_season_uploads (series_id, source, path, thumbhash)
		VALUES ($1, 'upload://sha256:ours', 'series/x/poster/original.ab.webp', 'th')`, series); err != nil {
		t.Fatal(err)
	}
	ours := foreign
	ours.Seasons = []CustomSeasonsSeason{{Number: 1, Poster: CustomSeasonPoster{Source: "upload://sha256:ours"},
		Episodes: []CustomSeasonsEpisode{{Key: "tvdb:a"}}}}
	if _, err := svc.SaveCustomSeasons(ctx, series, ours, &revision, 0); err != nil {
		t.Fatalf("save with the series' own upload = %v", err)
	}
	layout, _, err = loadCustomLayout(ctx, pool, series)
	if err != nil || layout.Seasons[0].Poster.Path != "series/x/poster/original.ab.webp" {
		t.Fatalf("stored poster = %+v (%v), want the upload's stored path", layout.Seasons, err)
	}

	// Switching back to the arrangement keeps the provider its episode keys
	// belong to: another provider's keys would match none of its slots.
	other := EpisodeOrderSettings{Provider: "tmdb", Layout: episodeLayoutCustom}
	if _, err := svc.SaveEpisodeOrder(ctx, series, other, nil, 0); !errors.Is(err, ErrEpisodeOrderInvalid) {
		t.Fatalf("custom layout on another provider = %v, want ErrEpisodeOrderInvalid", err)
	}
	if _, err := svc.SaveEpisodeOrder(ctx, series, EpisodeOrderSettings{Layout: episodeLayoutCustom}, nil, 0); err != nil {
		t.Fatalf("custom layout on the library's provider = %v", err)
	}
	if order, err := svc.seriesEpisodeOrder(ctx, series); err != nil || order.Provider != "tvdb" {
		t.Fatalf("seriesEpisodeOrder = %+v, %v; want the arrangement's provider", order, err)
	}
}

// A bare number counts through the episodes the structure provider listed in
// its last full refresh: a row a renumber parked because the chosen order
// lacks it, from the same provider or another, must not shift every later
// file by one, while a row no refresh has numbered yet still counts.
func TestEpisodesOfCurrentNumberingDB(t *testing.T) {
	pool := chainBuiltinTestPool(t)
	ctx := t.Context()
	episodes := catalog.NewEpisodeRepository(pool)
	svc := &MetadataService{dbPool: pool}

	series, tvdb, _ := insertRenumberTestSeries(t, pool, 660_000_000, "Absolute show")
	var all []*models.Episode
	add := func(number int) {
		t.Helper()
		id, ok := contentid.ForEpisode(series, 1, number)
		if !ok {
			t.Fatal("content id did not compose")
		}
		episode := &models.Episode{
			ContentID: id, SeriesID: series, SeasonNumber: 1, EpisodeNumber: number, TvdbID: fmt.Sprint(tvdb, number),
			Title: fmt.Sprint("Episode ", number), DefaultMetadataLanguage: "en", MetadataSource: "provider",
		}
		if err := episodes.Upsert(ctx, episode); err != nil {
			t.Fatal(err)
		}
		all = append(all, episode)
	}
	write := func(slug string, numbers ...int) {
		t.Helper()
		targets := make([]EpisodeResult, 0, len(numbers))
		for _, n := range numbers {
			targets = append(targets, EpisodeResult{SeasonNumber: 1, EpisodeNumber: n, FileNumbered: true,
				FileSeasonNumber: 1, FileEpisodeNumber: n})
		}
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			return writeEpisodeFileNumbers(ctx, tx, series, slug, targets)
		}); err != nil {
			t.Fatal(err)
		}
	}
	current := func() string {
		t.Helper()
		kept, err := svc.episodesOfCurrentNumbering(ctx, series, all)
		if err != nil {
			t.Fatal(err)
		}
		var numbers []int
		for _, e := range kept {
			numbers = append(numbers, e.EpisodeNumber)
		}
		sort.Ints(numbers)
		return fmt.Sprint(numbers)
	}

	for number := 1; number <= 3; number++ {
		add(number)
	}
	write("tmdb", 1, 2, 3)
	write("tvdb", 1, 3) // another provider lacks episode 2
	if got := current(); got != "[1 3]" {
		t.Fatalf("after TVDB listed two = %v, want [1 3]", got)
	}
	write("tvdb", 1, 2, 3)
	write("tvdb", 2, 3) // another order of the same provider lacks episode 1
	if got := current(); got != "[2 3]" {
		t.Fatalf("after a TVDB order listed two = %v, want [2 3]", got)
	}
	add(4) // stored by a refresh that wrote no file numbers
	if got := current(); got != "[2 3 4]" {
		t.Fatalf("with an episode no refresh numbered = %v, want [2 3 4]", got)
	}
}

// insertRenumberTestSeries stores a matched series titled title under a TVDB
// ID made unique from base, and removes it when the test ends. It returns the
// series' content ID, its TVDB ID and the nonce in that ID.
func insertRenumberTestSeries(t *testing.T, pool *pgxpool.Pool, base int64, title string) (series, tvdb string, nonce int64) {
	t.Helper()
	nonce = time.Now().UnixNano() % 100_000_000
	tvdb = strconv.FormatInt(base+nonce, 10)
	series = "series-tvdb-" + tvdb
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id = $1`, series)
	})
	if err := catalog.NewItemRepository(pool).Upsert(t.Context(), &models.MediaItem{
		ContentID: series, Type: "series", Title: title, Status: "matched", TvdbID: tvdb,
		DefaultMetadataLanguage: "en",
		Studios:                 []string{}, Networks: []string{}, Countries: []string{}, Genres: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	return series, tvdb, nonce
}

// insertRenumberTestProgress makes a user named after prefix and nonce with
// watch progress on mediaItemID, removes both when the test ends, and
// returns the user's ID.
func insertRenumberTestProgress(t *testing.T, pool *pgxpool.Pool, prefix string, nonce int64, mediaItemID string, position, duration int) int {
	t.Helper()
	ctx := t.Context()
	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username, role) VALUES ($1, 'user') RETURNING id`,
		fmt.Sprintf("%s-user-%d", prefix, nonce)).Scan(&userID); err != nil {
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
		VALUES ($1, $2, $3, $4, $5, now())`, userID, profileID, mediaItemID, position, duration); err != nil {
		t.Fatal(err)
	}
	return userID
}
