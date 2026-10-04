//go:build unix

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// When every read times out the storage is stalled: the pass stops after a
// few timeouts in a row instead of waiting out every remaining file. FIFOs
// with no writer block on open the way files on a hung mount do.
func TestMatroskaTrackBackfillStopsOnStalledStorage(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	saved := matroskaTracksReadTimeout
	matroskaTracksReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { matroskaTracksReadTimeout = saved })

	dir := t.TempDir()
	const files = matroskaTrackBackfillMaxTimeouts + 2
	for i := range files {
		path := filepath.Join(dir, fmt.Sprintf("Stalled %d (2020).mkv", i))
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		// Unblock the abandoned opens when the test ends.
		t.Cleanup(func() {
			if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = f.Close()
			}
		})
		insertMatroskaBackfillRow(t, ctx, pool, path, 0, nil, `[{"index":2,"codec":"subrip"}]`)
	}

	backfiller := NewMatroskaTrackBackfiller(NewFileRepository(pool))
	result, err := backfiller.Run(ctx, nil)
	if !errors.Is(err, errMatroskaTrackStorageStalled) {
		t.Fatalf("err = %v, want stalled storage", err)
	}
	if result.TimedOut < matroskaTrackBackfillMaxTimeouts || result.TimedOut > files {
		t.Fatalf("timed out = %d, want the pass to stop after %d", result.TimedOut, matroskaTrackBackfillMaxTimeouts)
	}
}
