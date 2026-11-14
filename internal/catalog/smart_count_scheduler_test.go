package catalog

import (
	"context"
	"slices"
	"testing"
	"time"
)

// A burst of scans folds into one refresh of every changed library, and a
// library that changes while that refresh runs is refreshed again after it,
// not dropped.
func TestSmartCountSchedulerFoldsScansIntoOneRefresh(t *testing.T) {
	calls := make(chan []int)
	release := make(chan struct{})
	s := newSmartCountScheduler(func(_ context.Context, libraryIDs []int) {
		slices.Sort(libraryIDs)
		calls <- libraryIDs
		<-release
	}, time.Millisecond)
	next := func() []int {
		t.Helper()
		select {
		case got := <-calls:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("no refresh ran")
			return nil
		}
	}

	s.LibraryChanged(2)
	s.LibraryChanged(1)
	s.LibraryChanged(2)
	s.LibraryChanged(0) // not a library
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go s.Run(ctx)

	if got := next(); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("first refresh covered %v, want [1 2]", got)
	}
	s.LibraryChanged(3)
	release <- struct{}{}
	if got := next(); !slices.Equal(got, []int{3}) {
		t.Fatalf("second refresh covered %v, want [3]", got)
	}
	release <- struct{}{}
}
