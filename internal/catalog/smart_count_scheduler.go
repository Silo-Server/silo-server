package catalog

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SmartCountScheduler refreshes the stored counts of the smart collections
// that draw from a library soon after a scan changes that library's titles.
// Unrestricted viewers read the stored count, so without it they would see
// the old number until the hourly refresh.
//
// The node that ran the scan calls LibraryChanged, so each scan is counted
// once however many API nodes there are, and the result lands in Postgres
// where every node reads it.
type SmartCountScheduler struct {
	refresh func(ctx context.Context, libraryIDs []int)
	// delay folds a burst of scans, such as autoscan ingesting one file at a
	// time, into one refresh.
	delay time.Duration

	mu      sync.Mutex
	pending map[int]struct{}
	wake    chan struct{}
}

// NewSmartCountScheduler returns a scheduler that refreshes through refresher
// once delay has passed since the first library changed.
func NewSmartCountScheduler(refresher *SmartCountRefresher, delay time.Duration) *SmartCountScheduler {
	return newSmartCountScheduler(func(ctx context.Context, libraryIDs []int) {
		refreshed, errs := refresher.RefreshLibraries(ctx, libraryIDs)
		slog.DebugContext(ctx, "smart-count refresh after library change complete", "component", "catalog",
			"library_ids", libraryIDs, "refreshed", refreshed, "errors", errs)
	}, delay)
}

func newSmartCountScheduler(refresh func(context.Context, []int), delay time.Duration) *SmartCountScheduler {
	return &SmartCountScheduler{
		refresh: refresh,
		delay:   delay,
		pending: map[int]struct{}{},
		wake:    make(chan struct{}, 1),
	}
}

// LibraryChanged marks a library whose titles changed. It never blocks, so it
// is safe to call from an event bus observer.
func (s *SmartCountScheduler) LibraryChanged(libraryID int) {
	if s == nil || libraryID <= 0 {
		return
	}
	s.mu.Lock()
	s.pending[libraryID] = struct{}{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run refreshes the changed libraries' smart collections until ctx ends. A
// library that changes during a refresh is refreshed again afterwards.
func (s *SmartCountScheduler) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		}
		timer := time.NewTimer(s.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		s.mu.Lock()
		libraryIDs := make([]int, 0, len(s.pending))
		for id := range s.pending {
			libraryIDs = append(libraryIDs, id)
		}
		clear(s.pending)
		s.mu.Unlock()
		if len(libraryIDs) > 0 {
			s.refresh(ctx, libraryIDs)
		}
	}
}
