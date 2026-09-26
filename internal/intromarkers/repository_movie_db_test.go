package intromarkers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
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
	candidates, err := repo.ListMovieCandidates(t.Context(), node)
	if err != nil {
		t.Fatal(err)
	}
	names := map[int]string{}
	for name, id := range f.files {
		names[id] = name
	}
	var got []string
	for _, candidate := range candidates {
		if name, ok := names[candidate.FileID]; ok {
			got = append(got, name)
		}
	}
	return got
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
