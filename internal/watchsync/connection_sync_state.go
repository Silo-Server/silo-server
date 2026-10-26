package watchsync

import "time"

// ConnectionSyncUpdate is the bookkeeping one sync flow or event records on a
// saved connection. Fields left nil keep their stored values, so the update
// never writes tokens, settings or another flow's progress from a stale copy.
type ConnectionSyncUpdate struct {
	LastInboundSyncAt   *time.Time
	LastProgressSyncAt  *time.Time
	LastOutboundSyncAt  *time.Time
	LastFavoritesSyncAt *time.Time
	LastWatchlistSyncAt *time.Time
	LastError           *string
	// RateLimitedUntil replaces the stored deferral.
	RateLimitedUntil *time.Time
	// ClearRateLimitBy drops a stored deferral that ended at or before it, and
	// keeps a later one another flow set meanwhile.
	ClearRateLimitBy *time.Time
	// SyncCursors are merged into the stored cursors; other keys are kept.
	SyncCursors map[string]string
}

// flowSucceeded is the update a flow records when it finishes at now: the
// error is cleared and a rate limit deferral that has ended is dropped. A
// deferral that has not ended keeps its error.
func flowSucceeded(now time.Time) ConnectionSyncUpdate {
	cleared := ""
	return ConnectionSyncUpdate{LastError: &cleared, ClearRateLimitBy: &now}
}

// connectionError is the update that records a flow's error.
func connectionError(message string) ConnectionSyncUpdate {
	return ConnectionSyncUpdate{LastError: &message}
}
