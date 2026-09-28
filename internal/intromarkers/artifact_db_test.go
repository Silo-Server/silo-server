package intromarkers

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
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

// artifactFixture returns two seeded files with the identity the silence
// fixture gives them: file hash, a size of 1000000, and 1500 seconds.
func artifactFixture(t *testing.T, pool *pgxpool.Pool) (int, int, func(int) ArtifactIdentity) {
	t.Helper()
	fileIDs := seedSilenceBackfillFixture(t, pool)
	hashes := map[int]string{}
	for _, id := range fileIDs[:2] {
		var hash string
		if err := pool.QueryRow(t.Context(), `SELECT file_hash FROM media_files WHERE id = $1`, id).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		hashes[id] = hash
	}
	identity := func(fileID int) ArtifactIdentity {
		return ArtifactIdentity{FileHash: hashes[fileID], FileSize: 1000000, DurationSeconds: 1500, WindowStartSeconds: 1200, WindowEndSeconds: 1500}
	}
	return fileIDs[0], fileIDs[1], identity
}

func TestArtifactStatusesPostgres(t *testing.T) {
	pool := openArtifactTestPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	first, second, identity := artifactFixture(t, pool)
	key := ArtifactKey{Kind: "test_tail", AlgorithmVersion: 1, ConfigHash: ArtifactConfigHash("test_tail", "v1")}
	now := time.Now().UTC().Truncate(time.Microsecond)

	complete := Artifact{
		MediaFileID:           first,
		ArtifactKey:           key,
		ArtifactIdentity:      identity(first),
		Status:                ArtifactComplete,
		PayloadFormat:         "test:v1",
		SampleDurationSeconds: 300,
		ItemCount:             2,
		Payload:               []byte{1, 2, 3, 4},
		RecordedBy:            "node-a",
	}
	if err := repo.UpsertArtifact(ctx, complete); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadArtifact(ctx, first, key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Status != ArtifactComplete || string(loaded.Payload) != string(complete.Payload) ||
		loaded.ItemCount != 2 || loaded.PayloadFormat != "test:v1" || loaded.SampleDurationSeconds != 300 || loaded.RecordedBy != "node-a" {
		t.Fatalf("loaded complete artifact = %+v", loaded)
	}
	if got := loaded.State(identity(first), "node-b", now); got != ArtifactReady {
		t.Fatalf("complete artifact state = %d, want ready", got)
	}
	replaced := identity(first)
	replaced.FileHash = "replaced"
	if got := loaded.State(replaced, "node-a", now); got != ArtifactMissing {
		t.Fatalf("complete artifact for a replaced file = %d, want missing", got)
	}
	for _, other := range []ArtifactKey{
		{Kind: "other_kind", AlgorithmVersion: key.AlgorithmVersion, ConfigHash: key.ConfigHash},
		{Kind: key.Kind, AlgorithmVersion: 2, ConfigHash: key.ConfigHash},
		{Kind: key.Kind, AlgorithmVersion: key.AlgorithmVersion, ConfigHash: ArtifactConfigHash("test_tail", "v2")},
	} {
		if got, err := repo.LoadArtifact(ctx, first, other); err != nil || got != nil {
			t.Fatalf("LoadArtifact(%+v) = %+v, %v; want no row", other, got, err)
		}
	}

	// A failure never replaces a settled result for the same file.
	if err := repo.RecordArtifactFailure(ctx, ArtifactFailure{
		MediaFileID: first, ArtifactKey: key, ArtifactIdentity: identity(first), RecordedBy: "node-a", Error: "late", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if loaded, err = repo.LoadArtifact(ctx, first, key); err != nil || loaded.Status != ArtifactComplete {
		t.Fatalf("complete artifact after a failure = %+v, %v; want it kept", loaded, err)
	}

	unusable := Artifact{
		MediaFileID:      second,
		ArtifactKey:      key,
		ArtifactIdentity: identity(second),
		Status:           ArtifactUnusable,
		Detail:           "no_video",
		RecordedBy:       "node-a",
	}
	withPayload := unusable
	withPayload.Payload = []byte{1}
	if err := repo.UpsertArtifact(ctx, withPayload); err == nil {
		t.Fatal("an unusable artifact with a payload must be rejected")
	}
	if err := repo.UpsertArtifact(ctx, unusable); err != nil {
		t.Fatal(err)
	}
	all, err := repo.LoadArtifacts(ctx, []int{first, second, 0}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[first].Status != ArtifactComplete || all[second].Status != ArtifactUnusable || all[second].Detail != "no_video" {
		t.Fatalf("LoadArtifacts() = %+v, want the complete and the unusable row", all)
	}
	unusableRow := all[second]
	if got := unusableRow.State(identity(second), "node-b", now); got != ArtifactSkipped {
		t.Fatalf("unusable artifact state = %d, want skipped", got)
	}
	resized := identity(second)
	resized.FileSize++
	if got := unusableRow.State(resized, "node-b", now); got != ArtifactMissing {
		t.Fatalf("unusable artifact for a changed file = %d, want missing", got)
	}
	if err := repo.UpsertArtifact(ctx, Artifact{MediaFileID: second, ArtifactKey: key, ArtifactIdentity: identity(second), Status: ArtifactFailed}); err == nil {
		t.Fatal("UpsertArtifact must not write failures")
	}

	// Failures back off on the recording server only, and escalate on retry.
	failKey := ArtifactKey{Kind: key.Kind, AlgorithmVersion: key.AlgorithmVersion, ConfigHash: ArtifactConfigHash("test_tail", "failing")}
	fail := ArtifactFailure{MediaFileID: second, ArtifactKey: failKey, ArtifactIdentity: identity(second), RecordedBy: "node-a", Error: "ffmpeg exited 1", At: now}
	anonymous := fail
	anonymous.RecordedBy = ""
	if err := repo.RecordArtifactFailure(ctx, anonymous); err == nil {
		t.Fatal("RecordArtifactFailure must require the recording server")
	}
	if err := repo.RecordArtifactFailure(ctx, fail); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.LoadArtifact(ctx, second, failKey)
	if err != nil {
		t.Fatal(err)
	}
	if failed == nil || failed.Status != ArtifactFailed || failed.FailureCount != 1 || failed.LastError != "ffmpeg exited 1" ||
		failed.RetryAfter == nil || !failed.RetryAfter.Equal(now.Add(12*time.Hour)) || len(failed.Payload) != 0 || failed.RecordedBy != "node-a" {
		t.Fatalf("failed artifact = %+v", failed)
	}
	if got := failed.State(identity(second), "node-a", now.Add(time.Hour)); got != ArtifactSkipped {
		t.Fatalf("failed artifact on its server = %d, want skipped", got)
	}
	if got := failed.State(identity(second), "node-b", now.Add(time.Hour)); got != ArtifactMissing {
		t.Fatalf("failed artifact on another server = %d, want missing", got)
	}
	if got := failed.State(identity(second), "node-a", now.Add(13*time.Hour)); got != ArtifactMissing {
		t.Fatalf("failed artifact after its backoff = %d, want missing", got)
	}
	fail.At = now.Add(13 * time.Hour)
	if err := repo.RecordArtifactFailure(ctx, fail); err != nil {
		t.Fatal(err)
	}
	if failed, err = repo.LoadArtifact(ctx, second, failKey); err != nil || failed.FailureCount != 2 || !failed.RetryAfter.Equal(fail.At.Add(24*time.Hour)) {
		t.Fatalf("second failure = %+v, %v; want count 2 retrying after a day", failed, err)
	}

	// A later success clears the failure.
	succeeded := complete
	succeeded.MediaFileID = second
	succeeded.ArtifactKey = failKey
	succeeded.ArtifactIdentity = identity(second)
	if err := repo.UpsertArtifact(ctx, succeeded); err != nil {
		t.Fatal(err)
	}
	if failed, err = repo.LoadArtifact(ctx, second, failKey); err != nil || failed.Status != ArtifactComplete ||
		failed.FailureCount != 0 || failed.LastError != "" || failed.RetryAfter != nil {
		t.Fatalf("artifact after success = %+v, %v; want complete with no failure", failed, err)
	}

	// A key derived by two kinds must not let one overwrite the other.
	collision := complete
	collision.Kind = "other_kind"
	if err := repo.UpsertArtifact(ctx, collision); !errors.Is(err, ErrArtifactKindConflict) {
		t.Fatalf("UpsertArtifact over another kind's key = %v, want ErrArtifactKindConflict", err)
	}
	if loaded, err = repo.LoadArtifact(ctx, first, key); err != nil || loaded == nil || loaded.Kind != key.Kind {
		t.Fatalf("original artifact after a kind collision = %+v, %v", loaded, err)
	}
}

// Two servers can analyze a new file at once. A failure recorded while the
// other server's first successful write is still in flight must not replace it.
func TestArtifactFailureKeepsConcurrentFirstResultPostgres(t *testing.T) {
	pool := openArtifactTestPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	first, second, identity := artifactFixture(t, pool)
	key := ArtifactKey{Kind: "test_tail", AlgorithmVersion: 1, ConfigHash: ArtifactConfigHash("test_tail", "race")}
	now := time.Now().UTC().Truncate(time.Microsecond)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := execArtifactUpsert(ctx, tx, Artifact{
		MediaFileID: first, ArtifactKey: key, ArtifactIdentity: identity(first), Status: ArtifactComplete,
		PayloadFormat: "test:v1", ItemCount: 1, Payload: []byte{1}, RecordedBy: "node-b",
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- repo.RecordArtifactFailure(ctx, ArtifactFailure{
			MediaFileID: first, ArtifactKey: key, ArtifactIdentity: identity(first), RecordedBy: "node-a", Error: "ffmpeg exited 1", At: now,
		})
	}()
	// The failure write reaches the key only after its own read found no row.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query LIKE '%INSERT INTO media_intro_fingerprints%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("RecordArtifactFailure returned %v before the first result committed", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("RecordArtifactFailure never waited on the in-flight first result")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.LoadArtifact(ctx, first, key)
	if err != nil || loaded == nil || loaded.Status != ArtifactComplete || loaded.RecordedBy != "node-b" {
		t.Fatalf("artifact after a concurrent failure = %+v, %v; want the complete result kept", loaded, err)
	}

	// A failure over another kind's key reports the collision.
	if err := repo.UpsertArtifact(ctx, Artifact{
		MediaFileID: second, ArtifactKey: key, ArtifactIdentity: identity(second), Status: ArtifactComplete,
		PayloadFormat: "test:v1", ItemCount: 1, Payload: []byte{1}, RecordedBy: "node-b",
	}); err != nil {
		t.Fatal(err)
	}
	other := key
	other.Kind = "other_kind"
	if err := repo.RecordArtifactFailure(ctx, ArtifactFailure{
		MediaFileID: second, ArtifactKey: other, ArtifactIdentity: identity(second), RecordedBy: "node-a", Error: "boom", At: now,
	}); !errors.Is(err, ErrArtifactKindConflict) {
		t.Fatalf("RecordArtifactFailure over another kind's key = %v, want ErrArtifactKindConflict", err)
	}
}

// Old and new binaries share the table during a rolling deploy: a binary that
// predates artifact kinds keeps upserting and reading intro fingerprints with
// its own statements, and must read other statuses as cache misses.
func TestIntroFingerprintArtifactsPostgres(t *testing.T) {
	pool := openArtifactTestPool(t)
	ctx := t.Context()
	repo := NewRepository(pool)
	first, second, _ := artifactFixture(t, pool)
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
	if kind != ArtifactKindIntroFingerprint || status != ArtifactComplete {
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
	if err := repo.RecordArtifactFailure(ctx, ArtifactFailure{
		MediaFileID:      second,
		ArtifactKey:      introFingerprintKey(cfg),
		ArtifactIdentity: introFingerprintIdentity(b, cfg),
		RecordedBy:       "node-a",
		Error:            "ffmpeg exited 1",
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
