package intromarkers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
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
	prefix  string
	folders map[string]int
	files   map[string]int
}

func seedMovieFixture(t *testing.T, pool *pgxpool.Pool) *movieFixture {
	t.Helper()
	ctx := t.Context()
	f := &movieFixture{prefix: fmt.Sprintf("movie-credits-%d-", time.Now().UnixNano()), folders: map[string]int{}, files: map[string]int{}}
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

// A changed identity clears a movie's local credits while its stored tail
// still matches the file, so the movie is listed again until a run places its
// credits from that tail, without sampling it again.
func TestMovieWithInvalidatedMarkersIsAnalyzedAgainPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := NewRepository(pool)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `UPDATE media_files SET codec_video = 'h264' WHERE file_path LIKE $1`, "/"+f.prefix+"%"); err != nil {
		t.Fatal(err)
	}
	sampler := &fakeMovieSampler{}
	analyzer := &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, movieSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler), moviePageSize: 10, workers: 1,
	}
	if _, err := analyzer.RunMovies(ctx, allMarkerKinds, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.listed(t, repo, "node-a"); len(got) != 0 {
		t.Fatalf("candidates after the first run %v, want every movie settled", got)
	}
	tails := sampler.tailCount()

	// A rescan that sees a new modification time is an identity change.
	if _, err := pool.Exec(ctx, `UPDATE media_files SET file_modified_at = NOW() WHERE id = $1`, f.files["newer"]); err != nil {
		t.Fatal(err)
	}
	var creditsStart *float64
	if err := pool.QueryRow(ctx, `SELECT credits_start FROM media_files WHERE id = $1`, f.files["newer"]).Scan(&creditsStart); err != nil {
		t.Fatal(err)
	}
	if creditsStart != nil {
		t.Fatalf("credits_start = %v after the identity change, want the trigger to clear it", *creditsStart)
	}
	if got := f.listed(t, repo, "node-a"); len(got) != 1 || got[0] != "newer" {
		t.Fatalf("candidates %v, want the invalidated movie listed again", got)
	}

	if _, err := analyzer.RunMovies(ctx, allMarkerKinds, nil); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT credits_start FROM media_files WHERE id = $1`, f.files["newer"]).Scan(&creditsStart); err != nil {
		t.Fatal(err)
	}
	if creditsStart == nil {
		t.Fatal("credits were not placed again from the stored tail")
	}
	if got := sampler.tailCount(); got != tails {
		t.Fatalf("%d tail passes, want %d: the stored tail is reused", got, tails)
	}
	if got := f.listed(t, repo, "node-a"); len(got) != 0 {
		t.Fatalf("candidates %v after the second run, want the movie settled again", got)
	}
}

// The trigger records an identity change and nothing else.
func TestMarkerInvalidationTimeTracksIdentityChangesPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	ctx := t.Context()
	id := f.files["older"]
	stamp := func() *time.Time {
		t.Helper()
		var at *time.Time
		if err := pool.QueryRow(ctx, `SELECT markers_invalidated_at FROM media_files WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	for _, tc := range []struct {
		name, update string
		want         bool
	}{
		{"unrelated column", `UPDATE media_files SET codec_video = 'hevc' WHERE id = $1`, false},
		{"identity column rewritten unchanged", `UPDATE media_files SET file_hash = file_hash, content_id = content_id WHERE id = $1`, false},
		{"modification time", `UPDATE media_files SET file_modified_at = NOW() WHERE id = $1`, true},
	} {
		if _, err := pool.Exec(ctx, `UPDATE media_files SET markers_invalidated_at = NULL WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, tc.update, id); err != nil {
			t.Fatal(err)
		}
		if got := stamp() != nil; got != tc.want {
			t.Errorf("%s: stamped = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A tail pass that fails on a movie with a metadata unusable row from an
// earlier build replaces that row with the failure, so the movie backs off
// like any other failure instead of being decoded again on every run.
func TestMovieTailFailureReplacesMetadataUnusableRowPostgres(t *testing.T) {
	pool := movieTestPool(t)
	f := seedMovieFixture(t, pool)
	repo := NewRepository(pool)
	ctx := t.Context()
	if _, err := pool.Exec(ctx, `UPDATE media_files SET codec_video = 'h264' WHERE file_path LIKE $1`, "/"+f.prefix+"%"); err != nil {
		t.Fatal(err)
	}
	// Every other listed movie gets a complete tail, so only the one under
	// test is analyzed.
	var target Candidate
	for _, name := range []string{"newer", "older", "mixed", "scannerCredits"} {
		candidates, err := repo.ListMovieCandidatesForFile(ctx, f.files[name])
		if err != nil || len(candidates) != 1 {
			t.Fatalf("%s: %+v, %v", name, candidates, err)
		}
		candidate := candidates[0]
		spec := movieTailSpec(candidate)
		artifact := mediaartifact.Artifact{
			MediaFileID: candidate.FileID, Key: spec.key, Identity: spec.window.identity(candidate),
			Status: mediaartifact.StatusComplete, PayloadFormat: creditsTailFormat, ItemCount: 1, Payload: []byte{1},
		}
		if name == "older" {
			target = candidate
			artifact = mediaartifact.Artifact{
				MediaFileID: candidate.FileID, Key: spec.key, Identity: spec.window.identity(candidate),
				Status: mediaartifact.StatusUnusable, Detail: tailDetailNoVideo,
			}
		}
		if err := repo.UpsertArtifact(ctx, artifact); err != nil {
			t.Fatal(err)
		}
	}
	timeout := &mediasample.Error{Reason: mediasample.ReasonTimeout, Attempts: []mediasample.AttemptError{{
		Decoder: "software", Reason: mediasample.ReasonTimeout, Err: context.DeadlineExceeded,
	}}}
	sampler := &fakeMovieSampler{errs: map[int]error{target.FileID: timeout}}
	analyzer := &Analyzer{
		repo: repo, extractor: &fakeFingerprintExtractor{}, movieSampler: sampler, config: DefaultConfig("ffmpeg"),
		node: "node-a", logger: slog.New(slog.DiscardHandler), moviePageSize: 1, workers: 1,
	}
	for run := 1; run <= 2; run++ {
		if _, err := analyzer.RunMovies(ctx, allMarkerKinds, nil); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if got := sampler.tailCount(); got != 1 {
		t.Fatalf("%d tail passes over two runs, want 1: the failure backs off", got)
	}
	spec := movieTailSpec(target)
	stored, err := repo.LoadArtifact(ctx, target.FileID, spec.key)
	if err != nil {
		t.Fatal(err)
	}
	if stored == nil || stored.Status != mediaartifact.StatusFailed || stored.Detail != "" ||
		stored.FailureCount != 1 || stored.RecordedBy != "node-a" || stored.RetryAfter == nil {
		t.Fatalf("stored tail %+v, want a failure recorded by node-a with a retry time", stored)
	}
	if got := f.listed(t, repo, "node-a"); len(got) != 0 {
		t.Fatalf("candidates %v, want the failed movie backing off", got)
	}
}
