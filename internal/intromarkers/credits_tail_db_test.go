package intromarkers

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

// TestCandidateCodecsPostgres loads the probed codecs the credits tail pass
// decides its outputs by, with a missing codec read as none.
func TestCandidateCodecsPostgres(t *testing.T) {
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
	if err := pool.QueryRow(ctx, `UPDATE media_files SET codec_video = 'hevc', codec_audio = NULL WHERE id = $1 RETURNING episode_id`, fileID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	candidates, err := NewRepository(pool).ListCandidatesForEpisode(ctx, episodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].CodecVideo != "hevc" || candidates[0].CodecAudio != "" {
		t.Fatalf("candidates %+v, want video hevc and no audio", candidates)
	}
	if !candidates[0].hasVideo() || candidates[0].hasAudio() {
		t.Fatal("the candidate should have video and no audio")
	}
}
