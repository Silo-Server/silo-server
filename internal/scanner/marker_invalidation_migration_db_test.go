package scanner

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The invalidation trigger decides what a file identity change clears. Rows
// reach media_files through two writers that store markers differently, so both
// shapes are covered here:
//
//   - a row with a known derived source (scanner/online/plugin/s3) holds data
//     that belongs to the previous file identity and must be fetched or
//     detected again;
//   - a row with no provenance at all (catalog import) holds ranges nothing can
//     re-derive, so an identity change must leave them alone.
//
// The scanner's first pass after an import is what makes the second case
// reachable: the import does not write file_modified_at, so the first rescan
// stamps it, and that stamping alone is an identity change.
func TestMarkerInvalidationPreservesProvenanceFreeRangesPostgres(t *testing.T) {
	ctx := t.Context()
	pool := markerInvalidationPool(t)
	folderID := markerInvalidationFolder(t, pool)

	t.Run("imported ranges without a source survive", func(t *testing.T) {
		fileID := markerInvalidationFile(t, pool, folderID, "imported", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, credits_start, credits_end, marker_segments)
			VALUES ($1, $2, 2000, 'imported-cut', 2000, 15, 65, 1800, 1950,
				'[{"kind":"intro","start_seconds":15,"end_seconds":65}]'::jsonb)
			RETURNING id`)

		// The scanner's first pass writes the columns the import left NULL.
		markerInvalidationRescan(t, pool, fileID)

		var introStart, introEnd, creditsStart, creditsEnd *float64
		var segments int
		if err := pool.QueryRow(ctx, `SELECT intro_start, intro_end, credits_start, credits_end,
			jsonb_array_length(marker_segments) FROM media_files WHERE id = $1`, fileID).Scan(
			&introStart, &introEnd, &creditsStart, &creditsEnd, &segments); err != nil {
			t.Fatal(err)
		}
		if introStart == nil || introEnd == nil || *introStart != 15 || *introEnd != 65 {
			t.Errorf("imported intro range was cleared: %v-%v, want 15-65", introStart, introEnd)
		}
		if creditsStart == nil || creditsEnd == nil || *creditsStart != 1800 || *creditsEnd != 1950 {
			t.Errorf("imported credits range was cleared: %v-%v, want 1800-1950", creditsStart, creditsEnd)
		}
		if segments != 1 {
			t.Errorf("source-less occurrence count = %d, want 1", segments)
		}
	})

	t.Run("derived ranges are cleared and manual ones kept", func(t *testing.T) {
		fileID := markerInvalidationFile(t, pool, folderID, "derived", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, credits_start, credits_end, markers_source, marker_segments,
				intro_markers_source, intro_markers_confidence, credits_markers_source)
			VALUES ($1, $2, 2000, 'derived-cut', 2000, 10, 50, 1800, 1950, 'online',
				'[{"kind":"intro","start_seconds":10,"end_seconds":50},
				  {"kind":"credits","start_seconds":1800,"end_seconds":1950}]'::jsonb,
				'online', 0.9, 'manual')
			RETURNING id`)

		markerInvalidationRescan(t, pool, fileID)

		var introStart, introSource *string
		var creditsStart *float64
		var segments int
		if err := pool.QueryRow(ctx, `SELECT intro_start::text, intro_markers_source, credits_start,
			jsonb_array_length(marker_segments) FROM media_files WHERE id = $1`, fileID).Scan(
			&introStart, &introSource, &creditsStart, &segments); err != nil {
			t.Fatal(err)
		}
		if introStart != nil || introSource != nil {
			t.Errorf("online intro survived an identity change: start=%v source=%v", introStart, introSource)
		}
		if creditsStart == nil || *creditsStart != 1800 {
			t.Errorf("manual credits range = %v, want 1800", creditsStart)
		}
		if segments != 1 {
			t.Errorf("occurrence count = %d, want 1 (manual credits only)", segments)
		}
	})

	t.Run("shared confidence follows the surviving ranges", func(t *testing.T) {
		// The surviving range here is a manual credits marker with no confidence,
		// while the shared column still carries the dropped online intro's
		// confidence. Folding the previous shared value back into GREATEST left
		// the row advertising a confidence no surviving range reports.
		staleID := markerInvalidationFile(t, pool, folderID, "stale-confidence", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, credits_start, credits_end, markers_source, markers_confidence,
				intro_markers_source, intro_markers_confidence, credits_markers_source)
			VALUES ($1, $2, 2000, 'stale-cut', 2000, 10, 50, 1800, 1950, 'online', 0.95,
				'online', 0.95, 'manual')
			RETURNING id`)
		markerInvalidationRescan(t, pool, staleID)

		var staleSource, staleConfidence *string
		if err := pool.QueryRow(ctx, `SELECT markers_source, markers_confidence::text
			FROM media_files WHERE id = $1`, staleID).Scan(&staleSource, &staleConfidence); err != nil {
			t.Fatal(err)
		}
		if staleSource == nil || *staleSource != "manual" {
			t.Errorf("markers_source = %v, want manual", staleSource)
		}
		if staleConfidence != nil {
			t.Errorf("markers_confidence = %v, want NULL: no surviving range reports one", *staleConfidence)
		}

		// A surviving range that does report a confidence still publishes it, so
		// the assertion above cannot pass by always clearing the column.
		reportedID := markerInvalidationFile(t, pool, folderID, "reported-confidence", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, credits_start, credits_end, markers_source, markers_confidence,
				intro_markers_source, intro_markers_confidence, credits_markers_source, credits_markers_confidence)
			VALUES ($1, $2, 2000, 'reported-cut', 2000, 10, 50, 1800, 1950, 'online', 0.95,
				'online', 0.95, 'manual', 0.6)
			RETURNING id`)
		markerInvalidationRescan(t, pool, reportedID)

		var reportedConfidence *string
		if err := pool.QueryRow(ctx, `SELECT markers_confidence::text FROM media_files WHERE id = $1`, reportedID).Scan(&reportedConfidence); err != nil {
			t.Fatal(err)
		}
		if reportedConfidence == nil || *reportedConfidence != "0.6" {
			t.Errorf("markers_confidence = %v, want 0.6 from the surviving manual range", reportedConfidence)
		}
	})

	t.Run("a kind without its own source survives a mixed-provenance row", func(t *testing.T) {
		// Catalog import wrote the intro bounds; a provider later marked the
		// credits, which also sets the shared markers_source. The intro has no
		// provenance of its own, so the shared value must not be read as its
		// source, or the import is deleted the moment the file identity changes.
		fileID := markerInvalidationFile(t, pool, folderID, "mixed", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, credits_start, credits_end, markers_source, marker_segments,
				credits_markers_source)
			VALUES ($1, $2, 2000, 'mixed-cut', 2000, 15, 65, 1800, 1950, 'online',
				'[{"kind":"intro","start_seconds":15,"end_seconds":65},
				  {"kind":"credits","start_seconds":1800,"end_seconds":1950}]'::jsonb,
				'online')
			RETURNING id`)
		markerInvalidationRescan(t, pool, fileID)

		var introStart, creditsStart *float64
		var segments int
		if err := pool.QueryRow(ctx, `SELECT intro_start, credits_start,
			jsonb_array_length(marker_segments) FROM media_files WHERE id = $1`, fileID).Scan(
			&introStart, &creditsStart, &segments); err != nil {
			t.Fatal(err)
		}
		if introStart == nil || *introStart != 15 {
			t.Errorf("imported intro = %v, want 15 to survive a mixed-provenance rescan", introStart)
		}
		if creditsStart != nil {
			t.Errorf("online credits = %v, want cleared on an identity change", creditsStart)
		}
		if segments != 1 {
			t.Errorf("occurrence count = %d, want 1 (source-less intro only)", segments)
		}
	})

	t.Run("a row with only the shared source still clears", func(t *testing.T) {
		// Rows written before the per-kind columns existed carry one source for
		// every kind, so the shared value is theirs and their ranges are derived.
		fileID := markerInvalidationFile(t, pool, folderID, "legacy-shape", `
			INSERT INTO media_files (media_folder_id, file_path, duration, file_hash, file_size,
				intro_start, intro_end, markers_source)
			VALUES ($1, $2, 2000, 'legacy-shape-cut', 2000, 15, 65, 'online')
			RETURNING id`)
		markerInvalidationRescan(t, pool, fileID)

		var introStart *float64
		if err := pool.QueryRow(ctx, `SELECT intro_start FROM media_files WHERE id = $1`, fileID).Scan(&introStart); err != nil {
			t.Fatal(err)
		}
		if introStart != nil {
			t.Errorf("shared-source intro = %v, want cleared on an identity change", introStart)
		}
	})
}

func markerInvalidationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	var trigger string
	if err := pool.QueryRow(ctx, `SELECT tgname FROM pg_trigger WHERE tgname = 'media_files_invalidate_markers'`).Scan(&trigger); err != nil {
		t.Skipf("marker invalidation trigger is not installed in this database: %v", err)
	}
	return pool
}

func markerInvalidationFolder(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := t.Context()
	var folderID int
	if err := pool.QueryRow(ctx, `INSERT INTO media_folders (type, name) VALUES ('movies', 'Marker invalidation test') RETURNING id`).Scan(&folderID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		if _, err := pool.Exec(cleanup, `DELETE FROM media_files WHERE media_folder_id = $1`, folderID); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM media_folders WHERE id = $1`, folderID); err != nil {
			t.Error(err)
		}
	})
	return folderID
}

func markerInvalidationFile(t *testing.T, pool *pgxpool.Pool, folderID int, name, statement string) int {
	t.Helper()
	var fileID int
	path := fmt.Sprintf("/marker-invalidation-%s-%d.mkv", name, time.Now().UnixNano())
	if err := pool.QueryRow(t.Context(), statement, folderID, path).Scan(&fileID); err != nil {
		t.Fatalf("insert %s fixture: %v", name, err)
	}
	return fileID
}

// markerInvalidationRescan reproduces the scanner's upsert of the identity
// columns, which is what fires the trigger.
func markerInvalidationRescan(t *testing.T, pool *pgxpool.Pool, fileID int) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), `UPDATE media_files
		SET file_hash = 'rescanned-cut', file_size = 2001, file_modified_at = now()
		WHERE id = $1`, fileID); err != nil {
		t.Fatalf("rescan update: %v", err)
	}
}
