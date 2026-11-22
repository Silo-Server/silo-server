package metadata_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/adminjob"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/scanner"
	"github.com/Silo-Server/silo-server/internal/scanqueue"
)

// stubTVDB knows one show with one season of two episodes.
type stubTVDB struct {
	tvdb, title string
	year        int
}

func (p *stubTVDB) Slug() string       { return "tvdb" }
func (p *stubTVDB) Name() string       { return "TheTVDB" }
func (p *stubTVDB) ForTypes() []string { return []string{"series"} }

func (p *stubTVDB) Search(_ context.Context, _ metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return []metadata.SearchResult{{Name: p.title, Year: p.year, ProviderIDs: map[string]string{"tvdb": p.tvdb}, Provider: "tvdb"}}, nil
}

func (p *stubTVDB) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if req.ProviderIDs["tvdb"] != p.tvdb {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{HasMetadata: true, Title: p.title, Year: p.year,
		ReleaseDate: fmt.Sprintf("%d-01-01", p.year), ProviderIDs: map[string]string{"tvdb": p.tvdb}}, nil
}

func (p *stubTVDB) episodes() []metadata.EpisodeResult {
	var out []metadata.EpisodeResult
	for n := 1; n <= 2; n++ {
		out = append(out, metadata.EpisodeResult{SeasonNumber: 1, EpisodeNumber: n, Title: fmt.Sprintf("Episode %d", n),
			ProviderIDs: map[string]string{"tvdb": fmt.Sprintf("%s%02d", p.tvdb, n)}})
	}
	return out
}

func (p *stubTVDB) GetSeasons(_ context.Context, req metadata.SeasonsRequest) ([]metadata.SeasonResult, error) {
	if req.ProviderIDs["tvdb"] != p.tvdb {
		return nil, nil
	}
	return []metadata.SeasonResult{{SeasonNumber: 1, Title: "Season 1", Episodes: p.episodes()}}, nil
}

func (p *stubTVDB) GetEpisodes(_ context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	if req.ProviderIDs["tvdb"] != p.tvdb || req.SeasonNumber != 1 {
		return nil, nil
	}
	return p.episodes(), nil
}

// recordedRefresh is the metadata refresh step of an item refresh. That step
// runs only after the scope is rebuilt and resolved, which is what most of
// these tests cover; with live set it also runs on the metadata service.
type recordedRefresh struct {
	targetType, contentID string
	live                  *metadata.MetadataService
}

func (r *recordedRefresh) RefreshItem(context.Context, string) error { return nil }
func (r *recordedRefresh) RefreshItemForLibrary(context.Context, string, int) error {
	return nil
}
func (r *recordedRefresh) RefreshTargetForLibrary(ctx context.Context, targetType, contentID string, folderID int) error {
	r.targetType, r.contentID = targetType, contentID
	if r.live != nil {
		return r.live.RefreshTargetForLibrary(ctx, targetType, contentID, folderID)
	}
	return nil
}
func (r *recordedRefresh) ResetTargetForLibrary(context.Context, string, string, int) error {
	return nil
}
func (r *recordedRefresh) RebuildItemForLibrary(ctx context.Context, contentID string, folderID int) error {
	r.targetType, r.contentID = "item", contentID
	if r.live != nil {
		return r.live.RebuildItemForLibrary(ctx, contentID, folderID)
	}
	return nil
}

// seriesRefreshHarness runs the production scan, series root queue, match
// worker and item refresh against Postgres.
type seriesRefreshHarness struct {
	pool      *pgxpool.Pool
	service   *metadata.MetadataService
	items     *catalog.ItemRepository
	folders   *catalog.FolderRepository
	scan      *scanner.Scanner
	worker    *metadata.MatchWorker
	resolver  *adminjob.ItemRefreshResolver
	refresh   *adminjob.ItemRefreshExecutor
	refreshed *recordedRefresh
}

