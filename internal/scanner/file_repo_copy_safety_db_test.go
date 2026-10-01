package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// copySafetyTestRow inserts one media_files row with a known size and mtime and
// returns its ID. The row is torn down with the test.
func copySafetyTestRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, size int64, mtime *time.Time) int {
	t.Helper()

	suffix := time.Now().UnixNano()
	var folderID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_folders (type, name, enabled) VALUES ('movies', $1, true) RETURNING id`,
		fmt.Sprintf("PPS Test %d", suffix),
	).Scan(&folderID); err != nil {
		t.Fatalf("insert media folder: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_folders WHERE id = $1`, folderID)
	})

	var fileID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO media_files (content_id, media_folder_id, file_path, file_size, file_modified_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		fmt.Sprintf("pps-content-%d", suffix),
		folderID,
		fmt.Sprintf("/tmp/pps-%d/Movie (2020)/Movie (2020).mkv", suffix),
		size,
		mtime,
	).Scan(&fileID); err != nil {
		t.Fatalf("insert media file: %v", err)
	}
	return fileID
}

func readCopySafetyRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fileID int) (*bool, *int64, *time.Time) {
	t.Helper()
	var multi *bool
	var size *int64
	var mtime *time.Time
	if err := pool.QueryRow(ctx, `
		SELECT multiple_pps, multiple_pps_scan_size, multiple_pps_scan_mtime
		FROM media_files WHERE id = $1`, fileID).Scan(&multi, &size, &mtime); err != nil {
		t.Fatalf("read verdict: %v", err)
	}
	return multi, size, mtime
}

// UpdateMultiplePPS is the one write that has to be generation-guarded. A scan
// reads the opening seconds of a file over storage that can be slow, so a file
// rewritten in place while the scan runs would otherwise have the old verdict —
// and the old size and mtime — stamped over the replacement's row, making a
// verdict for bytes that are gone read back as valid. It also drives a
// notification, so the refusal has to be visible rather than a silent no-op.
func TestUpdateMultiplePPSGuardsAgainstAStaleGeneration(t *testing.T) {
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
	repo := NewFileRepository(pool)

	// The mtime deliberately carries nanoseconds the row cannot store: a
	// predicate that compared the raw value would reject every write made from
	// a freshly stat'ed file.
	scanned := time.Date(2026, time.March, 4, 5, 6, 7, 123456789, time.UTC)
	fileID := copySafetyTestRow(t, ctx, pool, 4096, &scanned)

	t.Run("matching generation writes", func(t *testing.T) {
		if err := repo.UpdateMultiplePPS(ctx, fileID, true, 4096, &scanned); err != nil {
			t.Fatalf("UpdateMultiplePPS() error = %v, want the write accepted despite sub-microsecond mtime drift", err)
		}
		multi, size, mtime := readCopySafetyRow(t, ctx, pool, fileID)
		if multi == nil || !*multi {
			t.Fatalf("multiple_pps = %v, want true", multi)
		}
		if size == nil || *size != 4096 {
			t.Fatalf("multiple_pps_scan_size = %v, want 4096", size)
		}
		if mtime == nil {
			t.Fatal("multiple_pps_scan_mtime = NULL, want the scanned mtime")
		}
	})

	t.Run("superseded size is refused", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE media_files SET file_size = 8192 WHERE id = $1`, fileID); err != nil {
			t.Fatalf("rewrite the row: %v", err)
		}
		err := repo.UpdateMultiplePPS(ctx, fileID, false, 4096, &scanned)
		if !errors.Is(err, ErrStaleCopySafetyScan) {
			t.Fatalf("UpdateMultiplePPS() error = %v, want ErrStaleCopySafetyScan", err)
		}
		// The earlier verdict is untouched: a refused write changes nothing.
		multi, size, _ := readCopySafetyRow(t, ctx, pool, fileID)
		if multi == nil || !*multi || size == nil || *size != 4096 {
			t.Fatalf("verdict = (%v, %v), want the refused write to have changed nothing", multi, size)
		}
	})

	t.Run("superseded mtime is refused", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE media_files SET file_size = 4096, file_modified_at = $2 WHERE id = $1`,
			fileID, scanned.Add(time.Hour)); err != nil {
			t.Fatalf("rewrite the row: %v", err)
		}
		if err := repo.UpdateMultiplePPS(ctx, fileID, false, 4096, &scanned); !errors.Is(err, ErrStaleCopySafetyScan) {
			t.Fatalf("UpdateMultiplePPS() error = %v, want ErrStaleCopySafetyScan", err)
		}
	})

	t.Run("row without an mtime", func(t *testing.T) {
		bare := copySafetyTestRow(t, ctx, pool, 2048, nil)
		if err := repo.UpdateMultiplePPS(ctx, bare, true, 2048, nil); err != nil {
			t.Fatalf("UpdateMultiplePPS() error = %v, want a mtime-less row to accept a mtime-less verdict", err)
		}
		// The same row will not take a verdict claiming an mtime it does not
		// have: that pairing is a different generation, not a match.
		if err := repo.UpdateMultiplePPS(ctx, bare, true, 2048, &scanned); !errors.Is(err, ErrStaleCopySafetyScan) {
			t.Fatalf("UpdateMultiplePPS() error = %v, want ErrStaleCopySafetyScan", err)
		}
	})

	t.Run("missing row", func(t *testing.T) {
		if err := repo.UpdateMultiplePPS(ctx, -1, true, 4096, &scanned); !errors.Is(err, ErrStaleCopySafetyScan) {
			t.Fatalf("UpdateMultiplePPS() error = %v, want ErrStaleCopySafetyScan for a row that is gone", err)
		}
	})
}
