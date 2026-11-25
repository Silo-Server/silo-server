package metadata_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// chainSimProvider is a builtin series provider that numbers one show its own
// way: seasons maps a season number to its episode count. Each provider
// answers for the show by its own ID key (idKey), as a plugin that stores the
// show's TMDB and TVDB IDs makes them available to TMDB and TVDB.
type chainSimProvider struct {
	slug, label string
	idKey, id   string
	seriesIDs   map[string]string
	seasons     map[int]int
	calls       atomic.Int64
}

func (p *chainSimProvider) Slug() string       { return p.slug }
func (p *chainSimProvider) Name() string       { return p.label }
func (p *chainSimProvider) ForTypes() []string { return []string{"series"} }

func (p *chainSimProvider) knows(ids map[string]string) bool {
	p.calls.Add(1)
	return ids[p.idKey] == p.id
}

func (p *chainSimProvider) Search(context.Context, metadata.SearchQuery) ([]metadata.SearchResult, error) {
	p.calls.Add(1)
	return []metadata.SearchResult{{Name: "Chain Show", Year: 2020, ProviderIDs: p.seriesIDs, Provider: p.slug}}, nil
}

func (p *chainSimProvider) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	if !p.knows(req.ProviderIDs) {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{HasMetadata: true, Title: "Chain Show", Year: 2020, ReleaseDate: "2020-01-01", ProviderIDs: p.seriesIDs}, nil
}

func (p *chainSimProvider) episodes(season int) []metadata.EpisodeResult {
	var out []metadata.EpisodeResult
	for n := 1; n <= p.seasons[season]; n++ {
		out = append(out, metadata.EpisodeResult{
			SeasonNumber: season, EpisodeNumber: n,
			Title:       fmt.Sprintf("%s S%02dE%02d", p.label, season, n),
			ProviderIDs: map[string]string{p.idKey: fmt.Sprintf("%s%02d%02d", p.id, season, n)},
		})
	}
	return out
}