func newSeriesRefreshHarness(t *testing.T, dsn string, providers ...metadata.Provider) *seriesRefreshHarness {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	files := scanner.NewFileRepository(pool)
	folders := catalog.NewFolderRepository(pool)
	items := catalog.NewItemRepository(pool)
	episodes := catalog.NewEpisodeRepository(pool)
	seasons := catalog.NewSeasonRepository(pool)
	rootClaims := catalog.NewRootClaimRepository(pool)
	skipped := metadata.NewSkippedRootRepository(pool)
	service := metadata.NewMetadataService(nil, nil, nil,
		items, catalog.NewProviderIDRepository(pool), episodes, seasons,
		catalog.NewLibraryItemRepository(pool), folders,
		nil, files, skipped, nil, rootClaims)
	service.SetLibraryRootResolver(folders)
	metadata.MatchWithProvidersForTest(service, "en", providers...)

	movieQueue := metadata.NewMovieMatchQueueRepository(pool, files)
	seriesQueue := metadata.NewSeriesRootMatchQueueRepository(pool)
	worker := metadata.NewMatchWorker(service, files, 2, 10, time.Hour)
	worker.SetMovieFileClaimer(movieQueue)
	worker.SetSeriesRootClaimer(seriesQueue, true)

	scan := scanner.NewScanner(files, "", nil, 1, false, 0)
	scan.SetMetadataQueueProducer(metadata.NewMatchQueueCoordinator(movieQueue, seriesQueue))
	scan.SetMovieQueueSyncer(movieQueue)
	scan.SetSeriesQueueSyncer(seriesQueue)
	ingest := libraryingest.NewExecutor(scan, worker, folders, skipped, nil, nil)

	refreshed := &recordedRefresh{}
	refresh := adminjob.NewItemRefreshExecutor(folders, files, rootClaims, catalog.NewGroupClaimRepository(pool), skipped,
		seasons, episodes, ingest, scanqueue.NewRepository(pool), refreshed, nil, nil)
	refresh.SetItemStatusReader(items)
	return &seriesRefreshHarness{
		pool: pool, service: service, items: items, folders: folders, scan: scan, worker: worker,
		resolver:  adminjob.NewItemRefreshResolver(items, seasons, episodes, folders, files),
		refresh:   refresh,
		refreshed: refreshed,
	}
}

// links lists each file of the library with its content and episode links.
func (h *seriesRefreshHarness) links(t *testing.T, folderID int) map[string][2]string {
	t.Helper()
	rows, err := h.pool.Query(t.Context(), `
		SELECT file_path, COALESCE(content_id, ''), COALESCE(episode_id, '')
		FROM media_files WHERE media_folder_id = $1 ORDER BY file_path`, folderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][2]string{}
	for rows.Next() {
		var path, content, episode string
		if err := rows.Scan(&path, &content, &episode); err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(path)] = [2]string{content, episode}
	}
	return out
}

// TestCompleteRefreshRebuildsSeriesDB covers #934 through the production scan,
// series root queue, matcher and item refresh: a Complete Refresh of a matched
// series, one of its seasons, or one episode unlinks the scope's files, and
// must link them again before it resolves the rebuilt item. The files are
// unchanged on disk, so without waiting out the TV queue's quiet window the
// refresh ended with "no rebuilt content found in refresh scope" and left them
// unlinked.
func TestCompleteRefreshRebuildsSeriesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	nonce := time.Now().UnixNano() % 100_000_000
	for i, target := range []struct{ name, idPattern string }{
		{"series", "series-tvdb-%s"},
		{"season", "season-tvdb-%s-1"},
		{"episode", "episode-tvdb-%s-1-2"},
	} {
		t.Run(target.name, func(t *testing.T) {
			// The fixtures are independent, so the real queue waits can overlap.
			t.Parallel()
			testCompleteRefreshSeriesTarget(t, dsn, nonce+int64(i), target.idPattern)
		})
	}
}

