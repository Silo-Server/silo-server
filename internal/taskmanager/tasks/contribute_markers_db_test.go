package tasks

import (
	"context"
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

// dbSubmitter is a marker submitter that accepts every submission and records
// which files reached it.
type dbSubmitter struct {
	mu        sync.Mutex
	id        string
	required  []string
	submitted []markers.SubmissionRequest
}

func (s *dbSubmitter) ID() string { return s.id }
func (s *dbSubmitter) FetchMarkers(context.Context, markers.Request) (markers.Result, error) {
	return markers.Result{}, nil
}
func (s *dbSubmitter) SubmitMarker(_ context.Context, req markers.SubmissionRequest) (markers.SubmissionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitted = append(s.submitted, req)
	return markers.SubmissionResult{ID: fmt.Sprintf("sub-%d", len(s.submitted)), Status: markers.SubmissionStatusPending}, nil
}
func (s *dbSubmitter) FetchUserStats(context.Context) (markers.UserStats, error) {
	return markers.UserStats{}, nil
}
func (s *dbSubmitter) SubmissionRequirements() markers.SubmissionRequirements {
	return markers.SubmissionRequirements{RequiredExternalIDs: s.required}
}

func (s *dbSubmitter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.submitted)
}

type dbProviderConfig map[string]markers.ProviderConfig

func (c dbProviderConfig) Load(context.Context) (map[string]markers.ProviderConfig, error) {
	return c, nil
}

// seededFileLoader loads only the files a test seeded, so candidates other
// packages' tests leave in the shared database never reach its providers.
type seededFileLoader struct {
	files  *scanner.FileRepository
	seeded []int
}

func (l seededFileLoader) GetByIDs(ctx context.Context, ids []int) ([]*models.MediaFile, error) {
	var own []int
	for _, id := range ids {
		if slices.Contains(l.seeded, id) {
			own = append(own, id)
		}
	}
	if len(own) == 0 {
		return nil, nil
	}
	return l.files.GetByIDs(ctx, own)
}

// recordingRunner records the files a run hands to the contribution service.
type recordingRunner struct {
	*markers.ContributionService
	files []int
}

func (r *recordingRunner) ContributeFile(ctx context.Context, file *models.MediaFile, opts markers.ContributeOptions) ([]markers.ContributionOutcome, error) {
	r.files = append(r.files, file.ID)
	return r.ContributionService.ContributeFile(ctx, file, opts)
}

type declinedFiles struct {
	tvdbOnly              string
	noTMDB, lowConfidence int
}

// seedDeclinedFiles seeds two episode files with scanner intros: noTMDB
// (0.98) in a series with only a TVDB ID, and lowConfidence (0.93) in a series
// with TMDB and TVDB IDs.
func seedDeclinedFiles(t *testing.T, pool *pgxpool.Pool, tag string) declinedFiles {
	t.Helper()
	ctx := context.Background()
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name) VALUES ('shows', $1) RETURNING id`, tag).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	tvdbOnly, tmdbSeries := tag+"-tvdb-series", tag+"-tmdb-series"
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(ctx, `DELETE FROM episodes WHERE series_id = ANY($1)`, []string{tvdbOnly, tmdbSeries})
		_, _ = pool.Exec(ctx, `DELETE FROM media_items WHERE content_id = ANY($1)`, []string{tvdbOnly, tmdbSeries})
		_, _ = pool.Exec(ctx, `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO media_items (content_id, type, title, status, genres, tmdb_id, tvdb_id) VALUES
		    ($1, 'series', 'TVDB Only', 'matched', '{}'::text[], '', '81189'),
		    ($2, 'series', 'TMDB Series', 'matched', '{}'::text[], '1396', '81190')`,
		tvdbOnly, tmdbSeries); err != nil {
		t.Fatalf("seed series: %v", err)
	}
	seedFile := func(seriesID string, confidence float64) int {
		t.Helper()
		episodeID := seriesID + "-s1e1"
		if _, err := pool.Exec(ctx, `
			INSERT INTO episodes (content_id, series_id, season_number, episode_number, title)
			VALUES ($1, $2, 1, 1, 'Pilot')`, episodeID, seriesID); err != nil {
			t.Fatalf("seed episode: %v", err)
		}
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, episode_id, duration, intro_start, intro_end,
			                         intro_markers_source, intro_markers_confidence)
			VALUES ($1, $2, 1, $3, 1800, 10, 70, 'scanner', $4)
			RETURNING id`, folderID, fmt.Sprintf("/%s/%s.mkv", tag, seriesID), episodeID, confidence).Scan(&id); err != nil {
			t.Fatalf("seed media file: %v", err)
		}
		return id
	}
	return declinedFiles{
		tvdbOnly:      tvdbOnly,
		noTMDB:        seedFile(tvdbOnly, 0.98),
		lowConfidence: seedFile(tmdbSeries, 0.93),
	}
}

