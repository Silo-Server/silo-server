package intromarkers

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/migrations"
)

// TestPatchMarkerCreditsPostgres writes a credits marker through PatchMarker
// and reads it back on the candidate, leaving the file's intro untouched.
func TestPatchMarkerCreditsPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileID := seedSilenceBackfillFixture(t, pool)[0]
	var episodeID string
	if err := pool.QueryRow(ctx, `SELECT episode_id FROM media_files WHERE id = $1`, fileID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	load := func() Candidate {
		t.Helper()
		candidates, err := repo.ListCandidatesForEpisode(ctx, episodeID)
		if err != nil {
			t.Fatal(err)
		}
		if len(candidates) != 1 {
			t.Fatalf("episode candidates = %d, want 1", len(candidates))
		}
		return candidates[0]
	}
	before := load()
	if before.marker(kindCredits).present() || !before.ownsMarker(kindCredits) {
		t.Fatalf("fixture credits = %+v, want none", before.marker(kindCredits))
	}

	applied, err := repo.PatchMarker(ctx, MarkerPatch{
		Kind:         kindCredits,
		ExpectedFile: before.expectedFile(),
		FileID:       fileID,
		Start:        1380,
		End:          1490,
		Source:       models.MarkerSourceScanner,
		Confidence:   0.9,
		Algorithm:    "credits-test:v1",
		DetectedAt:   time.Now().UTC(),
	})
	if err != nil || !applied {
		t.Fatalf("PatchMarker(credits) = %t, %v; want applied", applied, err)
	}

	after := load()
	credits := after.marker(kindCredits)
	if !credits.present() || *credits.Start != 1380 || *credits.End != 1490 ||
		after.effectiveSource(kindCredits) != models.MarkerSourceScanner ||
		credits.Algorithm == nil || *credits.Algorithm != "credits-test:v1" ||
		credits.Confidence == nil || *credits.Confidence != 0.9 {
		t.Fatalf("credits after patch = %+v", credits)
	}
	intro := after.marker(kindIntro)
	if *intro.Start != 60 || *intro.End != 120 || *intro.Algorithm != ChapterAlgorithm {
		t.Fatalf("intro after credits patch = %+v, want the fixture's chapter intro", intro)
	}
}

// TestSeasonStateIsKeyedByAnalysisHashPostgres stores two kinds' season state
// for one group side by side.
func TestSeasonStateIsKeyedByAnalysisHashPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	fileID := seedSilenceBackfillFixture(t, pool)[0]
	state := SeasonState{AnalysisGroupKey: "default|default|und", InputSignature: "signature", Status: seasonStatusComplete}
	if err := pool.QueryRow(ctx, `
		SELECT e.season_id, mf.media_folder_id
		FROM media_files mf JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.id = $1`, fileID).Scan(&state.SeasonID, &state.MediaFolderID); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	introHash := DefaultConfig("ffmpeg").AnalysisConfigHash()
	otherHash := ArtifactConfigHash("credits_test", "season")
	if err := repo.UpsertSeasonState(ctx, state, introHash); err != nil {
		t.Fatal(err)
	}
	if other, err := repo.LoadSeasonState(ctx, state, otherHash); err != nil || other != nil {
		t.Fatalf("state under another kind's hash = %+v, %v; want none", other, err)
	}
	other := state
	other.Status = seasonStatusNotFound
	if err := repo.UpsertSeasonState(ctx, other, otherHash); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{introHash: seasonStatusComplete, otherHash: seasonStatusNotFound} {
		loaded, err := repo.LoadSeasonState(ctx, state, hash)
		if err != nil {
			t.Fatal(err)
		}
		if loaded == nil || loaded.Status != want {
			t.Fatalf("state under %s = %+v, want status %s", hash, loaded, want)
		}
	}
}
