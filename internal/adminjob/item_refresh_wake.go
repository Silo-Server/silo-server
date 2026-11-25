package adminjob

import "context"

// The item types whose complete refresh rebuilds a series root.
const (
	itemTypeSeries  = "series"
	itemTypeSeason  = "season"
	itemTypeEpisode = "episode"
)

// itemRefreshSeriesRootWaker returns a scope's backed-off series root queue
// rows to pending.
type itemRefreshSeriesRootWaker interface {
	WakeInScope(ctx context.Context, folderID int, scopePath string) error
}

// SetSeriesRootWaker lets a complete refresh of a series, season or episode
// match its series root at once, although an earlier failure backed the root
// off in the TV match queue.
func (e *ItemRefreshExecutor) SetSeriesRootWaker(waker itemRefreshSeriesRootWaker) {
	e.seriesRoots = waker
}

// wakeSeriesRoots wakes the series roots of a complete refresh's scope. The
// refresh's scan queues the root again, but a root an earlier failure backed
// off keeps its later available_at, so its match would not run before the
// refresh gives up. A hand-matched folder that no provider finds on its own
// then left both the library and Unmatched Items until the back-off ended
// (#5337). It runs before the scope's links are cleared, so a failed wake
// leaves the scope as it was.
func (e *ItemRefreshExecutor) wakeSeriesRoots(ctx context.Context, req ItemRefreshRequest) error {
	if !isSeriesTarget(req.RequestedType) || e.seriesRoots == nil {
		return nil
	}
	return e.seriesRoots.WakeInScope(ctx, req.ScanFolderID, req.ScanPath)
}

// isSeriesTarget reports whether a refresh of requestedType rebuilds a series
// root, which the TV match queue matches.
func isSeriesTarget(requestedType string) bool {
	switch requestedType {
	case itemTypeSeries, itemTypeSeason, itemTypeEpisode:
		return true
	}
	return false
}