func testCompleteRefreshSeriesTarget(t *testing.T, dsn string, nonce int64, idPattern string) {
	t.Helper()
	show := &stubTVDB{tvdb: strconv.FormatInt(700_000_000+nonce, 10), title: "Refresh Show", year: 2020}
	h := newSeriesRefreshHarness(t, dsn, show)
	ctx := t.Context()

	root := filepath.Join(t.TempDir(), "tv")
	seasonDir := filepath.Join(root, "Refresh Show (2020)", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		path := filepath.Join(seasonDir, fmt.Sprintf("Refresh Show (2020) - S01E%02d.mkv", n))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("video %d-%d", nonce, n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	folder, err := h.folders.Create(ctx, catalog.CreateFolderInput{Name: fmt.Sprintf("Complete refresh %d", nonce), Type: "series", Paths: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	seriesID := "series-tvdb-" + show.tvdb
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = h.pool.Exec(cleanup, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folder.ID)
		_, _ = h.pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = h.pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})

	wantLinked := func(when string) {
		t.Helper()
		got := h.links(t, folder.ID)
		for n := 1; n <= 2; n++ {
			name := fmt.Sprintf("Refresh Show (2020) - S01E%02d.mkv", n)
			want := [2]string{seriesID, fmt.Sprintf("episode-tvdb-%s-1-%d", show.tvdb, n)}
			if got[name] != want {
				t.Fatalf("%s: %s linked to %v, want %v", when, name, got[name], want)
			}
		}
	}
	if _, err := h.scan.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	// Make the initial scan's jobs claimable. The refresh itself queues new
	// work and must still wait for it to become claimable.
	if _, err := h.pool.Exec(ctx, `
		UPDATE series_root_match_queue SET available_at = NOW()
		WHERE media_folder_id = $1 AND observed_root_path = $2`, folder.ID, filepath.Dir(seasonDir)); err != nil {
		t.Fatalf("make fixture match jobs claimable: %v", err)
	}
	if _, err := h.worker.ProcessAllByFolderAndPathPrefix(ctx, folder.ID, root, time.Time{}); err != nil {
		t.Fatalf("match fixture: %v", err)
	}
	wantLinked("before complete refresh")

	targetID := fmt.Sprintf(idPattern, show.tvdb)
	req, err := h.resolver.ResolveWithMode(ctx, targetID, adminjob.ItemRefreshModeComplete)
	if err != nil {
		t.Fatalf("resolve for complete refresh: %v", err)
	}
	result, err := h.refresh.Execute(ctx, *req, nil)
	if err != nil {
		t.Fatalf("complete refresh: %v", err)
	}
	if result.DetailContentID != targetID || h.refreshed.contentID != targetID {
		t.Errorf("complete refresh rebuilt %q and refreshed %q, want %q", result.DetailContentID, h.refreshed.contentID, targetID)
	}
	wantLinked("after complete refresh")
}

// stubWrongMatch is a TVDB match that was wrong for a show. Once the admin
// takes it off the library it answers nothing.
type stubWrongMatch struct {
	tvdb, sportarr string
	off            bool
}

func (p *stubWrongMatch) Slug() string       { return "tvdb" }
func (p *stubWrongMatch) Name() string       { return "TheTVDB" }
func (p *stubWrongMatch) ForTypes() []string { return []string{"series"} }

func (p *stubWrongMatch) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	if p.off {
		return nil, nil
	}
	return []metadata.SearchResult{{Name: "Race Series", Year: 2019, Provider: "tvdb",
		ProviderIDs: map[string]string{"tvdb": p.tvdb}}}, nil
}

func (p *stubWrongMatch) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if p.off || req.ProviderIDs["tvdb"] != p.tvdb {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{HasMetadata: true, Title: "Wrong Show", Year: 2019, FirstAirDate: "2019-03-08",
		Overview: "The wrong show's overview.", Tagline: "Watch your back.", ContentRating: "TV-MA",
		Genres: []string{"Documentary"}, Ratings: metadata.Ratings{IMDB: 8.6},
		ProviderIDs: map[string]string{"tvdb": p.tvdb}}, nil
}

// stubSpecialist is a specialist provider that knows the show by its own ID
// and supplies little more than its title.
type stubSpecialist struct{ sportarr string }

func (p *stubSpecialist) Slug() string       { return "sportarr" }
func (p *stubSpecialist) Name() string       { return "Sportarr" }
func (p *stubSpecialist) ForTypes() []string { return []string{"series"} }

func (p *stubSpecialist) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	return []metadata.SearchResult{{Name: "Race Series", Year: 2019, Provider: "sportarr",
		ProviderIDs: map[string]string{"sportarr": p.sportarr}}}, nil
}

func (p *stubSpecialist) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if req.ProviderIDs["sportarr"] != p.sportarr {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{HasMetadata: true, Title: "Race Series", Genres: []string{"Motorsport"},
		ProviderIDs: map[string]string{"sportarr": p.sportarr}}, nil
}

// TestCompleteRefreshDropsAClearedMatchsValuesDB covers #4336: a series
// matched to the wrong TVDB show keeps that show's content ID after the admin
// clears its TVDB ID, and a Complete Refresh merges the rebuilt series back
// onto it through the ID it shares with the right provider. The refresh that
// ends the Complete Refresh must leave only what the providers send now and
// keep the admin's locks.
func TestCompleteRefreshDropsAClearedMatchsValuesDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	nonce := time.Now().UnixNano() % 100_000_000
	wrong := &stubWrongMatch{tvdb: strconv.FormatInt(710_000_000+nonce, 10), sportarr: fmt.Sprintf("lg-%d", nonce)}
	h := newSeriesRefreshHarness(t, dsn, &stubSpecialist{sportarr: wrong.sportarr}, wrong)
	h.refreshed.live = h.service
	ctx := t.Context()
	folderID, seriesID := seedWrongMatchSeries(t, h, nonce, wrong)

	// The admin clears the wrong TVDB ID, sets and locks the genres, and takes
	// TVDB off the library.
	empty, genres := "", []string{"Admin Genre"}
	locks := []int{int(metadata.FieldGenres)}
	if err := h.items.UpdateMetadata(ctx, seriesID, &catalog.MetadataUpdate{TvdbID: &empty, Genres: &genres, LockedFields: &locks}); err != nil {
		t.Fatalf("clear the wrong TVDB ID: %v", err)
	}
	wrong.off = true

	req, err := h.resolver.ResolveWithMode(ctx, seriesID, adminjob.ItemRefreshModeComplete)
	if err != nil {
		t.Fatalf("resolve for complete refresh: %v", err)
	}
	if _, err := h.refresh.Execute(ctx, *req, nil); err != nil {
		t.Fatalf("complete refresh: %v", err)
	}

	// The rebuilt series is still the item that holds both files and their
	// history, and each file is still linked to an episode.
	links := h.links(t, folderID)
	for n := 1; n <= 2; n++ {
		name := fmt.Sprintf("Race Series (2019) - S01E%02d.mkv", n)
		link, ok := links[name]
		switch {
		case !ok:
			t.Errorf("%s is missing after complete refresh", name)
		case link[0] != seriesID:
			t.Errorf("%s linked to %q after complete refresh, want %q", name, link[0], seriesID)
		case link[1] == "":
			t.Errorf("%s lost its episode link after complete refresh", name)
		}
	}
	assertRebuiltSeries(t, h, seriesID, "sportarr="+wrong.sportarr)
}

