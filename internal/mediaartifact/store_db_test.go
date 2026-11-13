package mediaartifact

import (
	"context"
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

// A failure replaces an unusable row for the same identity only when its
// detail is one the failure lists; other unusable rows and complete rows
// still stand.
func TestArtifactFailureReplacesListedUnusableDetailsPostgres(t *testing.T) {
	pool := openTestPool(t)
	ctx := t.Context()
	store := NewStore(pool)
	first, _, identity := artifactFixture(t, pool)
	key := Key{Kind: "test_tail", AlgorithmVersion: 1, ConfigHash: ConfigHash("test_tail", "v1")}
	at := time.Now().UTC().Truncate(time.Microsecond)
	fail := Failure{
		MediaFileID: first, Key: key, Identity: identity(first), RecordedBy: "node-a", Error: "timeout", At: at,
		ReplacesUnusable: []string{"legacy"},
	}
	for _, tc := range []struct {
		name   string
		stored Artifact
		// replaced is whether the failure replaces the stored row.
		replaced bool
	}{
		{name: "listed unusable detail", stored: Artifact{Status: StatusUnusable, Detail: "legacy"}, replaced: true},
		{name: "unlisted unusable detail", stored: Artifact{Status: StatusUnusable, Detail: "sparse"}},
		{name: "unusable without detail", stored: Artifact{Status: StatusUnusable}},
		{name: "complete", stored: Artifact{Status: StatusComplete, PayloadFormat: "test:v1", ItemCount: 1, Payload: []byte{1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stored := tc.stored
			stored.MediaFileID, stored.Key, stored.Identity = first, key, identity(first)
			if err := store.Upsert(ctx, stored); err != nil {
				t.Fatal(err)
			}
			if err := store.RecordFailure(ctx, fail); err != nil {
				t.Fatal(err)
			}
			loaded, err := store.Load(ctx, first, key)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.replaced {
				if loaded == nil || loaded.Status != stored.Status || loaded.Detail != stored.Detail || loaded.RetryAfter != nil {
					t.Fatalf("stored row = %+v, want the %s row kept", loaded, stored.Status)
				}
				return
			}
			if loaded == nil || loaded.Status != StatusFailed || loaded.Detail != "" || loaded.FailureCount != 1 ||
				loaded.RecordedBy != "node-a" || loaded.RetryAfter == nil || !loaded.RetryAfter.Equal(at.Add(RetryDelay(1))) {
				t.Fatalf("stored row = %+v, want a first failure by node-a", loaded)
			}
			if got := loaded.State(identity(first), "node-a", at); got != Skipped {
				t.Fatalf("state = %d, want skipped while backing off", got)
			}
		})
	}
}
