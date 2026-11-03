package intromarkers

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/migrations"
)

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
	state := SeasonState{AnalysisGroupKey: "default|default|und", InputSignature: "signature", Status: seasonStatusPartial, LastError: "1 fingerprint extraction(s) failed"}
	if err := pool.QueryRow(ctx, `
		SELECT e.season_id, mf.media_folder_id
		FROM media_files mf JOIN episodes e ON e.content_id = mf.episode_id
		WHERE mf.id = $1`, fileID).Scan(&state.SeasonID, &state.MediaFolderID); err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(pool)
	introHash := DefaultConfig("ffmpeg").AnalysisConfigHash()
	otherHash := mediaartifact.ConfigHash("credits_test", "season")
	before := time.Now().Add(-time.Minute)
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
	for hash, want := range map[string]string{introHash: seasonStatusPartial, otherHash: seasonStatusNotFound} {
		loaded, err := repo.LoadSeasonState(ctx, state, hash)
		if err != nil {
			t.Fatal(err)
		}
		if loaded == nil || loaded.Status != want {
			t.Fatalf("state under %s = %+v, want status %s", hash, loaded, want)
		}
		if hash == introHash {
			if loaded.LastError != state.LastError || loaded.AnalyzedAt.Before(before) {
				t.Fatalf("loaded state = %+v, want the saved error and a fresh analyzed_at", loaded)
			}
			now := time.Now()
			if !loaded.settled(now) || loaded.settled(now.Add(partialSeasonRetryInterval)) {
				t.Fatalf("partial state should hold for the retry interval and lapse after it: %+v", loaded)
			}
		}
	}
}
