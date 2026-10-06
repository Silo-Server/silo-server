package mediaartifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database"
	"github.com/Silo-Server/silo-server/migrations"
)

func openTestPool(t *testing.T) *pgxpool.Pool {
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

// artifactFixture seeds two media files and returns their IDs with the
// identity each one's artifacts are computed from.
func artifactFixture(t *testing.T, pool *pgxpool.Pool) (int, int, func(int) Identity) {
	t.Helper()
	ctx := t.Context()
	name := fmt.Sprintf("mediaartifact-%d", time.Now().UnixNano())
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', $1) RETURNING id`, name).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, stmt := range []string{
			`DELETE FROM media_files WHERE media_folder_id = $1`,
			`DELETE FROM media_folders WHERE id = $1`,
		} {
			if _, err := pool.Exec(cleanup, stmt, folderID); err != nil {
				t.Errorf("clean artifact fixture: %v", err)
			}
		}
	})
	var ids [2]int
	for i := range ids {
		if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
			folderID, fmt.Sprintf("/%s/e%d.mkv", name, i+1)).Scan(&ids[i]); err != nil {
			t.Fatalf("seed media file: %v", err)
		}
	}
	identity := func(fileID int) Identity {
		return Identity{FileHash: fmt.Sprintf("%s-hash-%d", name, fileID), FileSize: 1000000, DurationSeconds: 1500, WindowStartSeconds: 1200, WindowEndSeconds: 1500}
	}
	return ids[0], ids[1], identity
}

