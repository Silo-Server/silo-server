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
// runs only after the scope is rebuilt and resolved, which is what these tests
// cover, and its provider chain is not wired here.
type recordedRefresh struct{ targetType, contentID string }

func (r *recordedRefresh) RefreshItem(context.Context, string) error { return nil }
func (r *recordedRefresh) RefreshItemForLibrary(context.Context, string, int) error {
	return nil
}
func (r *recordedRefresh) RefreshTargetForLibrary(_ context.Context, targetType, contentID string, _ int) error {
	r.targetType, r.contentID = targetType, contentID
	return nil
}
func (r *recordedRefresh) ResetTargetForLibrary(context.Context, string, string, int) error {
	return nil
}

// seriesRefreshHarness runs the production scan, series root queue, match
// worker and item refresh against Postgres.
type seriesRefreshHarness struct {
	pool      *pgxpool.Pool
	files     *scanner.FileRepository
	folders   *catalog.FolderRepository
	ingest    *libraryingest.Executor
	resolver  *adminjob.ItemRefreshResolver
	refresh   *adminjob.ItemRefreshExecutor
	refreshed *recordedRefresh
}

func newSeriesRefreshHarness(t *testing.T, providers ...metadata.Provider) *seriesRefreshHarness {
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
	return &seriesRefreshHarness{
		pool: pool, files: files, folders: folders, ingest: ingest,
		resolver: adminjob.NewItemRefreshResolver(items, seasons, episodes, folders, files),
		refresh: adminjob.NewItemRefreshExecutor(folders, files, rootClaims, catalog.NewGroupClaimRepository(pool), skipped,
			seasons, episodes, ingest, scanqueue.NewRepository(pool), refreshed, nil, nil),
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
	nonce := time.Now().UnixNano() % 100_000_000
	show := &stubTVDB{tvdb: strconv.FormatInt(700_000_000+nonce, 10), title: "Refresh Show", year: 2020}
	h := newSeriesRefreshHarness(t, show)
	ctx := t.Context()

	root := filepath.Join(t.TempDir(), "tv")
	seasonDir := filepath.Join(root, "Refresh Show (2020)", "Season 01")
	if err := os.MkdirAll(seasonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n := 1; n <= 2; n++ {
		path := filepath.Join(seasonDir, fmt.Sprintf("Refresh Show (2020) - S01E%02d.mkv", n))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("video %d", n)), 0o644); err != nil {
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
	if _, err := h.ingest.IngestFolder(ctx, folder); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	wantLinked("after the library scan")

	for _, target := range []struct{ name, contentID string }{
		{"series", seriesID},
		{"season", "season-tvdb-" + show.tvdb + "-1"},
		{"episode", "episode-tvdb-" + show.tvdb + "-1-2"},
	} {
		req, err := h.resolver.ResolveWithMode(ctx, target.contentID, adminjob.ItemRefreshModeComplete)
		if err != nil {
			t.Fatalf("resolve the %s for a complete refresh: %v", target.name, err)
		}
		result, err := h.refresh.Execute(ctx, *req, nil)
		if err != nil {
			t.Fatalf("complete refresh of the %s: %v", target.name, err)
		}
		if result.DetailContentID != target.contentID || h.refreshed.contentID != target.contentID {
			t.Errorf("complete refresh of the %s rebuilt %q and refreshed %q, want %q", target.name, result.DetailContentID, h.refreshed.contentID, target.contentID)
		}
		wantLinked("after a complete refresh of the " + target.name)
	}
}
