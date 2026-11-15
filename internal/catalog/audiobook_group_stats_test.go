package catalog

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

// A scan of a library without audiobooks refreshes nothing. A scan of an
// audiobook library refreshes the groups after the delay unless a refresh
// started after the scan, and tries again while another server holds the
// refresh.
func TestAudiobookGroupStatsRefresherRefreshesAfterAudiobookScans(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var refreshes []time.Duration
		busy := []bool{true, false}
		r := newAudiobookGroupStatsRefresher(time.Hour, time.Hour, time.Minute,
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
		if !slices.Equal(refreshes, []time.Duration{time.Minute}) {
			t.Fatalf("refreshes %v after the delay, want one that skips only a refresh started since the scan", refreshes)
		}
		advance(time.Minute)
		if !slices.Equal(refreshes, []time.Duration{time.Minute, 2 * time.Minute}) {
			t.Fatalf("refreshes %v, want the busy one tried again", refreshes)
		}
		advance(5 * time.Minute)
		if len(refreshes) != 2 {
			t.Fatalf("refreshes %v, want none once one ran", refreshes)
		}
	})
}

// Each server checks every poll whether any server refreshed within the
// interval, so the view is never older than the interval plus one poll. After
// a failed refresh both the check and a scan's refresh wait an interval.
func TestAudiobookGroupStatsRefresherChecksTheRefreshAgeEachPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var refreshes []time.Duration
		fail := false
		r := newAudiobookGroupStatsRefresher(10*time.Minute, time.Minute, time.Minute,
			func(_ context.Context, skipWithin time.Duration) (bool, error) {
				refreshes = append(refreshes, skipWithin)
				if fail {
					return false, errors.New("refresh failed")
				}
				return false, nil
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

		advance(2*time.Minute + time.Second)
		if !slices.Equal(refreshes, []time.Duration{10 * time.Minute, 10 * time.Minute}) {
			t.Fatalf("refreshes %v after two polls, want two that skip a refresh within the interval", refreshes)
		}

		fail = true
		advance(time.Minute)
		r.LibraryChanged(7)
		advance(9 * time.Minute)
		if len(refreshes) != 3 {
			t.Fatalf("%d refreshes, want none, checked or scanned, for an interval after the failed third", len(refreshes))
		}
		fail = false
		advance(time.Minute)
		// The scan arrived 9m59s before the pause ended, so its refresh
		// skips the check's if that one ran first.
		if len(refreshes) != 5 || !slices.Contains(refreshes[3:], 9*time.Minute+59*time.Second) {
			t.Fatalf("refreshes %v, want the check and the scan's refresh once the pause ends", refreshes)
		}
	})
}