// seedWrongMatchSeries scans and matches a two-episode series to the wrong
// match, then stores the specialist's ID on it as well, as when its NFO
// declares it. It returns the library and the series' content ID.
func seedWrongMatchSeries(t *testing.T, h *seriesRefreshHarness, nonce int64, wrong *stubWrongMatch) (int, string) {
	t.Helper()
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "sport")
	seasonDir := filepath.Join(root, "Race Series (2019)", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		path := filepath.Join(seasonDir, fmt.Sprintf("Race Series (2019) - S01E%02d.mkv", n))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("race %d-%d", nonce, n)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	folder, err := h.folders.Create(ctx, catalog.CreateFolderInput{Name: fmt.Sprintf("Cleared match %d", nonce), Type: "series", Paths: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	seriesID := "series-tvdb-" + wrong.tvdb
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = h.pool.Exec(cleanup, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folder.ID)
		_, _ = h.pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = $1`, seriesID)
		_, _ = h.pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})

	if _, err := h.scan.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	if _, err := h.pool.Exec(ctx, `
		UPDATE series_root_match_queue SET available_at = NOW()
		WHERE media_folder_id = $1 AND observed_root_path = $2`, folder.ID, filepath.Dir(seasonDir)); err != nil {
		t.Fatalf("make fixture match jobs claimable: %v", err)
	}
	if _, err := h.worker.ProcessAllByFolderAndPathPrefix(ctx, folder.ID, root, time.Time{}); err != nil {
		t.Fatalf("match fixture: %v", err)
	}
	if before, err := h.items.GetByID(ctx, seriesID); err != nil || before.Tagline != "Watch your back." {
		t.Fatalf("fixture series %+v, err %v; want the wrong match's tagline", before, err)
	}
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO media_item_provider_ids (content_id, item_type, provider, provider_id)
		VALUES ($1, 'series', 'sportarr', $2)`, seriesID, wrong.sportarr); err != nil {
		t.Fatalf("store the specialist's ID: %v", err)
	}
	return folder.ID, seriesID
}

// assertRebuiltSeries checks that the rebuilt series holds only the
// specialist's values, the admin's locked genres and wantIDs.
func assertRebuiltSeries(t *testing.T, h *seriesRefreshHarness, seriesID, wantIDs string) {
	t.Helper()
	after, err := h.items.GetByID(t.Context(), seriesID)
	if err != nil {
		t.Fatalf("load rebuilt series: %v", err)
	}
	firstAired, imdbRating := "", 0.0
	if after.FirstAirDate != nil {
		firstAired = *after.FirstAirDate
	}
	if after.RatingIMDB != nil {
		imdbRating = *after.RatingIMDB
	}
	if after.Title != "Race Series" || after.Overview != "" || after.Tagline != "" || after.ContentRating != "" ||
		after.Year != 0 || firstAired != "" || imdbRating != 0 || after.TvdbID != "" {
		t.Errorf("rebuilt series = {title %q, overview %q, tagline %q, rating %q, year %d, first aired %q, imdb rating %v, tvdb %q}; want only the specialist's values",
			after.Title, after.Overview, after.Tagline, after.ContentRating, after.Year, firstAired, imdbRating, after.TvdbID)
	}
	if len(after.Genres) != 1 || after.Genres[0] != "Admin Genre" {
		t.Errorf("rebuilt series genres = %v, want the admin's locked genres", after.Genres)
	}
	var ids string
	if err := h.pool.QueryRow(t.Context(), `
		SELECT COALESCE(string_agg(provider || '=' || provider_id, ',' ORDER BY provider), '')
		FROM media_item_provider_ids WHERE content_id = $1`, seriesID).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if ids != wantIDs {
		t.Errorf("rebuilt series provider IDs = %q, want %q", ids, wantIDs)
	}
}
