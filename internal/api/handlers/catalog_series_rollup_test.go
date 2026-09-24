package handlers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seriesRollupTracer counts the statements a request sends and the rows they
// return, so the test can compare the SQL rollup with the per-episode fold.
type seriesRollupTracer struct {
	mu         sync.Mutex
	statements int
	rows       int64
}

func (t *seriesRollupTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return ctx
}

func (t *seriesRollupTracer) TraceQueryEnd(_ context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.statements++
	t.rows += data.CommandTag.RowsAffected()
}

func (t *seriesRollupTracer) measure(run func()) (int, int64) {
	t.mu.Lock()
	t.statements, t.rows = 0, 0
	t.mu.Unlock()
	run()
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.statements, t.rows
}

// rollupFallbackProvider serves the same Postgres store without its optional
// capabilities, so the handler folds per-episode progress over identical data.
type rollupFallbackProvider struct{ userstore.UserStoreProvider }

func (p rollupFallbackProvider) ForUser(ctx context.Context, userID int) (userstore.UserStore, error) {
	store, err := p.UserStoreProvider.ForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return struct{ userstore.UserStore }{store}, nil
}

// rollupOnlyStore answers the SQL rollup and nothing else. Its embedded nil
// UserStore panics on any other call, as does a pool-less episode repository,
// so a fallback to the per-episode fold fails the test.
type rollupOnlyStore struct {
	userstore.UserStore
	series, seasons map[string]userstore.SeriesWatchCounts
	seasonNumbers   map[int]userstore.SeriesWatchCounts
}

func (s rollupOnlyStore) SeriesEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return s.series, nil
}

func (s rollupOnlyStore) SeasonEpisodeWatchCounts(context.Context, string, []string) (map[string]userstore.SeriesWatchCounts, error) {
	return s.seasons, nil
}

func (s rollupOnlyStore) SeriesSeasonWatchCounts(context.Context, string, string) (map[int]userstore.SeriesWatchCounts, error) {
	return s.seasonNumbers, nil
}

// TestSeriesUserDataUsesEpisodeRollup pins series and season user_data to the
// store's SQL rollup when it has one: no episode listing, no per-episode
// progress reads. TestSeriesUserDataRollupParity proves the values match.
func TestSeriesUserDataUsesEpisodeRollup(t *testing.T) {
	store := rollupOnlyStore{
		series:  map[string]userstore.SeriesWatchCounts{"series-1": {TotalEpisodes: 5, WatchedCount: 2, InProgressCount: 1}},
		seasons: map[string]userstore.SeriesWatchCounts{"season-1": {TotalEpisodes: 3, WatchedCount: 3}},
		seasonNumbers: map[int]userstore.SeriesWatchCounts{
			0: {TotalEpisodes: 2, InProgressCount: 1},
			1: {TotalEpisodes: 3, WatchedCount: 3},
		},
	}
	h := &ItemsHandler{episodeRepo: catalog.NewEpisodeRepository(nil), storeProvider: testUserStoreProvider{store: store}}
	ctx := middleware.SetClaims(t.Context(), &auth.Claims{UserID: 1, Role: "user"})
	viewer := ItemViewer{ProfileID: "profile-1"}

	for _, tc := range []struct {
		itemType, id string
		want         *catalog.SeasonUserData
	}{
		{"series", "series-1", &catalog.SeasonUserData{WatchedCount: 2, UnplayedCount: 3, InProgressCount: 1}},
		{"season", "season-1", &catalog.SeasonUserData{WatchedCount: 3, Played: true}},
		// Like the fold, a parent without available episodes has no user_data.
		{"series", "series-2", nil},
		{"season", "season-2", nil},
	} {
		got, ok := h.parentRollupUserData(ctx, viewer, tc.itemType, tc.id)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s %s: user_data = %+v (ok %v), want %+v", tc.itemType, tc.id, got, ok, tc.want)
		}
	}

	episodeCounts, userData, err := h.seriesSeasonRollups(ctx, viewer, "series-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[int]int{0: 2, 1: 3}; !reflect.DeepEqual(episodeCounts, want) {
		t.Fatalf("episode counts = %v, want %v", episodeCounts, want)
	}
	wantUserData := map[int]*catalog.SeasonUserData{
		0: {UnplayedCount: 2, InProgressCount: 1},
		1: {WatchedCount: 3, Played: true},
	}
	if !reflect.DeepEqual(userData, wantUserData) {
		t.Fatalf("season user_data = %+v, want %+v", userData, wantUserData)
	}
}