// A provider that declines a file for a reason the next run cannot change (a
// required external ID the series lacks, or a confidence below the provider's
// own minimum) writes no claim. The run after it must not select the file
// again for that provider, and the file must become eligible once the reason
// goes away.
func TestContributeMarkersTaskDoesNotReselectDeclinedFiles(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	tag := fmt.Sprintf("decline-test-%d", time.Now().UnixNano())
	seed := seedDeclinedFiles(t, pool, tag)
	// noTMDB's series has only a TVDB ID; lowConfidence clears one
	// provider's minimum but not the other's.
	noTMDB, lowConfidence := seed.noTMDB, seed.lowConfidence

	needsTMDB := &dbSubmitter{id: tag + "-tmdb", required: []string{markers.ExternalIDKeyTMDB}}
	// strict keys submissions by TVDB, so a TMDB ID added later does not
	// move its target.
	strict := &dbSubmitter{id: tag + "-strict", required: []string{markers.ExternalIDKeyTVDB}}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM marker_contributions WHERE provider = ANY($1)`, []string{needsTMDB.id, strict.id})
	})

	registry := markers.NewRegistry(nil)
	for _, sub := range []*dbSubmitter{needsTMDB, strict} {
		if err := registry.Register(sub); err != nil {
			t.Fatalf("register provider: %v", err)
		}
	}
	config := dbProviderConfig{
		needsTMDB.id: {Provider: needsTMDB.id, ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.9},
		strict.id:    {Provider: strict.id, ContributeEnabled: true, ContributeAutoLocal: true, ContributeMinConfidence: 0.97},
	}
	store := markers.NewContributionStore(pool)
	service := markers.NewContributionService(registry, markers.NewDBExternalIDResolver(pool), config, store, nil)
	loader := seededFileLoader{files: scanner.NewFileRepository(pool), seeded: []int{noTMDB, lowConfidence}}

	run := func() []int {
		t.Helper()
		runner := &recordingRunner{ContributionService: service}
		task := NewContributeMarkersTask(runner, store, loader)
		task.wait = noWait
		if err := task.Execute(ctx, &contribTestProgress{}); err != nil {
			t.Fatalf("Execute: %v", err)
		}
		slices.Sort(runner.files)
		return runner.files
	}

	// First run: needsTMDB takes the TMDB-keyed file and declines the other;
	// strict takes the high-confidence file and declines the other.
	if got, want := run(), []int{noTMDB, lowConfidence}; !slices.Equal(got, want) {
		t.Fatalf("first run selected %v, want %v", got, want)
	}
	if needsTMDB.count() != 1 || strict.count() != 1 {
		t.Fatalf("first run submissions = (needsTMDB %d, strict %d), want one each", needsTMDB.count(), strict.count())
	}

	// Second run: every remaining provider declines each file for the same
	// reason as before, so neither file is selected.
	if got := run(); len(got) != 0 {
		t.Fatalf("second run selected %v, want no file: the declining provider cannot accept them", got)
	}

	// The series gains a TMDB ID and the low intro is re-detected with a
	// higher confidence: each file is eligible again for the provider that
	// declined it.
	if _, err := pool.Exec(ctx, `UPDATE media_items SET tmdb_id = '1399' WHERE content_id = $1`, seed.tvdbOnly); err != nil {
		t.Fatalf("add tmdb id: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE media_files SET intro_markers_confidence = 0.99 WHERE id = $1`, lowConfidence); err != nil {
		t.Fatalf("raise confidence: %v", err)
	}
	if got, want := run(), []int{noTMDB, lowConfidence}; !slices.Equal(got, want) {
		t.Fatalf("run after the reasons changed selected %v, want %v", got, want)
	}
	if needsTMDB.count() != 2 || strict.count() != 2 {
		t.Fatalf("submissions after the reasons changed = (needsTMDB %d, strict %d), want two each", needsTMDB.count(), strict.count())
	}
	if got := run(); len(got) != 0 {
		t.Fatalf("final run selected %v, want no file once every provider holds a claim", got)
	}
}