func TestArtifactStatusesPostgres(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	store := NewStore(pool)
	first, second, identity := artifactFixture(t, pool)
	key := Key{Kind: "test_tail", AlgorithmVersion: 1, ConfigHash: ConfigHash("test_tail", "v1")}
	now := time.Now().UTC().Truncate(time.Microsecond)

	complete := Artifact{
		MediaFileID:           first,
		Key:                   key,
		Identity:              identity(first),
		Status:                StatusComplete,
		PayloadFormat:         "test:v1",
		SampleDurationSeconds: 300,
		ItemCount:             2,
		Payload:               []byte{1, 2, 3, 4},
		RecordedBy:            "node-a",
	}
	if err := store.Upsert(ctx, complete); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, first, key)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Status != StatusComplete || string(loaded.Payload) != string(complete.Payload) ||
		loaded.ItemCount != 2 || loaded.PayloadFormat != "test:v1" || loaded.SampleDurationSeconds != 300 || loaded.RecordedBy != "node-a" {
		t.Fatalf("loaded complete artifact = %+v", loaded)
	}
	if got := loaded.State(identity(first), "node-b", now); got != Ready {
		t.Fatalf("complete artifact state = %d, want ready", got)
	}
	replaced := identity(first)
	replaced.FileHash = "replaced"
	if got := loaded.State(replaced, "node-a", now); got != Missing {
		t.Fatalf("complete artifact for a replaced file = %d, want missing", got)
	}
	for _, other := range []Key{
		{Kind: "other_kind", AlgorithmVersion: key.AlgorithmVersion, ConfigHash: key.ConfigHash},
		{Kind: key.Kind, AlgorithmVersion: 2, ConfigHash: key.ConfigHash},
		{Kind: key.Kind, AlgorithmVersion: key.AlgorithmVersion, ConfigHash: ConfigHash("test_tail", "v2")},
	} {
		if got, err := store.Load(ctx, first, other); err != nil || got != nil {
			t.Fatalf("Load(%+v) = %+v, %v; want no row", other, got, err)
		}
	}

	// A failure never replaces a settled result for the same file.
	if err := store.RecordFailure(ctx, Failure{
		MediaFileID: first, Key: key, Identity: identity(first), RecordedBy: "node-a", Error: "late", At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if loaded, err = store.Load(ctx, first, key); err != nil || loaded.Status != StatusComplete {
		t.Fatalf("complete artifact after a failure = %+v, %v; want it kept", loaded, err)
	}

	unusable := Artifact{
		MediaFileID: second,
		Key:         key,
		Identity:    identity(second),
		Status:      StatusUnusable,
		Detail:      "no_video",
		RecordedBy:  "node-a",
	}
	withPayload := unusable
	withPayload.Payload = []byte{1}
	if err := store.Upsert(ctx, withPayload); err == nil {
		t.Fatal("an unusable artifact with a payload must be rejected")
	}
	if err := store.Upsert(ctx, unusable); err != nil {
		t.Fatal(err)
	}
	all, err := store.LoadMany(ctx, []int{first, second, 0}, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[first].Status != StatusComplete || all[second].Status != StatusUnusable || all[second].Detail != "no_video" {
		t.Fatalf("LoadMany() = %+v, want the complete and the unusable row", all)
	}
	unusableRow := all[second]
	if got := unusableRow.State(identity(second), "node-b", now); got != Skipped {
		t.Fatalf("unusable artifact state = %d, want skipped", got)
	}
	resized := identity(second)
	resized.FileSize++
	if got := unusableRow.State(resized, "node-b", now); got != Missing {
		t.Fatalf("unusable artifact for a changed file = %d, want missing", got)
	}
	if err := store.Upsert(ctx, Artifact{MediaFileID: second, Key: key, Identity: identity(second), Status: StatusFailed}); err == nil {
		t.Fatal("Upsert must not write failures")
	}

	// Failures back off on the recording server only, and escalate on retry.
	failKey := Key{Kind: key.Kind, AlgorithmVersion: key.AlgorithmVersion, ConfigHash: ConfigHash("test_tail", "failing")}
	fail := Failure{MediaFileID: second, Key: failKey, Identity: identity(second), RecordedBy: "node-a", Error: "ffmpeg exited 1", At: now}
	anonymous := fail
	anonymous.RecordedBy = ""
	if err := store.RecordFailure(ctx, anonymous); err == nil {
		t.Fatal("RecordFailure must require the recording server")
	}
	if err := store.RecordFailure(ctx, fail); err != nil {
		t.Fatal(err)
	}
	failed, err := store.Load(ctx, second, failKey)
	if err != nil {
		t.Fatal(err)
	}
	if failed == nil || failed.Status != StatusFailed || failed.FailureCount != 1 || failed.LastError != "ffmpeg exited 1" ||
		failed.RetryAfter == nil || !failed.RetryAfter.Equal(now.Add(12*time.Hour)) || len(failed.Payload) != 0 || failed.RecordedBy != "node-a" {
		t.Fatalf("failed artifact = %+v", failed)
	}
	if got := failed.State(identity(second), "node-a", now.Add(time.Hour)); got != Skipped {
		t.Fatalf("failed artifact on its server = %d, want skipped", got)
	}
	if got := failed.State(identity(second), "node-b", now.Add(time.Hour)); got != Missing {
		t.Fatalf("failed artifact on another server = %d, want missing", got)
	}
	if got := failed.State(identity(second), "node-a", now.Add(13*time.Hour)); got != Missing {
		t.Fatalf("failed artifact after its backoff = %d, want missing", got)
	}
	fail.At = now.Add(13 * time.Hour)
	if err := store.RecordFailure(ctx, fail); err != nil {
		t.Fatal(err)
	}
	if failed, err = store.Load(ctx, second, failKey); err != nil || failed.FailureCount != 2 || !failed.RetryAfter.Equal(fail.At.Add(24*time.Hour)) {
		t.Fatalf("second failure = %+v, %v; want count 2 retrying after a day", failed, err)
	}

	// A later success clears the failure.
	succeeded := complete
	succeeded.MediaFileID = second
	succeeded.Key = failKey
	succeeded.Identity = identity(second)
	if err := store.Upsert(ctx, succeeded); err != nil {
		t.Fatal(err)
	}
	if failed, err = store.Load(ctx, second, failKey); err != nil || failed.Status != StatusComplete ||
		failed.FailureCount != 0 || failed.LastError != "" || failed.RetryAfter != nil {
		t.Fatalf("artifact after success = %+v, %v; want complete with no failure", failed, err)
	}

	// A key derived by two kinds must not let one overwrite the other.
	collision := complete
	collision.Kind = "other_kind"
	if err := store.Upsert(ctx, collision); !errors.Is(err, ErrKindConflict) {
		t.Fatalf("Upsert over another kind's key = %v, want ErrKindConflict", err)
	}
	if loaded, err = store.Load(ctx, first, key); err != nil || loaded == nil || loaded.Kind != key.Kind {
		t.Fatalf("original artifact after a kind collision = %+v, %v", loaded, err)
	}
}

// Two servers can analyze a new file at once. A failure recorded while the
// other server's first successful write is still in flight must not replace it.
func TestArtifactFailureKeepsConcurrentFirstResultPostgres(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	store := NewStore(pool)
	first, second, identity := artifactFixture(t, pool)
	key := Key{Kind: "test_tail", AlgorithmVersion: 1, ConfigHash: ConfigHash("test_tail", "race")}
	now := time.Now().UTC().Truncate(time.Microsecond)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := execArtifactUpsert(ctx, tx, Artifact{
		MediaFileID: first, Key: key, Identity: identity(first), Status: StatusComplete,
		PayloadFormat: "test:v1", ItemCount: 1, Payload: []byte{1}, RecordedBy: "node-b",
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.RecordFailure(ctx, Failure{
			MediaFileID: first, Key: key, Identity: identity(first), RecordedBy: "node-a", Error: "ffmpeg exited 1", At: now,
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
			t.Fatalf("RecordFailure returned %v before the first result committed", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("RecordFailure never waited on the in-flight first result")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, first, key)
	if err != nil || loaded == nil || loaded.Status != StatusComplete || loaded.RecordedBy != "node-b" {
		t.Fatalf("artifact after a concurrent failure = %+v, %v; want the complete result kept", loaded, err)
	}

	// A failure over another kind's key reports the collision.
	if err := store.Upsert(ctx, Artifact{
		MediaFileID: second, Key: key, Identity: identity(second), Status: StatusComplete,
		PayloadFormat: "test:v1", ItemCount: 1, Payload: []byte{1}, RecordedBy: "node-b",
	}); err != nil {
		t.Fatal(err)
	}
	other := key
	other.Kind = "other_kind"
	if err := store.RecordFailure(ctx, Failure{
		MediaFileID: second, Key: other, Identity: identity(second), RecordedBy: "node-a", Error: "boom", At: now,
	}); !errors.Is(err, ErrKindConflict) {
		t.Fatalf("RecordFailure over another kind's key = %v, want ErrKindConflict", err)
	}
}