// TestSeriesUserDataRollupParity checks that series and season user_data from
// the SQL rollup matches the per-episode fold value for value, and that the
// rollup stops the detail and seasons requests from loading every episode row.
func TestSeriesUserDataRollupParity(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	tracer := &seriesRollupTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}

	// 24 specials plus 12 seasons of 98 episodes: 1,200 available episodes.
	// Every episode has a file and the first five of each season a second
	// version. One extra episode has no library membership and must not count.
	const specials, seasonCount, perSeason = 24, 12, 98
	const available = specials + seasonCount*perSeason
	prefix := fmt.Sprintf("series-rollup-%d", time.Now().UnixNano())
	series := prefix + "-series"
	seasonID := func(n int) string { return fmt.Sprintf("%s-season-%d", prefix, n) }
	episodeID := func(season, episode int) string { return fmt.Sprintf("%s-s%02de%03d", prefix, season, episode) }
	var folder int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type,name,enabled) VALUES ('series',$1,true) RETURNING id`, prefix).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id=$1`, folder)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=$1`, series)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id=$1`, folder)
	})
	exec(`INSERT INTO media_items (content_id,type,title) VALUES ($1,'series','Rollup Series')`, series)
	exec(`INSERT INTO media_item_libraries (content_id,media_folder_id) VALUES ($1,$2)`, series, folder)
	for n := 0; n <= seasonCount; n++ {
		season := &models.Season{ContentID: seasonID(n), SeriesID: series, SeasonNumber: n, Title: fmt.Sprintf("Season %d", n), DefaultMetadataLanguage: "en"}
		if err := catalog.NewSeasonRepository(pool).Upsert(ctx, season); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title)
		SELECT format('%s-s%se%s', $1::text, lpad(s::text,2,'0'), lpad(e::text,3,'0')), $2, $1 || '-season-' || s, s, e, 'Episode ' || e
		FROM generate_series(0,$3::int) s
		CROSS JOIN LATERAL generate_series(1, CASE WHEN s = 0 THEN $4::int ELSE $5::int END) e`,
		prefix, series, seasonCount, specials, perSeason)
	exec(`INSERT INTO episode_libraries (episode_id,media_folder_id) SELECT content_id,$2 FROM episodes WHERE series_id=$1`, series, folder)
	exec(`INSERT INTO media_files (media_folder_id,file_path,episode_id)
		SELECT $2, '/rollup/' || content_id || '/v' || v || '.mkv', content_id
		FROM episodes CROSS JOIN LATERAL generate_series(1, CASE WHEN episode_number <= 5 THEN 2 ELSE 1 END) v
		WHERE series_id=$1`, series, folder)
	unavailable := episodeID(1, perSeason+1)
	exec(`INSERT INTO episodes (content_id,series_id,season_id,season_number,episode_number,title) VALUES ($1,$2,$3,1,$4,'Unavailable')`,
		unavailable, series, seasonID(1), perSeason+1)

	var userID int
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,role) VALUES ($1,'user') RETURNING id`, prefix).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	profileID := fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	otherProfileID := fmt.Sprintf("00000000-0000-4000-8001-%012d", time.Now().UnixNano()%1_000_000_000_000)
	for _, id := range []string{profileID, otherProfileID} {
		exec(`INSERT INTO user_profiles (id,user_id,name) VALUES ($1,$2,'Rollup fixture')`, id, userID)
	}
	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	watched := func(profile, id string) {
		t.Helper()
		if err := store.SetProgressAt(ctx, profile, id, 1500, 1500, true, at); err != nil {
			t.Fatal(err)
		}
	}
	resuming := func(profile, id string, when time.Time) {
		t.Helper()
		if err := store.SetProgressAt(ctx, profile, id, 300, 1500, false, when); err != nil {
			t.Fatal(err)
		}
	}
	history := func(id string) {
		t.Helper()
		if err := store.AddHistory(ctx, userstore.WatchHistoryEntry{ProfileID: profileID, MediaItemID: id, WatchedAt: at.Format(time.RFC3339), Completed: true, Source: userstore.WatchHistorySourceTrakt}); err != nil {
			t.Fatal(err)
		}
	}
	var hidden []string
	// Season 1 is fully watched and the unavailable extra is ignored.
	for e := 1; e <= perSeason; e++ {
		watched(profileID, episodeID(1, e))
	}
	watched(profileID, unavailable)
	// Season 2: 40 watched, 10 partially watched.
	for e := 1; e <= 50; e++ {
		if e <= 40 {
			watched(profileID, episodeID(2, e))
		} else {
			resuming(profileID, episodeID(2, e), at)
		}
	}
	// Season 3 covers completed history: history only (1-10), partial
	// progress plus history (11-15), progress or history hidden by a history
	// removal (16-25), and a resume after the removal (26-30).
	for e := 1; e <= 30; e++ {
		id := episodeID(3, e)
		switch {
		case e <= 10:
			history(id)
		case e <= 15:
			resuming(profileID, id, at)
			history(id)
		case e <= 20:
			watched(profileID, id)
			hidden = append(hidden, id)
		case e <= 25:
			history(id)
			hidden = append(hidden, id)
		default:
			watched(profileID, id)
			hidden = append(hidden, id)
		}
	}
	if err := store.RemoveHistoryItems(ctx, profileID, hidden, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for e := 26; e <= 30; e++ {
		resuming(profileID, episodeID(3, e), at.Add(2*time.Hour))
	}
	// Specials: one watched, one partially watched.
	watched(profileID, episodeID(0, 1))
	resuming(profileID, episodeID(0, 2), at)
	// Another profile on the account must not leak into the viewer's counts.
	for e := 1; e <= perSeason; e++ {
		watched(otherProfileID, episodeID(4, e))
	}

	want := map[int]catalog.SeasonUserData{
		0: {WatchedCount: 1, UnplayedCount: specials - 1, InProgressCount: 1},
		1: {WatchedCount: perSeason, Played: true},
		2: {WatchedCount: 40, UnplayedCount: perSeason - 40, InProgressCount: 10},
		3: {WatchedCount: 15, UnplayedCount: perSeason - 15, InProgressCount: 5},
	}
	wantSeries := catalog.SeasonUserData{WatchedCount: 1 + perSeason + 40 + 15, InProgressCount: 1 + 10 + 5}
	wantSeries.UnplayedCount = available - wantSeries.WatchedCount

	if rollup, ok := store.(userstore.SeriesEpisodeRollupStore); ok {
		// Call the capability directly as well: a SQL error must not let the
		// handler's fallback hide a broken rollup from this test.
		counts, err := rollup.SeriesEpisodeWatchCounts(ctx, profileID, []string{series})
		if err != nil {
			t.Fatal(err)
		}
		if got := *catalog.SeasonUserDataFromCounts(counts[series]); got != wantSeries {
			t.Fatalf("series rollup = %+v, want %+v", got, wantSeries)
		}
	}

	ctx = middleware.SetClaims(ctx, &auth.Claims{UserID: userID, Role: "user"})
	viewer := ItemViewer{ProfileID: profileID}
	newHandler := func(provider userstore.UserStoreProvider) *CatalogResourceHandler {
		items := catalog.NewItemRepository(pool)
		episodes := catalog.NewEpisodeRepository(pool)
		seasons := catalog.NewSeasonRepository(pool)
		files := scanner.NewFileRepository(pool)
		detail := catalog.NewDetailService(items, episodes, seasons, catalog.NewPersonRepository(pool), files)
		return NewCatalogResourceHandler(NewItemsHandler(catalog.NewBrowseRepository(pool), items, episodes, seasons, nil, files, provider, detail, catalog.NewProviderIDRepository(pool)))
	}
	type result struct {
		seriesDetail, seasonDetail *catalog.ItemDetail
		seasons                    []SeasonView
		seasonByNumber             SeasonView
		counts                     map[string][2]int64
	}
	run := func(h *CatalogResourceHandler) result {
		t.Helper()
		res := result{counts: map[string][2]int64{}}
		var err error
		statements, rows := tracer.measure(func() { res.seriesDetail, err = h.ItemDetail(ctx, viewer, series) })
		if err != nil {
			t.Fatalf("series detail: %v", err)
		}
		res.counts["series detail"] = [2]int64{int64(statements), rows}
		statements, rows = tracer.measure(func() { res.seasonDetail, err = h.ItemDetail(ctx, viewer, seasonID(3)) })
		if err != nil {
			t.Fatalf("season detail: %v", err)
		}
		res.counts["season detail"] = [2]int64{int64(statements), rows}
		statements, rows = tracer.measure(func() { res.seasons, err = h.SeriesSeasons(ctx, viewer, series, true) })
		if err != nil {
			t.Fatalf("series seasons: %v", err)
		}
		res.counts["series seasons"] = [2]int64{int64(statements), rows}
		statements, rows = tracer.measure(func() { res.seasonByNumber, err = h.SeriesSeason(ctx, viewer, series, 3) })
		if err != nil {
			t.Fatalf("series season: %v", err)
		}
		res.counts["series season"] = [2]int64{int64(statements), rows}
		return res
	}
	// The rollup runs first, so anything it warms can only make the fold
	// cheaper and the comparison below stricter.
	rollup := run(newHandler(provider))
	fold := run(newHandler(rollupFallbackProvider{provider}))
	for _, name := range []string{"series detail", "season detail", "series seasons", "series season"} {
		t.Logf("%s: fold %d statements / %d rows, rollup %d statements / %d rows",
			name, fold.counts[name][0], fold.counts[name][1], rollup.counts[name][0], rollup.counts[name][1])
	}

	if rollup.seriesDetail.SeasonUserData == nil || *rollup.seriesDetail.SeasonUserData != wantSeries {
		t.Fatalf("series user_data = %+v, want %+v", rollup.seriesDetail.SeasonUserData, wantSeries)
	}
	if rollup.seasonDetail.SeasonUserData == nil || *rollup.seasonDetail.SeasonUserData != want[3] {
		t.Fatalf("season user_data = %+v, want %+v", rollup.seasonDetail.SeasonUserData, want[3])
	}
	if got := rollup.seasonByNumber; got.UserData == nil || *got.UserData != want[3] || got.EpisodeCount != perSeason {
		t.Fatalf("season 3 by number: user_data = %+v, episode_count = %d; want %+v, %d", got.UserData, got.EpisodeCount, want[3], perSeason)
	}
	if len(rollup.seasons) != seasonCount+1 {
		t.Fatalf("got %d seasons, want %d", len(rollup.seasons), seasonCount+1)
	}
	for _, season := range rollup.seasons {
		wantSeason, ok := want[season.SeasonNumber]
		if !ok {
			wantSeason = catalog.SeasonUserData{UnplayedCount: perSeason}
		}
		if season.UserData == nil || *season.UserData != wantSeason {
			t.Fatalf("season %d user_data = %+v, want %+v", season.SeasonNumber, season.UserData, wantSeason)
		}
		wantEpisodes := perSeason
		if season.SeasonNumber == 0 {
			wantEpisodes = specials
		}
		if season.EpisodeCount != wantEpisodes {
			t.Fatalf("season %d episode_count = %d, want %d", season.SeasonNumber, season.EpisodeCount, wantEpisodes)
		}
	}
	if !reflect.DeepEqual(rollup.seriesDetail, fold.seriesDetail) {
		t.Fatalf("series detail differs from the per-episode fold:\nrollup %+v\nfold   %+v", rollup.seriesDetail, fold.seriesDetail)
	}
	if !reflect.DeepEqual(rollup.seasonDetail, fold.seasonDetail) {
		t.Fatalf("season detail differs from the per-episode fold:\nrollup %+v\nfold   %+v", rollup.seasonDetail, fold.seasonDetail)
	}
	if !reflect.DeepEqual(rollup.seasons, fold.seasons) {
		t.Fatalf("seasons differ from the per-episode fold:\nrollup %+v\nfold   %+v", rollup.seasons, fold.seasons)
	}
	if !reflect.DeepEqual(rollup.seasonByNumber, fold.seasonByNumber) {
		t.Fatalf("season by number differs from the per-episode fold:\nrollup %+v\nfold   %+v", rollup.seasonByNumber, fold.seasonByNumber)
	}

	// The fold reads every available episode row in scope; the rollup must
	// not, so it saves at least that many rows and some statements.
	for name, episodes := range map[string]int64{"series detail": available, "season detail": perSeason, "series seasons": available} {
		if rollup.counts[name][0] >= fold.counts[name][0] || fold.counts[name][1]-rollup.counts[name][1] < episodes {
			t.Errorf("%s: rollup %d statements / %d rows, fold %d statements / %d rows; want fewer statements and at least %d fewer rows",
				name, rollup.counts[name][0], rollup.counts[name][1], fold.counts[name][0], fold.counts[name][1], episodes)
		}
	}
	// A season requested by number still lists its episodes for the stale
	// metadata check, so the rollup replaces only the progress reads.
	if name := "series season"; rollup.counts[name][0] >= fold.counts[name][0] || rollup.counts[name][1] >= fold.counts[name][1] {
		t.Errorf("%s: rollup %d statements / %d rows, fold %d statements / %d rows; want fewer of both",
			name, rollup.counts[name][0], rollup.counts[name][1], fold.counts[name][0], fold.counts[name][1])
	}
}
