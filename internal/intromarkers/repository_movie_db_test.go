package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

func movieTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(t.Context(), pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

// movieFixture seeds libraries, items, and files for the movie candidate
// queries and returns the file IDs by name.
type movieFixture struct {
	pool    *pgxpool.Pool
	prefix  string
	folders map[string]int
	files   map[string]int
}

func seedMovieFixture(t *testing.T, pool *pgxpool.Pool) *movieFixture {
	t.Helper()
	ctx := t.Context()
	f := &movieFixture{pool: pool, prefix: fmt.Sprintf("movie-credits-%d-", time.Now().UnixNano()), folders: map[string]int{}, files: map[string]int{}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var items []string
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, folderID := range f.folders {
			if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
				t.Errorf("clean movie fixture: %v", err)
			}
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_items WHERE content_id = ANY($1)`, items); err != nil {
			t.Errorf("clean movie fixture: %v", err)
		}
	})
	for name, spec := range map[string]struct {
		kind    string
		enabled bool
	}{
		"movies": {"movies", true}, "mixed": {"mixed", true}, "series": {"series", true}, "off": {"movies", false},
	} {
		var id int
		if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name, enabled, intro_detection_enabled) VALUES ($1, $2, true, $3) RETURNING id`,
			spec.kind, f.prefix+name, spec.enabled).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.folders[name] = id
	}
	item := func(name, kind string) string {
		id := f.prefix + name
		exec(`INSERT INTO media_items (content_id, type, title) VALUES ($1, $2, $3)`, id, kind, name)
		items = append(items, id)
		return id
	}
	movie, series := item("movie", "movie"), item("show", "series")
	extra := f.prefix + "trailer"
	exec(`INSERT INTO media_extras (content_id, parent_id, kind) VALUES ($1, $2, 'trailer')`, extra, movie)

	type file struct {
		folder, content string
		duration        int
		extra           any
		parts           any
		missing         bool
		creditsSource   any
		age             time.Duration
	}
	for name, spec := range map[string]file{
		"older":          {folder: "movies", content: movie, duration: 7200, age: 2 * time.Hour},
		"newer":          {folder: "movies", content: movie, duration: 7200, age: time.Hour},
		"mixed":          {folder: "mixed", content: movie, duration: 6000, age: 3 * time.Hour},
		"scannerCredits": {folder: "movies", content: movie, duration: 7200, creditsSource: "scanner", age: 4 * time.Hour},
		"onlineCredits":  {folder: "movies", content: movie, duration: 7200, creditsSource: "online"},
		"extra":          {folder: "movies", content: movie, duration: 7200, extra: extra},
		"multiPart":      {folder: "movies", content: movie, duration: 7200, parts: 2},
		"short":          {folder: "movies", content: movie, duration: 1199},
		"flagOff":        {folder: "off", content: movie, duration: 7200},
		"seriesLibrary":  {folder: "series", content: movie, duration: 7200},
		"notAMovie":      {folder: "movies", content: series, duration: 7200},
		"missing":        {folder: "movies", content: movie, duration: 7200, missing: true},
	} {
		var missingSince any
		if spec.missing {
			missingSince = time.Now().UTC()
		}
		var creditsStart, creditsEnd any
		if spec.creditsSource != nil {
			creditsStart, creditsEnd = 6600.0, 7200.0
		}
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (
			    media_folder_id, file_path, content_id, extra_id, file_hash, file_size, duration,
			    presentation_part_total, missing_since, credits_start, credits_end, credits_markers_source, created_at
			) VALUES ($1, $2, $3, $4, $5, 1000, $6, $7, $8, $9, $10, $11, $12)
			RETURNING id`,
			f.folders[spec.folder], "/"+f.prefix+name+".mkv", spec.content, spec.extra, f.prefix+"hash-"+name, spec.duration,
			spec.parts, missingSince, creditsStart, creditsEnd, spec.creditsSource, time.Now().Add(-spec.age),
		).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		f.files[name] = id
	}
	return f
}

// listed returns the fixture files ListMovieCandidates returns, by name, in
// order.
func (f *movieFixture) listed(t *testing.T, repo *Repository, node string) []string {
	t.Helper()
	candidates, _, err := repo.ListMovieCandidates(t.Context(), node, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return f.names(candidateIDs(candidates))
}

// names returns the names of the fixture files among ids, in order.
func (f *movieFixture) names(ids []int) []string {
	names := map[int]string{}
	for name, id := range f.files {
		names[id] = name
	}
	var got []string
	for _, id := range ids {
		if name, ok := names[id]; ok {
			got = append(got, name)
		}
	}
	return got
}

func candidateIDs(candidates []Candidate) []int {
	ids := make([]int, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.FileID)
	}
	return ids
}

// pagedMovieRepository records the pages of the scheduled movie listing.
type pagedMovieRepository struct {
	*Repository
	mu     sync.Mutex
	pages  [][]int
	onList func()
}

func (r *pagedMovieRepository) ListMovieCandidates(ctx context.Context, node string, after *movieCandidateCursor, limit int) ([]Candidate, *movieCandidateCursor, error) {
	candidates, next, err := r.Repository.ListMovieCandidates(ctx, node, after, limit)
	r.mu.Lock()
	r.pages = append(r.pages, candidateIDs(candidates))
	onList := r.onList
	r.mu.Unlock()
	if onList != nil {
		onList()
	}
	return candidates, next, err
}

func (r *pagedMovieRepository) listed() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Concat(r.pages...)
}

func pagedMovieAnalyzer(repo *pagedMovieRepository, sampler *fakeMovieSampler) *Analyzer {
	return &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, movieSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler), moviePageSize: 1, workers: 1,
	}
}

// The scheduled run lists movies a page at a time and analyzes each once,
// even those that stay eligible after their analysis.
func TestRunMoviesPagesCandidatesPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := &pagedMovieRepository{Repository: NewRepository(pool)}
	if _, err := pagedMovieAnalyzer(repo, &fakeMovieSampler{}).RunMovies(t.Context(), allMarkerKinds, nil); err != nil {
		t.Fatalf("RunMovies: %v", err)
	}
	want := []string{"newer", "older", "mixed", "scannerCredits"}
	if got := f.names(repo.listed()); !slices.Equal(got, want) {
		t.Fatalf("listed across pages %v, want %v", got, want)
	}
	repo.mu.Lock()
	pages := len(repo.pages)
	repo.mu.Unlock()
	if pages < len(want) {
		t.Fatalf("%d pages, want at least %d with one movie per page", pages, len(want))
	}
	// Without video metadata they are decided unusable on every analysis
	// and nothing is stored, so all of them are still eligible.
	if got := f.listed(t, repo.Repository, "node-a"); !slices.Equal(got, want) {
		t.Fatalf("candidates after the run %v, want %v still eligible", got, want)
	}
}

// The movie budget covers listing the movies, and a spent budget stops the
// run from listing further pages.
func TestRunMoviesStopsPagingAtTheBudgetPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET codec_video = 'h264' WHERE file_path LIKE $1`, "/"+f.prefix+"%"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		// listing and sampling spend the budget when set.
		listing, sampling bool
		wantTails         int
	}{
		{name: "spent while analyzing", sampling: true, wantTails: 1},
		{name: "spent while listing", listing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			now := time.Date(2026, 9, 26, 3, 30, 0, 0, time.UTC)
			spend := func() {
				mu.Lock()
				now = now.Add(61 * time.Minute)
				mu.Unlock()
			}
			repo := &pagedMovieRepository{Repository: NewRepository(pool)}
			sampler := &fakeMovieSampler{}
			if tc.listing {
				repo.onList = spend
			}
			if tc.sampling {
				sampler.onSample = spend
			}
			analyzer := pagedMovieAnalyzer(repo, sampler)
			analyzer.now = func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				return now
			}
			summary, err := analyzer.RunMovies(t.Context(), allMarkerKinds, nil)
			if err != nil {
				t.Fatalf("RunMovies: %v", err)
			}
			repo.mu.Lock()
			pages := len(repo.pages)
			repo.mu.Unlock()
			if pages != 1 || sampler.tailCount() != tc.wantTails || !summary.MovieBudgetExhausted {
				t.Fatalf("%d pages, %d tail passes, summary %+v; want one page, %d tail passes, and the budget exhausted",
					pages, sampler.tailCount(), summary, tc.wantTails)
			}
		})
	}
}

func TestListMovieCandidatesPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := NewRepository(pool)
	ctx := t.Context()

	// Extras, multi-part films, short files, files of libraries with
	// detection off or of series libraries, non-movie items, missing files,
	// and online credits are left out; the rest come newest first.
	if got, want := f.listed(t, repo, "node-a"), []string{"newer", "older", "mixed", "scannerCredits"}; !slices.Equal(got, want) {
		t.Fatalf("candidates %v, want %v", got, want)
	}
	candidates, err := repo.ListMovieCandidatesForFile(ctx, f.files["older"])
	if err != nil || len(candidates) != 1 {
		t.Fatalf("file candidates %+v, %v", candidates, err)
	}
	older := candidates[0]
	if older.EpisodeID != "" || older.SeasonID != "" || older.ContentID != f.prefix+"movie" || older.DurationSeconds != 7200 {
		t.Fatalf("candidate %+v", older)
	}
	if candidates, err := repo.ListMovieCandidatesForFile(ctx, f.files["extra"]); err != nil || len(candidates) != 0 {
		t.Fatalf("extra as a candidate: %+v, %v", candidates, err)
	}
	items, err := repo.ListMovieCandidatesForItem(ctx, f.prefix+"movie")
	if err != nil || len(items) != 5 {
		t.Fatalf("item candidates %d, %v; want the four listed files and the one with online credits", len(items), err)
	}

	// A stored tail for the file as it is keeps a file out: complete,
	// unusable, or failed on this server and backing off.
	store := func(name string, status string) {
		t.Helper()
		candidates, err := repo.ListMovieCandidatesForFile(ctx, f.files[name])
		if err != nil || len(candidates) != 1 {
			t.Fatalf("%s: %+v, %v", name, candidates, err)
		}
		candidate := candidates[0]
		spec := movieTailSpec(candidate)
		switch status {
		case ArtifactFailed:
			err = repo.RecordArtifactFailure(ctx, ArtifactFailure{
				MediaFileID: candidate.FileID, ArtifactKey: spec.key, ArtifactIdentity: spec.window.identity(candidate),
				RecordedBy: "node-a", Error: "timeout",
			})
		case ArtifactUnusable:
			err = repo.UpsertArtifact(ctx, Artifact{
				MediaFileID: candidate.FileID, ArtifactKey: spec.key, ArtifactIdentity: spec.window.identity(candidate),
				Status: ArtifactUnusable, Detail: tailDetailSparse,
			})
		case tailDetailNoVideo:
			// Stored from probe metadata by an earlier build.
			err = repo.UpsertArtifact(ctx, Artifact{
				MediaFileID: candidate.FileID, ArtifactKey: spec.key, ArtifactIdentity: spec.window.identity(candidate),
				Status: ArtifactUnusable, Detail: tailDetailNoVideo,
			})
		default:
			err = repo.UpsertArtifact(ctx, Artifact{
				MediaFileID: candidate.FileID, ArtifactKey: spec.key, ArtifactIdentity: spec.window.identity(candidate),
				Status: ArtifactComplete, PayloadFormat: creditsTailFormat, ItemCount: 1, Payload: []byte{1},
			})
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	store("newer", ArtifactComplete)
	store("older", ArtifactUnusable)
	store("mixed", ArtifactFailed)
	if got, want := f.listed(t, repo, "node-a"), []string{"scannerCredits"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after analysis %v, want %v", got, want)
	}
	// Another server retries the failure, after files never analyzed.
	if got, want := f.listed(t, repo, "node-b"), []string{"scannerCredits", "mixed"}; !slices.Equal(got, want) {
		t.Fatalf("candidates on another server %v, want %v", got, want)
	}
	// A tail ruled out by probe metadata is decided on every analysis, so
	// such a row from an earlier build does not keep the file out.
	store("scannerCredits", tailDetailNoVideo)
	if got, want := f.listed(t, repo, "node-a"), []string{"scannerCredits"}; !slices.Equal(got, want) {
		t.Fatalf("candidates with a metadata unusable row %v, want %v", got, want)
	}
	// An episode tail stored for a file does not count as its movie tail.
	candidates, _ = repo.ListMovieCandidatesForFile(ctx, f.files["scannerCredits"])
	episode := episodeTailSpec(candidates[0])
	if err := repo.UpsertArtifact(ctx, Artifact{
		MediaFileID: candidates[0].FileID, ArtifactKey: episode.key, ArtifactIdentity: movieTailSpec(candidates[0]).window.identity(candidates[0]),
		Status: ArtifactUnusable, Detail: tailDetailNoVideo,
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.listed(t, repo, "node-a"); !slices.Equal(got, []string{"scannerCredits"}) {
		t.Fatalf("candidates %v, want the episode-keyed tail ignored", got)
	}
	// A replaced file is analyzed again.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_hash = 'replaced' WHERE id = $1`, f.files["newer"]); err != nil {
		t.Fatal(err)
	}
	if got, want := f.listed(t, repo, "node-a"), []string{"newer", "scannerCredits"}; !slices.Equal(got, want) {
		t.Fatalf("candidates after replacing a file %v, want %v", got, want)
	}
}

func TestMarkerItemEligibilityPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := NewRepository(pool)
	ctx := t.Context()

	movie, err := repo.MarkerItemEligibility(ctx, f.prefix+"movie")
	if err != nil || movie.Kind != MarkerItemMovie || !movie.HasMediaFiles || !movie.IntroDetectionEnabled {
		t.Fatalf("movie eligibility %+v, %v", movie, err)
	}
	if _, err := repo.MarkerItemEligibility(ctx, f.prefix+"show"); !errors.Is(err, ErrMarkerItemNotFound) {
		t.Fatalf("series eligibility error %v, want ErrMarkerItemNotFound", err)
	}
	// A movie whose files all sit in a library with detection off is known
	// but not enabled.
	if _, err := pool.Exec(ctx, `UPDATE media_folders SET intro_detection_enabled = false WHERE id = ANY($1)`,
		[]int{f.folders["movies"], f.folders["mixed"], f.folders["series"]}); err != nil {
		t.Fatal(err)
	}
	movie, err = repo.MarkerItemEligibility(ctx, f.prefix+"movie")
	if err != nil || movie.Kind != MarkerItemMovie || !movie.HasMediaFiles || movie.IntroDetectionEnabled {
		t.Fatalf("movie eligibility with detection off %+v, %v", movie, err)
	}

	episodeFile := seedSilenceBackfillFixture(t, pool)[0]
	var episodeID string
	if err := pool.QueryRow(ctx, `SELECT episode_id FROM media_files WHERE id = $1`, episodeFile).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	episode, err := repo.MarkerItemEligibility(ctx, episodeID)
	if err != nil || episode.Kind != MarkerItemEpisode || !episode.HasMediaFiles || !episode.IntroDetectionEnabled {
		t.Fatalf("episode eligibility %+v, %v", episode, err)
	}
}

// A movie candidate's file identity matches the stored file, so its credits
// are written.
func TestPatchMovieCreditsPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := NewRepository(pool)
	candidates, err := repo.ListMovieCandidatesForFile(t.Context(), f.files["older"])
	if err != nil || len(candidates) != 1 {
		t.Fatalf("candidates %+v, %v", candidates, err)
	}
	candidate := candidates[0]
	applied, err := repo.PatchMarker(t.Context(), MarkerPatch{
		Kind: kindCredits, ExpectedFile: candidate.expectedFile(), FileID: candidate.FileID,
		Start: 6600, End: 7200, Source: "scanner", Confidence: creditsVideoLetteredConfidence,
		Algorithm: CreditsVideoAlgorithm, DetectedAt: time.Now().UTC(),
	})
	if err != nil || !applied {
		t.Fatalf("PatchMarker = %t, %v; want applied", applied, err)
	}
	candidates, err = repo.ListMovieCandidatesForFile(t.Context(), f.files["older"])
	if err != nil || len(candidates) != 1 || !candidates[0].marker(kindCredits).matches(Segment{
		Start: 6600, End: 7200, Confidence: creditsVideoLetteredConfidence, Algorithm: CreditsVideoAlgorithm,
	}) {
		t.Fatalf("credits after patch %+v, %v", candidates, err)
	}
}
