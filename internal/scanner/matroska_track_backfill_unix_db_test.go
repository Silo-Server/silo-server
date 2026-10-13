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

// Storage that stalls on every other file never trips the consecutive-timeout
// check, but its stuck reads still fill the read slots, and the pass ends
// there instead of accumulating more.
func TestMatroskaTrackBackfillStopsOnIntermittentStalls(t *testing.T) {
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

	savedSlots, savedTimeout := matroskaTracksReadSlots, matroskaTracksReadTimeout
	matroskaTracksReadSlots = make(chan struct{}, 3)
	matroskaTracksReadTimeout = 50 * time.Millisecond
	t.Cleanup(func() { matroskaTracksReadSlots, matroskaTracksReadTimeout = savedSlots, savedTimeout })

	fixture, err := os.ReadFile("testdata/subtitles.mkv")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i := range 12 {
		path := filepath.Join(dir, fmt.Sprintf("Intermittent %d (2020).mkv", i))
		if i%2 == 0 {
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
			t.Cleanup(func() {
				if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = f.Close()
				}
			})
			insertMatroskaBackfillRow(t, ctx, pool, path, 0, nil, `[{"index":2,"codec":"subrip"}]`)
			continue
		}
		if err := os.WriteFile(path, fixture, 0o600); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		insertMatroskaBackfillRow(t, ctx, pool, path, info.Size(), new(info.ModTime()),
			`[{"index":2,"codec":"subrip"},{"index":3,"codec":"ass"}]`)
	}

	backfiller := NewMatroskaTrackBackfiller(NewFileRepository(pool))
	backfiller.workers = 1
	result, err := backfiller.Run(ctx, nil)
	if !errors.Is(err, errMatroskaTrackStorageStalled) {
		t.Fatalf("err = %v (result %+v), want stalled storage", err, result)
	}
	if result.TimedOut != 3 || len(matroskaTracksReadSlots) != 3 {
		t.Fatalf("timed out = %d with %d reads stuck, want the pass to stop at the 3-slot cap", result.TimedOut, len(matroskaTracksReadSlots))
	}
}
