package metadata_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/catalog/filesplit"
	"github.com/Silo-Server/silo-server/internal/catalog/reattribute"
	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/libraryingest"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// catalogMovie is one title the stub TMDB knows.
type catalogMovie struct {
	tmdb, imdb, tvdb, title string
	year                    int
}

func (m catalogMovie) ids() map[string]string {
	ids := map[string]string{"tmdb": m.tmdb, "imdb": m.imdb}
	if m.tvdb != "" {
		ids["tvdb"] = m.tvdb
	}
	return ids
}

// stubTMDB answers like the TMDB plugin: a search with a TMDB, IMDb or TVDB ID
// looks that ID up, and only a search without one falls back to the title.
// Its title search is loose the way TMDB's is: every title of the year that
// shares a word with the query comes back.
type stubTMDB struct {
	mu     sync.Mutex
	movies []catalogMovie
}

func (p *stubTMDB) Slug() string       { return "tmdb" }
func (p *stubTMDB) Name() string       { return "The Movie Database" }
func (p *stubTMDB) ForTypes() []string { return []string{"movie"} }

func (p *stubTMDB) find(ids map[string]string) (catalogMovie, bool) {
	for _, key := range []string{"tmdb", "imdb", "tvdb"} {
		value := ids[key]
		if value == "" {
			continue
		}
		for _, movie := range p.movies {
			if movie.ids()[key] == value {
				return movie, true
			}
		}
		return catalogMovie{}, false
	}
	return catalogMovie{}, false
}

func (p *stubTMDB) Search(_ context.Context, query metadata.SearchQuery) ([]metadata.SearchResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := func(m catalogMovie) metadata.SearchResult {
		return metadata.SearchResult{Name: m.title, Year: m.year, ProviderIDs: m.ids(), Provider: "tmdb"}
	}
	if query.ProviderIDs["tmdb"] != "" || query.ProviderIDs["imdb"] != "" || query.ProviderIDs["tvdb"] != "" {
		if movie, ok := p.find(query.ProviderIDs); ok {
			return []metadata.SearchResult{result(movie)}, nil
		}
		return nil, nil
	}
	var out []metadata.SearchResult
	words := strings.Fields(strings.ToLower(query.Title))
	for _, movie := range p.movies {
		if query.Year != 0 && (movie.year < query.Year-1 || movie.year > query.Year+1) {
			continue
		}
		for _, word := range words {
			if strings.Contains(strings.ToLower(movie.title), word) {
				out = append(out, result(movie))
				break
			}
		}
	}
	return out, nil
}

func (p *stubTMDB) GetMetadata(_ context.Context, req metadata.MetadataRequest) (*metadata.MetadataResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	movie, ok := p.find(req.ProviderIDs)
	if !ok {
		return &metadata.MetadataResult{}, nil
	}
	return &metadata.MetadataResult{
		HasMetadata: true,
		Title:       movie.title,
		Year:        movie.year,
		ReleaseDate: fmt.Sprintf("%d-06-01", movie.year),
		ProviderIDs: movie.ids(),
	}, nil
}

// ingestHarness runs the production scan → classify → match path for movie
// libraries against Postgres: the scanner, the movie match queue, the match
// worker and the library ingest executor that drives them.
type ingestHarness struct {
	pool     *pgxpool.Pool
	folders  *catalog.FolderRepository
	executor *libraryingest.Executor
}

func newIngestHarness(t *testing.T, providers ...metadata.Provider) *ingestHarness {
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
	skipped := metadata.NewSkippedRootRepository(pool)
	service := metadata.NewMetadataService(nil, nil, nil,
		catalog.NewItemRepository(pool), catalog.NewProviderIDRepository(pool),
		catalog.NewEpisodeRepository(pool), catalog.NewSeasonRepository(pool),
		catalog.NewLibraryItemRepository(pool), folders,
		nil, files, skipped, nil, catalog.NewRootClaimRepository(pool))
	service.SetLibraryRootResolver(folders)
	metadata.MatchWithProvidersForTest(service, "en", providers...)

	movieQueue := metadata.NewMovieMatchQueueRepository(pool, files)
	worker := metadata.NewMatchWorker(service, files, 2, 10, time.Hour)
	worker.SetMovieFileClaimer(movieQueue)

	scan := scanner.NewScanner(files, "", nil, 1, false, 0)
	scan.SetMetadataQueueProducer(metadata.NewMatchQueueCoordinator(movieQueue, metadata.NewSeriesRootMatchQueueRepository(pool)))
	scan.SetMovieQueueSyncer(movieQueue)

	return &ingestHarness{
		pool:     pool,
		folders:  folders,
		executor: libraryingest.NewExecutor(scan, worker, folders, skipped, nil, nil),
	}
}

