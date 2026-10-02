package intromarkers

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/migrations"
)

func openArtifactTestPool(t *testing.T) *pgxpool.Pool {
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

// Old and new binaries share the table during a rolling deploy: a binary that
// predates artifact kinds keeps upserting and reading intro fingerprints with
// its own statements, and must read other statuses as cache misses.
func TestIntroFingerprintArtifactsPostgres(t *testing.T) {
	pool := openArtifactTestPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	fileIDs := seedSilenceBackfillFixture(t, pool)
	first, second := fileIDs[0], fileIDs[1]
	cfg := DefaultConfig("ffmpeg")
	candidate := func(fileID int) Candidate {
		var c Candidate
		if err := pool.QueryRow(ctx, `SELECT id, file_hash, file_size, duration FROM media_files WHERE id = $1`, fileID).
			Scan(&c.FileID, &c.FileHash, &c.FileSize, &c.DurationSeconds); err != nil {
			t.Fatal(err)
		}
		return c
	}
	fingerprint := func(c Candidate, points []uint32) Fingerprint {
		return Fingerprint{
			MediaFileID:           c.FileID,
			FileHash:              c.FileHash,
			FileSize:              c.FileSize,
			DurationSeconds:       c.DurationSeconds,
			WindowEndSeconds:      analysisWindowEnd(c.DurationSeconds, cfg),
			AlgorithmVersion:      AlgorithmVersion,
			ConfigHash:            cfg.ConfigHash(),
			FingerprintFormat:     ChromaprintFormat,
			SampleDurationSeconds: float64(len(points)) * DefaultPointHopSeconds,
			Points:                points,
		}
	}

	a := candidate(first)
	if err := repo.UpsertFingerprint(ctx, fingerprint(a, []uint32{1, 2, 3})); err != nil {
		t.Fatal(err)
	}
	var kind, status string
	if err := pool.QueryRow(ctx, `SELECT kind, status FROM media_intro_fingerprints WHERE media_file_id = $1`, first).Scan(&kind, &status); err != nil {
		t.Fatal(err)
	}
	if kind != ArtifactKindIntroFingerprint || status != mediaartifact.StatusComplete {
		t.Fatalf("stored intro fingerprint kind, status = %s, %s", kind, status)
	}
	got, err := repo.LoadFingerprint(ctx, a, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got.Points) != 3 || got.Points[2] != 3 || got.WindowEndSeconds != analysisWindowEnd(a.DurationSeconds, cfg) {
		t.Fatalf("LoadFingerprint() = %+v", got)
	}
	changed := a
	changed.FileSize++
	if got, err := repo.LoadFingerprint(ctx, changed, cfg); err != nil || got != nil {
		t.Fatalf("LoadFingerprint() for a changed file = %+v, %v; want a miss", got, err)
	}

	// The statement binaries before artifact kinds use to store fingerprints.
	b := candidate(second)
	legacy := fingerprint(b, []uint32{7, 8})
	for range 2 {
		if _, err := pool.Exec(ctx, `
			INSERT INTO media_intro_fingerprints (
			    media_file_id, file_hash, file_size, duration_seconds, window_start_seconds, window_end_seconds,
			    algorithm_version, config_hash, fingerprint_format, sample_duration_seconds, point_count, points
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (media_file_id, algorithm_version, config_hash) DO UPDATE SET
			    file_hash = EXCLUDED.file_hash,
			    file_size = EXCLUDED.file_size,
			    duration_seconds = EXCLUDED.duration_seconds,
			    window_start_seconds = EXCLUDED.window_start_seconds,
			    window_end_seconds = EXCLUDED.window_end_seconds,
			    fingerprint_format = EXCLUDED.fingerprint_format,
			    sample_duration_seconds = EXCLUDED.sample_duration_seconds,
			    point_count = EXCLUDED.point_count,
			    points = EXCLUDED.points,
			    updated_at = NOW()`,
			legacy.MediaFileID, legacy.FileHash, legacy.FileSize, legacy.DurationSeconds, legacy.WindowStartSeconds,
			legacy.WindowEndSeconds, legacy.AlgorithmVersion, legacy.ConfigHash, legacy.FingerprintFormat,
			legacy.SampleDurationSeconds, len(legacy.Points), mediasample.EncodeRawFingerprint(legacy.Points),
		); err != nil {
			t.Fatalf("legacy fingerprint upsert: %v", err)
		}
	}
	if got, err := repo.LoadFingerprint(ctx, b, cfg); err != nil || got == nil || len(got.Points) != 2 {
		t.Fatalf("LoadFingerprint() of a legacy row = %+v, %v", got, err)
	}

	// A failed intro row reads as a miss, to this binary and to the old one.
	if _, err := pool.Exec(ctx, `DELETE FROM media_intro_fingerprints WHERE media_file_id = $1`, second); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordArtifactFailure(ctx, mediaartifact.Failure{
		MediaFileID: second,
		Key:         introFingerprintKey(cfg),
		Identity:    introFingerprintIdentity(b, cfg),
		RecordedBy:  "node-a",
		Error:       "ffmpeg exited 1",
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.LoadFingerprint(ctx, b, cfg); err != nil || got != nil {
		t.Fatalf("LoadFingerprint() of a failed row = %+v, %v; want a miss", got, err)
	}
	var format string
	var points []byte
	if err := pool.QueryRow(ctx, `
		SELECT fingerprint_format, points FROM media_intro_fingerprints
		WHERE media_file_id = $1 AND algorithm_version = $2 AND config_hash = $3`,
		second, AlgorithmVersion, cfg.ConfigHash()).Scan(&format, &points); err != nil {
		t.Fatal(err)
	}
	if format == ChromaprintFormat || len(points) != 0 {
		t.Fatalf("failed row format %q with %d payload bytes; an older binary would read it as a fingerprint", format, len(points))
	}
}
