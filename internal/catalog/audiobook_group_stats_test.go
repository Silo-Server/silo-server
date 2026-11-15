package catalog

import (
	"context"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// A scan of a library without audiobooks refreshes nothing. A scan of an
// audiobook library refreshes the groups after the delay, even right after
// another refresh, and tries again while another server holds the refresh.
func TestAudiobookGroupStatsRefresherRefreshesAfterAudiobookScans(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var refreshes []time.Duration
		busy := []bool{true, false}
		r := newAudiobookGroupStatsRefresher(time.Hour, time.Minute,
			func(_ context.Context, skipWithin time.Duration) (bool, error) {
				refreshes = append(refreshes, skipWithin)
				b := busy[0]
				busy = busy[1:]
				return b, nil
			},
			func(_ context.Context, libraryIDs []int) (bool, error) {
				return slices.Contains(libraryIDs, 7), nil
			})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go r.Run(ctx)
		advance := func(d time.Duration) {
			time.Sleep(d) //nolint:forbidigo // Inside a synctest bubble this only advances the fake clock.
			synctest.Wait()
		}

		r.LibraryChanged(3)
		advance(2 * time.Minute)
		if len(refreshes) != 0 {
			t.Fatalf("a scan of a library without audiobooks refreshed %d times", len(refreshes))
		}

		r.LibraryChanged(7)
		advance(59 * time.Second)
		if len(refreshes) != 0 {
			t.Fatal("refreshed before the delay")
		}
		advance(2 * time.Second)
		if !slices.Equal(refreshes, []time.Duration{0}) {
			t.Fatalf("refreshes %v after the delay, want one that a recent refresh does not skip", refreshes)
		}
		advance(time.Minute)
		if !slices.Equal(refreshes, []time.Duration{0, 0}) {
			t.Fatalf("refreshes %v, want the busy one tried again", refreshes)
		}
		advance(5 * time.Minute)
		if len(refreshes) != 2 {
			t.Fatalf("refreshes %v, want none once one ran", refreshes)
		}
	})
}