// movieLibrary creates a movie library over root and removes it, with every
// item its files reached, when the test ends.
func (h *ingestHarness) movieLibrary(t *testing.T, root string) *models.MediaFolder {
	t.Helper()
	ctx := context.Background()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	folder, err := h.folders.Create(ctx, catalog.CreateFolderInput{
		Name:  fmt.Sprintf("Tagged merge %d", time.Now().UnixNano()),
		Type:  "movies",
		Paths: []string{root},
	})
	if err != nil {
		t.Fatalf("create library: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		_, _ = h.pool.Exec(cleanup, `
			DELETE FROM media_items WHERE content_id IN (
				SELECT content_id FROM media_files WHERE media_folder_id = $1 AND content_id IS NOT NULL
				UNION SELECT content_id FROM media_item_libraries WHERE media_folder_id = $1)`, folder.ID)
		_, _ = h.pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folder.ID)
	})
	return folder
}

func (h *ingestHarness) ingest(t *testing.T, folder *models.MediaFolder) {
	t.Helper()
	if _, err := h.executor.IngestFolder(t.Context(), folder); err != nil {
		t.Fatalf("ingest library %d: %v", folder.ID, err)
	}
}

// fileItem is the item a file belongs to.
type fileItem struct {
	contentID, status, tmdb, imdb, tvdb string
}

func (h *ingestHarness) itemOf(t *testing.T, path string) fileItem {
	t.Helper()
	var cid, status, tmdb, imdb, tvdb *string
	if err := h.pool.QueryRow(t.Context(), `
		SELECT mf.content_id, mi.status, mi.tmdb_id, mi.imdb_id, mi.tvdb_id
		FROM media_files mf LEFT JOIN media_items mi ON mi.content_id = mf.content_id
		WHERE mf.file_path = $1`, path).Scan(&cid, &status, &tmdb, &imdb, &tvdb); err != nil {
		t.Fatalf("load file %s: %v", path, err)
	}
	deref := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	return fileItem{deref(cid), deref(status), deref(tmdb), deref(imdb), deref(tvdb)}
}

// expectMatched fails unless the file belongs to the matched item of movie.
func (h *ingestHarness) expectMatched(t *testing.T, path string, movie catalogMovie) fileItem {
	t.Helper()
	got := h.itemOf(t, path)
	want := "movie-tmdb-" + movie.tmdb
	if got.contentID != want || got.status != "matched" || got.tmdb != movie.tmdb || got.imdb != movie.imdb {
		t.Errorf("%s is on %+v, want matched %s (tmdb %s, imdb %s)", filepath.Base(path), got, want, movie.tmdb, movie.imdb)
	}
	return got
}

func writeMovieFile(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("video "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The movies from issue #2263. Each pair has its own TMDB and IMDb IDs, but
// the provider data lists one TVDB ID for both movies of the pair: community
// cross-ID mappings get neighboring titles wrong. Which provider supplied
// the shared ID on the reporter's server is unknown; any provider can.
var (
	bataan       = catalogMovie{tmdb: "43506", imdb: "tt0035664", tvdb: "66484", title: "Bataan", year: 1943}
	batman       = catalogMovie{tmdb: "125249", imdb: "tt0035665", tvdb: "66484", title: "Batman", year: 1943}
	monk         = catalogMovie{tmdb: "595980", imdb: "tt1864420", tvdb: "357111", title: "Monk", year: 1968}
	monkInEurope = catalogMovie{tmdb: "595983", imdb: "tt1864421", tvdb: "357111", title: "Monk in Europe", year: 1968}
)

func taggedName(m catalogMovie) string {
	return fmt.Sprintf("%s (%d) [imdb-%s][tmdb-%s]", m.title, m.year, m.imdb, m.tmdb)
}

// TestScanKeepsTaggedMovieFoldersApartDB covers issue #2263 end to end: a
// movie folder tagged with its own TMDB and IMDb IDs becomes its own matched
// movie and is never merged into another movie whose IDs differ from its tags,
// whether that movie is in another library from an earlier scan or is new in
// the same scan. The TVDB ID both movies of a pair claim stays with the movie
// that held it first.
func TestScanKeepsTaggedMovieFoldersApartDB(t *testing.T) {
	tmdb := &stubTMDB{movies: []catalogMovie{bataan, batman, monk, monkInEurope}}
	h := newIngestHarness(t, tmdb)
	root := t.TempDir()

	notRated := h.movieLibrary(t, filepath.Join(root, "Movies-NotRated"))
	bataanFile := writeMovieFile(t, filepath.Join(notRated.Paths[0], taggedName(bataan)), taggedName(bataan)+".mkv")
	h.ingest(t, notRated)
	h.expectMatched(t, bataanFile, bataan)

	movies := h.movieLibrary(t, filepath.Join(root, "Archive"))
	batmanFile := writeMovieFile(t, filepath.Join(movies.Paths[0], taggedName(batman)),
		taggedName(batman)+"[DVD][AC3 2.0][x264].mkv")
	monkFile := writeMovieFile(t, filepath.Join(movies.Paths[0], taggedName(monk)), taggedName(monk)+".mkv")
	monkInEuropeFile := writeMovieFile(t, filepath.Join(movies.Paths[0], taggedName(monkInEurope)), taggedName(monkInEurope)+".mkv")
	h.ingest(t, movies)

	if got := h.expectMatched(t, bataanFile, bataan); got.tvdb != bataan.tvdb {
		t.Errorf("Bataan has TVDB %q, want %s", got.tvdb, bataan.tvdb)
	}
	if got := h.expectMatched(t, batmanFile, batman); got.tvdb != "" {
		t.Errorf("Batman has TVDB %q, which belongs to Bataan", got.tvdb)
	}
	monkItem := h.expectMatched(t, monkFile, monk)
	monkInEuropeItem := h.expectMatched(t, monkInEuropeFile, monkInEurope)
	if (monkItem.tvdb == monk.tvdb) == (monkInEuropeItem.tvdb == monk.tvdb) {
		t.Errorf("Monk has TVDB %q and Monk in Europe %q, want exactly one with %s",
			monkItem.tvdb, monkInEuropeItem.tvdb, monk.tvdb)
	}
	for _, path := range []string{bataanFile, batmanFile, monkFile, monkInEuropeFile} {
		var siblings int
		if err := h.pool.QueryRow(t.Context(), `
			SELECT COUNT(*) FROM media_files WHERE content_id = (SELECT content_id FROM media_files WHERE file_path = $1)`,
			path).Scan(&siblings); err != nil {
			t.Fatal(err)
		}
		if siblings != 1 {
			t.Errorf("%s shares its movie with %d other files, want none", filepath.Base(path), siblings-1)
		}
	}
}

// TestScanStillMergesCopiesOfOneMovieDB is the other side of #2263: copies of
// one movie in different libraries still become versions of one item, tagged
// or not, including after the provider changed the movie's TVDB mapping since
// the first copy matched.
func TestScanStillMergesCopiesOfOneMovieDB(t *testing.T) {
	heat := catalogMovie{tmdb: "949", imdb: "tt0113277", tvdb: "1001", title: "Heat", year: 1995}
	tmdb := &stubTMDB{movies: []catalogMovie{heat, {tmdb: "11", imdb: "tt0076759", title: "Heat Wave", year: 1995}}}
	h := newIngestHarness(t, tmdb)
	root := t.TempDir()

	first := h.movieLibrary(t, filepath.Join(root, "Movies"))
	taggedFile := writeMovieFile(t, filepath.Join(first.Paths[0], taggedName(heat)), taggedName(heat)+".mkv")
	h.ingest(t, first)
	h.expectMatched(t, taggedFile, heat)

	second := h.movieLibrary(t, filepath.Join(root, "Movies-4K"))
	untaggedFile := writeMovieFile(t, filepath.Join(second.Paths[0], "Heat (1995)"), "Heat (1995) 2160p.mkv")
	h.ingest(t, second)
	h.expectMatched(t, untaggedFile, heat)

	tmdb.mu.Lock()
	tmdb.movies[0].tvdb = "2002"
	tmdb.mu.Unlock()
	third := h.movieLibrary(t, filepath.Join(root, "Movies-Remux"))
	remappedFile := writeMovieFile(t, filepath.Join(third.Paths[0], "Heat (1995)"), "Heat (1995) Remux.mkv")
	h.ingest(t, third)
	h.expectMatched(t, remappedFile, heat)
}

// TestRescanAfterSplitKeepsTaggedMovieApartDB covers the reproduction in
// #2263: a tagged movie that an earlier build merged into another movie is
// split back out as unmatched, and the rescan the split runs matches it as its
// own movie instead of merging it again.
func TestRescanAfterSplitKeepsTaggedMovieApartDB(t *testing.T) {
	tmdb := &stubTMDB{movies: []catalogMovie{bataan, batman}}
	h := newIngestHarness(t, tmdb)
	ctx := t.Context()
	root := t.TempDir()

	notRated := h.movieLibrary(t, filepath.Join(root, "Movies-NotRated"))
	bataanFile := writeMovieFile(t, filepath.Join(notRated.Paths[0], taggedName(bataan)), taggedName(bataan)+".mkv")
	h.ingest(t, notRated)
	movies := h.movieLibrary(t, filepath.Join(root, "Archive"))
	batmanDir := filepath.Join(movies.Paths[0], taggedName(batman))
	batmanFile := writeMovieFile(t, batmanDir, taggedName(batman)+"[DVD][AC3 2.0][x264].mkv")
	h.ingest(t, movies)

	// The state an earlier build's merge left: Batman's file is a version of
	// Bataan, and Batman has no item.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := h.pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`UPDATE media_files SET content_id = $1 WHERE file_path = $2`, "movie-tmdb-"+bataan.tmdb, batmanFile)
	exec(`DELETE FROM media_items WHERE content_id = $1`, "movie-tmdb-"+batman.tmdb)
	exec(`INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		"movie-tmdb-"+bataan.tmdb, movies.ID)

	// Split it out as unmatched, as POST /admin/items/{id}/split does with
	// target.unmatched and no override.
	var file filesplit.File
	if err := h.pool.QueryRow(ctx, `
		SELECT id, content_id, media_folder_id, file_path, COALESCE(canonical_root_path, ''), COALESCE(observed_root_path, ''),
		       group_key_version, COALESCE(content_group_key, ''), COALESCE(base_type, '')
		FROM media_files WHERE file_path = $1`, batmanFile).Scan(&file.ID, &file.ContentID, &file.MediaFolderID, &file.FilePath,
		&file.CanonicalRootPath, &file.ObservedRootPath, &file.GroupKeyVersion, &file.ContentGroupKey, &file.BaseType); err != nil {
		t.Fatalf("load Batman file: %v", err)
	}
	local := contentid.ForLocal(batmanFile)
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO media_items (content_id, type, title, year, status) VALUES ($1, 'movie', 'Bataan', 1943, 'unmatched')`, local); err != nil {
		t.Fatalf("create split target: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO media_item_libraries (content_id, media_folder_id) VALUES ($1, $2)`, local, movies.ID); err != nil {
		t.Fatalf("split target library: %v", err)
	}
	if _, err := filesplit.Move(ctx, tx, filesplit.Options{
		FromContentID: "movie-tmdb-" + bataan.tmdb, ToContentID: local, ItemType: "movie",
		Files: []filesplit.File{file}, HistoryMode: reattribute.HistoryModeKeep,
	}); err != nil {
		t.Fatalf("split: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.itemOf(t, batmanFile); got.contentID != local {
		t.Fatalf("split left Batman's file on %q, want %s", got.contentID, local)
	}

	if _, err := h.executor.IngestSubtree(ctx, movies, batmanDir); err != nil {
		t.Fatalf("rescan the split root: %v", err)
	}
	h.expectMatched(t, batmanFile, batman)
	h.expectMatched(t, bataanFile, bataan)
}

// TestScanKeepsTaggedMovieApartFromTitleWithItsTaggedIDDB covers the scan's
// tag lookup for #2263: the provider lists Batman's IMDb ID on Bataan, so
// Bataan's IMDb column holds the ID Batman's folder is tagged with. The
// folder's TMDB tag contradicts Bataan, so Batman still becomes its own movie
// instead of a version of Bataan.
func TestScanKeepsTaggedMovieApartFromTitleWithItsTaggedIDDB(t *testing.T) {
	wrongBataan := catalogMovie{tmdb: bataan.tmdb, imdb: batman.imdb, title: bataan.title, year: bataan.year}
	tmdb := &stubTMDB{movies: []catalogMovie{wrongBataan, batman}}
	h := newIngestHarness(t, tmdb)
	root := t.TempDir()

	notRated := h.movieLibrary(t, filepath.Join(root, "Movies-NotRated"))
	bataanName := fmt.Sprintf("Bataan (1943) [tmdb-%s]", bataan.tmdb)
	bataanFile := writeMovieFile(t, filepath.Join(notRated.Paths[0], bataanName), bataanName+".mkv")
	h.ingest(t, notRated)
	h.expectMatched(t, bataanFile, wrongBataan)

	movies := h.movieLibrary(t, filepath.Join(root, "Archive"))
	batmanFile := writeMovieFile(t, filepath.Join(movies.Paths[0], taggedName(batman)), taggedName(batman)+".mkv")
	h.ingest(t, movies)

	if got := h.itemOf(t, batmanFile); got.contentID != "movie-tmdb-"+batman.tmdb || got.status != "matched" {
		t.Errorf("Batman is on %+v, want matched movie-tmdb-%s", got, batman.tmdb)
	}
	h.expectMatched(t, bataanFile, wrongBataan)
}
