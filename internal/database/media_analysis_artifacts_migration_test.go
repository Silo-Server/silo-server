package database

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/migrations"
)

var mediaAnalysisArtifactColumns = []string{"kind", "status", "detail", "failure_count", "last_error", "retry_after", "recorded_by"}

// TestGeneralizeMediaAnalysisArtifactsMigration seeds a fingerprint in the
// pre-migration shape, migrates up and back down, and checks that existing
// rows become complete intro fingerprints under the unchanged primary key, and
// that the rollback keeps only rows the previous schema can represent.
func TestGeneralizeMediaAnalysisArtifactsMigration(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	beforeMediaAnalysisArtifacts := versionBefore(t, "_generalize_media_analysis_artifacts.sql")
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("up: %v", err)
	}
	// Leave the database on the current schema whatever happens below.
	t.Cleanup(func() {
		if err := RunMigrations(context.Background(), pool, migrations.FS, "sql"); err != nil {
			t.Errorf("re-up: %v", err)
		}
	})
	if err := MigrateDownTo(ctx, pool, migrations.FS, "sql", beforeMediaAnalysisArtifacts); err != nil {
		t.Fatalf("down before the migration: %v", err)
	}
	assertPreArtifactShape(ctx, t, pool, "before")

	fileID := seedIntroFingerprintFile(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
INSERT INTO media_intro_fingerprints (
    media_file_id, file_hash, file_size, duration_seconds, window_end_seconds,
    algorithm_version, config_hash, fingerprint_format, sample_duration_seconds, point_count, points
) VALUES ($1, 'hash', 1000, 1500, 375, 1, '475b8bded0ab1398', 'chromaprint:raw:uint32le', 0.246, 2, '\x0100000002000000')`,
		fileID); err != nil {
		t.Fatalf("seed pre-migration fingerprint: %v", err)
	}

	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("up: %v", err)
	}
	var kind, status, recordedBy string
	var failureCount int
	var detail, lastError *string
	var retryAfter *time.Time
	if err := pool.QueryRow(ctx, `
SELECT kind, status, detail, failure_count, last_error, retry_after, recorded_by
  FROM media_intro_fingerprints
 WHERE media_file_id = $1 AND algorithm_version = 1 AND config_hash = '475b8bded0ab1398'`, fileID).Scan(
		&kind, &status, &detail, &failureCount, &lastError, &retryAfter, &recordedBy); err != nil {
		t.Fatalf("read migrated fingerprint: %v", err)
	}
	if kind != "intro_fingerprint" || status != "complete" || detail != nil || failureCount != 0 ||
		lastError != nil || retryAfter != nil || recordedBy != "" {
		t.Fatalf("migrated fingerprint = kind %q status %q detail %v failures %d error %v retry %v by %q; want a complete intro fingerprint",
			kind, status, detail, failureCount, lastError, retryAfter, recordedBy)
	}
	if got := primaryKeyColumns(ctx, t, pool); !slices.Equal(got, []string{"media_file_id", "algorithm_version", "config_hash"}) {
		t.Fatalf("primary key = %v; older binaries upsert on (media_file_id, algorithm_version, config_hash)", got)
	}
	if hashIndexExists(ctx, t, pool) {
		t.Error("up: idx_media_intro_fingerprints_hash still exists")
	}
	var comment *string
	if err := pool.QueryRow(ctx, `SELECT obj_description('media_intro_fingerprints'::regclass, 'pg_class')`).Scan(&comment); err != nil {
		t.Fatal(err)
	}
	if comment == nil || *comment == "" {
		t.Error("up: media_intro_fingerprints has no table comment")
	}
	if _, err := pool.Exec(ctx, `UPDATE media_intro_fingerprints SET status = 'pending' WHERE media_file_id = $1`, fileID); err == nil {
		t.Error("up: status accepts values other than complete, unusable, and failed")
	}

	// Rows the previous schema cannot represent.
	if _, err := pool.Exec(ctx, `
INSERT INTO media_intro_fingerprints (
    media_file_id, kind, status, file_hash, file_size, duration_seconds, window_start_seconds, window_end_seconds,
    algorithm_version, config_hash, fingerprint_format, sample_duration_seconds, point_count, points
) VALUES
    ($1, 'credits_tail', 'complete', 'hash', 1000, 1500, 1200, 1500, 1, 'credits-tail', 'tail:v1', 0, 1, '\x01'),
    ($1, 'intro_fingerprint', 'failed', 'hash', 1000, 1500, 0, 375, 1, 'failed-intro', '', 0, 0, '\x'),
    ($1, 'intro_fingerprint', 'unusable', 'hash', 1000, 1500, 0, 375, 1, 'unusable-intro', '', 0, 0, '\x')`,
		fileID); err != nil {
		t.Fatalf("seed new artifact rows: %v", err)
	}

	if err := MigrateDownTo(ctx, pool, migrations.FS, "sql", beforeMediaAnalysisArtifacts); err != nil {
		t.Fatalf("down: %v", err)
	}
	assertPreArtifactShape(ctx, t, pool, "down")
	rows, err := pool.Query(ctx, `SELECT config_hash FROM media_intro_fingerprints WHERE media_file_id = $1 ORDER BY config_hash`, fileID)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatal(err)
		}
		kept = append(kept, hash)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(kept, []string{"475b8bded0ab1398"}) {
		t.Fatalf("rows kept by the rollback = %v; want only the complete intro fingerprint", kept)
	}
}

// versionBefore returns the version of the SQL migration that sorts just
// before the one whose file name ends in suffix.
func versionBefore(t *testing.T, suffix string) int64 {
	t.Helper()
	entries, err := fs.ReadDir(migrations.FS, "sql")
	if err != nil {
		t.Fatal(err)
	}
	var versions []int64
	target := int64(-1)
	for _, entry := range entries {
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			continue
		}
		if strings.HasSuffix(entry.Name(), suffix) {
			target = version
		}
		versions = append(versions, version)
	}
	if target < 0 {
		t.Fatalf("no migration ends in %s", suffix)
	}
	previous := int64(-1)
	for _, version := range versions {
		if version < target && version > previous {
			previous = version
		}
	}
	if previous < 0 {
		t.Fatalf("no migration sorts before %d", target)
	}
	return previous
}

func assertPreArtifactShape(ctx context.Context, t *testing.T, pool *pgxpool.Pool, stage string) {
	t.Helper()
	columns := simplifyColumns(ctx, t, pool, "media_intro_fingerprints")
	for _, column := range mediaAnalysisArtifactColumns {
		if columns[column] {
			t.Errorf("%s: column %s exists", stage, column)
		}
	}
	if !hashIndexExists(ctx, t, pool) {
		t.Errorf("%s: idx_media_intro_fingerprints_hash is missing", stage)
	}
}

func hashIndexExists(ctx context.Context, t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_media_intro_fingerprints_hash')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func primaryKeyColumns(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `
SELECT a.attname
  FROM pg_index i
  JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
  JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
 WHERE i.indrelid = 'media_intro_fingerprints'::regclass
   AND i.indisprimary
 ORDER BY k.ord`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return columns
}

func seedIntroFingerprintFile(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	name := fmt.Sprintf("media-analysis-artifacts-%d", time.Now().UnixNano())
	var folderID, fileID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('series', $1) RETURNING id`, name).Scan(&folderID); err != nil {
		t.Fatalf("seed media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_files WHERE media_folder_id = $1`, folderID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})
	if err := pool.QueryRow(ctx, `INSERT INTO media_files (media_folder_id, file_path) VALUES ($1, $2) RETURNING id`,
		folderID, "/"+name+"/episode.mkv").Scan(&fileID); err != nil {
		t.Fatalf("seed media file: %v", err)
	}
	return fileID
}