func (p *chainSimProvider) GetSeasons(_ context.Context, req metadata.SeasonsRequest) ([]metadata.SeasonResult, error) {
	if !p.knows(req.ProviderIDs) {
		return nil, nil
	}
	var out []metadata.SeasonResult
	for season := range p.seasons {
		out = append(out, metadata.SeasonResult{SeasonNumber: season, Title: fmt.Sprintf("%s Season %d", p.label, season), Episodes: p.episodes(season)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeasonNumber < out[j].SeasonNumber })
	return out, nil
}

func (p *chainSimProvider) GetEpisodes(_ context.Context, req metadata.EpisodesRequest) ([]metadata.EpisodeResult, error) {
	if !p.knows(req.ProviderIDs) {
		return nil, nil
	}
	return p.episodes(req.SeasonNumber), nil
}

type chainSimEpisode struct {
	number, title, tmdbID, tvdbID string
	hasFile, inLibrary            bool
}

func chainSimEpisodes(t *testing.T, pool *pgxpool.Pool, seriesID string) []chainSimEpisode {
	t.Helper()
	rows, err := pool.Query(t.Context(), `
		SELECT e.season_number, e.episode_number, e.title, COALESCE(e.tmdb_id::text, ''), COALESCE(e.tvdb_id::text, ''),
		       EXISTS (SELECT 1 FROM media_files f WHERE f.episode_id = e.content_id),
		       EXISTS (SELECT 1 FROM episode_libraries l WHERE l.episode_id = e.content_id)
		FROM episodes e WHERE e.series_id = $1
		ORDER BY e.season_number, e.episode_number`, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []chainSimEpisode
	for rows.Next() {
		var season, episode int
		var e chainSimEpisode
		if err := rows.Scan(&season, &episode, &e.title, &e.tmdbID, &e.tvdbID, &e.hasFile, &e.inLibrary); err != nil {
			t.Fatal(err)
		}
		e.number = fmt.Sprintf("S%02dE%02d", season, episode)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func logChainSimEpisodes(t *testing.T, when string, episodes []chainSimEpisode) {
	t.Helper()
	t.Logf("%s:", when)
	t.Logf("  %-7s %-24s %-12s %-12s %-5s %s", "episode", "title", "tmdb_id", "tvdb_id", "file", "library link")
	for _, e := range episodes {
		t.Logf("  %-7s %-24s %-12s %-12s %-5t %t", e.number, e.title, e.tmdbID, e.tvdbID, e.hasFile, e.inLibrary)
	}
}

func chainSimNumbers(episodes []chainSimEpisode) []string {
	var out []string
	for _, e := range episodes {
		out = append(out, e.number)
	}
	return out
}

// chainSim is a series library whose provider chain names only the plugin,
// scanned and matched through the production scanner and matcher.
type chainSim struct {
	pool         *pgxpool.Pool
	service      *metadata.MetadataService
	plugin, tmdb *chainSimProvider
	seriesID     string
}

// seedChainSimProviders registers the plugin and TMDB stubs as builtin
// providers, both enabled by default, so folder 0's all-providers fallback
// includes both. It returns their installation.
func seedChainSimProviders(t *testing.T, pool *pgxpool.Pool, nonce int64, providers ...*chainSimProvider) int {
	t.Helper()
	ctx := t.Context()
	var installationID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO plugin_installations (plugin_id, version, install_path, enabled, update_policy, kind)
		VALUES ($1, '0', '/nonexistent/chain-sim', true, 'manual', 'builtin') RETURNING id`,
		fmt.Sprintf("test.chain-sim-%d", nonce)).Scan(&installationID); err != nil {
		t.Fatalf("seed installation: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM plugin_installations WHERE id = $1`, installationID)
	})
	for _, p := range providers {
		metadata.RegisterBuiltinProvider(p.slug, func() metadata.Provider { return p })
		if _, err := pool.Exec(ctx, `
			INSERT INTO plugin_capabilities (plugin_installation_id, capability_type, capability_id, metadata)
			VALUES ($1, 'metadata_provider.v1', $2, $3::jsonb)`, installationID, p.slug,
			fmt.Sprintf(`{"display_name":%q,"default_enabled":true,"default_priority":{"series":1,"season":1,"episode":1}}`, p.label)); err != nil {
			t.Fatalf("seed capability %s: %v", p.slug, err)
		}
	}
	return installationID
}

// newChainSimLibrary writes the show's three episode files and creates a
// series library over them whose chain names only chainSlug.
func newChainSimLibrary(t *testing.T, pool *pgxpool.Pool, chains *metadata.ChainRepository, folders *catalog.FolderRepository, installationID int, chainSlug string, nonce int64) (*models.MediaFolder, string) {
	t.Helper()
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "tv")
	for i, rel := range []string{"Season 01/Chain Show (2020) - S01E01.mkv", "Season 01/Chain Show (2020) - S01E02.mkv", "Season 02/Chain Show (2020) - S02E01.mkv"} {
		path := filepath.Join(root, "Chain Show (2020)", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fmt.Sprintf("video %d-%d", nonce, i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	folder, err := folders.Create(ctx, catalog.CreateFolderInput{Name: fmt.Sprintf("Chain sim %d", nonce), Type: "series", Paths: []string{root}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = pool.Exec(cleanup, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folder.ID)
		_, _ = pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})
	var chain []metadata.ChainEntry
	for _, level := range []string{"series", "season", "episode"} {
		chain = append(chain, metadata.ChainEntry{PluginInstallationID: installationID, CapabilityID: chainSlug, ContentLevel: level, Enabled: true})
	}
	if err := chains.SetChain(ctx, folder.ID, chain); err != nil {
		t.Fatalf("set the library's chain: %v", err)
	}
	return folder, root
}

// scanAndMatchChainSim scans the library and matches it through the
// production queue and worker, returning the matched series.
func scanAndMatchChainSim(t *testing.T, pool *pgxpool.Pool, service *metadata.MetadataService, fileRepo *scanner.FileRepository, folder *models.MediaFolder, root string) string {
	t.Helper()
	ctx := t.Context()
	movieQueue := metadata.NewMovieMatchQueueRepository(pool, fileRepo)
	seriesQueue := metadata.NewSeriesRootMatchQueueRepository(pool)
	worker := metadata.NewMatchWorker(service, fileRepo, 2, 10, time.Hour)
	worker.SetMovieFileClaimer(movieQueue)
	worker.SetSeriesRootClaimer(seriesQueue, true)
	scan := scanner.NewScanner(fileRepo, "", nil, 1, false, 0)
	scan.SetMetadataQueueProducer(metadata.NewMatchQueueCoordinator(movieQueue, seriesQueue))
	scan.SetMovieQueueSyncer(movieQueue)
	scan.SetSeriesQueueSyncer(seriesQueue)

	if _, err := scan.ScanFolder(ctx, folder); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE series_root_match_queue SET available_at = NOW() WHERE media_folder_id = $1`, folder.ID); err != nil {
		t.Fatalf("make match jobs claimable: %v", err)
	}
	if _, err := worker.ProcessAllByFolderAndPathPrefix(ctx, folder.ID, root, time.Time{}); err != nil {
		t.Fatalf("match: %v", err)
	}
	var seriesID string
	if err := pool.QueryRow(ctx, `SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1`, folder.ID).Scan(&seriesID); err != nil {
		t.Fatalf("matched series: %v", err)
	}
	return seriesID
}

func newChainSim(t *testing.T, dsn string) *chainSim {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	nonce := time.Now().UnixNano() % 100_000_000
	tvdbID := strconv.FormatInt(800_000_000+nonce, 10)
	tmdbID := strconv.FormatInt(900_000_000+nonce, 10)
	seriesIDs := map[string]string{"tvdb": tvdbID, "tmdb": tmdbID}
	// The plugin splits the show into two seasons; TMDB keeps one longer season.
	plugin := &chainSimProvider{slug: fmt.Sprintf("sim-plugin-%d", nonce), label: "Plugin", idKey: "tvdb", id: tvdbID, seriesIDs: seriesIDs, seasons: map[int]int{1: 2, 2: 1}}
	tmdb := &chainSimProvider{slug: fmt.Sprintf("sim-tmdb-%d", nonce), label: "TMDB", idKey: "tmdb", id: tmdbID, seriesIDs: seriesIDs, seasons: map[int]int{1: 4}}
	installationID := seedChainSimProviders(t, pool, nonce, plugin, tmdb)

	fileRepo := scanner.NewFileRepository(pool)
	folders := catalog.NewFolderRepository(pool)
	chains := metadata.NewChainRepository(pool)
	service := metadata.NewMetadataService(chains, nil, nil,
		catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool),
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), folders,
		nil, fileRepo, metadata.NewSkippedRootRepository(pool), nil, catalog.NewRootClaimRepository(pool))
	service.SetLibraryRootResolver(folders)

	folder, root := newChainSimLibrary(t, pool, chains, folders, installationID, plugin.slug, nonce)
	seriesID := scanAndMatchChainSim(t, pool, service, fileRepo, folder, root)
	return &chainSim{pool: pool, service: service, plugin: plugin, tmdb: tmdb, seriesID: seriesID}
}

// TestScheduledRefreshKeepsToTheLibraryChainDB covers #4605 through the
// production scan, matcher, chain repository and scheduled refresh. The
// library's chain names one provider (the plugin); another enabled provider
// (TMDB) numbers the same show differently. Folder 0 has no chain rows, so a
// scheduled refresh used to fall back to every enabled provider, adding TMDB's
// episodes the library has no file for and writing TMDB titles and IDs onto
// the plugin's episodes. It must use the library's chain.
func TestScheduledRefreshKeepsToTheLibraryChainDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	sim := newChainSim(t, dsn)

	want := []string{"S01E01", "S01E02", "S02E01"}
	scanned := chainSimEpisodes(t, sim.pool, sim.seriesID)
	logChainSimEpisodes(t, "after scan and match", scanned)
	if got := chainSimNumbers(scanned); !slices.Equal(got, want) {
		t.Fatalf("matched episodes = %v, want the plugin's %v", got, want)
	}

	var episodeID string
	if err := sim.pool.QueryRow(ctx, `SELECT content_id FROM episodes WHERE series_id = $1 AND season_number = 2 AND episode_number = 1`, sim.seriesID).Scan(&episodeID); err != nil {
		t.Fatalf("S02E01: %v", err)
	}
	pluginBefore, tmdbBefore := sim.plugin.calls.Load(), sim.tmdb.calls.Load()
	for _, target := range []struct{ targetType, contentID string }{
		{metadata.RefreshTargetItem, sim.seriesID},
		{metadata.RefreshTargetEpisode, episodeID},
	} {
		if err := sim.service.RefreshScheduledTarget(ctx, target.targetType, target.contentID); err != nil {
			t.Fatalf("scheduled %s refresh of %s: %v", target.targetType, target.contentID, err)
		}
	}
	refreshed := chainSimEpisodes(t, sim.pool, sim.seriesID)
	logChainSimEpisodes(t, "after scheduled item and episode refreshes", refreshed)
	tmdbCalls := sim.tmdb.calls.Load() - tmdbBefore
	t.Logf("provider calls during the refreshes: plugin (in the library's chain) %d, TMDB (not in it) %d",
		sim.plugin.calls.Load()-pluginBefore, tmdbCalls)

	if tmdbCalls != 0 {
		t.Errorf("TMDB, which the library's chain leaves out, was asked %d times", tmdbCalls)
	}
	if got := chainSimNumbers(refreshed); !slices.Equal(got, want) {
		t.Errorf("episodes after the refresh = %v, want only the plugin's %v", got, want)
	}
	for _, e := range refreshed {
		if !e.hasFile || !e.inLibrary {
			t.Errorf("%s has no file or no library link", e.number)
		}
		if e.tmdbID != "" || e.title != "Plugin "+e.number {
			t.Errorf("%s carries title %q and tmdb_id %q, want the plugin's title and no TMDB ID", e.number, e.title, e.tmdbID)
		}
	}
}
