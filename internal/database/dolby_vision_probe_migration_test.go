package database

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/Silo-Server/silo-server/migrations"
)

// TestInvalidateLegacyDolbyVisionProbeMigration verifies that the one-time
// repair discards only probe timestamps whose Dolby Vision facts cannot be
// trusted. A missing provenance field is materially different from false:
// pre-existing JSONB tracks omit both keys entirely.
func TestInvalidateLegacyDolbyVisionProbeMigration(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_invalidate_legacy_dolby_vision_probes.sql")
	if err != nil {
		t.Fatalf("find migration: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("migration matches = %v; want exactly one", matches)
	}
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

	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	libraryID := seedDolbyVisionProbeRows(ctx, t, pool)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM media_folders WHERE id = $1", libraryID)
	})

	applyMigrationForTest(ctx, t, pool, matches[0])

	wantInvalidated := map[string]bool{
		"profile":          true,
		"dovi-range":       true,
		"dolby-vision":     true,
		"complete-dovi":    false,
		"ordinary-hdr":     false,
		"sql-null":         false,
		"json-null":        false,
		"json-object":      false,
		"json-scalar":      false,
		"already-unprobed": true,
	}
	rows, err := pool.Query(ctx, `
SELECT file_path, probe_updated_at IS NULL
  FROM media_files
 WHERE media_folder_id = $1`, libraryID)
	if err != nil {
		t.Fatalf("read probe timestamps: %v", err)
	}
	defer rows.Close()
	got := make(map[string]bool, len(wantInvalidated))
	for rows.Next() {
		var path string
		var timestampIsNull bool
		if err := rows.Scan(&path, &timestampIsNull); err != nil {
			t.Fatalf("scan probe timestamp: %v", err)
		}
		got[filepath.Base(path)] = timestampIsNull
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate probe timestamps: %v", err)
	}
	if len(got) != len(wantInvalidated) {
		t.Fatalf("seeded rows = %v; want %d rows", got, len(wantInvalidated))
	}
	for name, want := range wantInvalidated {
		if got[name] != want {
			t.Errorf("%s probe timestamp null = %t, want %t", name, got[name], want)
		}
	}
}

func seedDolbyVisionProbeRows(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	name := fmt.Sprintf("task4-dolby-vision-probes-%d", time.Now().UnixNano())
	var libraryID int
	if err := pool.QueryRow(ctx, `
INSERT INTO media_folders (type, name)
VALUES ('movies', $1)
RETURNING id`, name).Scan(&libraryID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}

	tracks := map[string]*string{
		"profile":          stringPtr(`[{"dv_profile":7,"dv_bl_compat_id_present":true}]`),
		"dovi-range":       stringPtr(`[{"video_range_type":"DOVI","dv_config_present":true}]`),
		"dolby-vision":     stringPtr(`[{"dolby_vision":"Dolby Vision","dv_bl_compat_id_present":true}]`),
		"complete-dovi":    stringPtr(`[{"dv_profile":7,"dv_config_present":true,"dv_bl_compat_id_present":true}]`),
		"ordinary-hdr":     stringPtr(`[{"video_range_type":"HDR10"}]`),
		"sql-null":         nil,
		"json-null":        stringPtr(`null`),
		"json-object":      stringPtr(`{"dv_profile":7}`),
		"json-scalar":      stringPtr(`7`),
		"already-unprobed": stringPtr(`[{"dv_profile":7}]`),
	}
	for name, videoTracks := range tracks {
		var probeUpdatedAt *time.Time
		if name != "already-unprobed" {
			updatedAt := time.Date(2026, time.August, 14, 12, 0, 0, 0, time.UTC)
			probeUpdatedAt = &updatedAt
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO media_files (media_folder_id, file_path, video_tracks, probe_updated_at)
VALUES ($1, $2, $3::jsonb, $4)`, libraryID, name, videoTracks, probeUpdatedAt); err != nil {
			t.Fatalf("seed %s media file: %v", name, err)
		}
	}
	return libraryID
}

func stringPtr(value string) *string {
	return &value
}

// applyMigrationForTest executes the generated SQL through Goose but keeps
// its applied-version history isolated from the shared test database. The
// production version table must never be removed just to make a one-time
// migration run again in a test.
func applyMigrationForTest(ctx context.Context, t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	versionTable := fmt.Sprintf("public.goose_dolby_vision_probe_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE IF EXISTS "+versionTable)
	})

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(schemaMigrationsLockID))
	if err != nil {
		t.Fatalf("create migration lock: %v", err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		sqlDB,
		fstest.MapFS{filepath.Base(path): &fstest.MapFile{Data: body}},
		goose.WithTableName(versionTable),
		goose.WithSessionLocker(&legacyBootstrapLocker{delegate: locker}),
	)
	if err != nil {
		t.Fatalf("create migration provider: %v", err)
	}
	defer func() { _ = provider.Close() }()
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("apply Dolby Vision probe migration: %v", err)
	}
}
