package userstore

// ProgressThresholds bundles the watched and min-resume threshold percentages.
// Zero values mean "use defaults" (90% watched, 5% min-resume).
type ProgressThresholds struct {
	WatchedPct   int // mark completed above this % (default 90)
	MinResumePct int // discard progress below this % (default 5)
}

// RestartDetectFraction is the position fraction below which an in-progress
// heartbeat landing on an already-completed row is treated as a genuine
// rewatch-from-the-start (releasing the completed latch) rather than a stale
// late heartbeat. The midpoint (0.5) sits well clear of both the min-resume
// (5%) and watched (90%) thresholds, giving a wide, unambiguous detection band.
const RestartDetectFraction = 0.5

// RestartMinGapSeconds is the minimum elapsed time between a completed row's
// last update and a new early-position heartbeat before the completed latch is
// released. It guards against a stale heartbeat from a concurrent/offline
// session (which lands within seconds of the completion event) spuriously
// un-completing an item; a genuine rewatch's first qualifying heartbeat (past
// the 5% min-resume floor) arrives minutes later.
const RestartMinGapSeconds = 60

// WatchedFraction converts a watched-threshold percentage (e.g. 90) to a
// fraction (0.9). If pct <= 0, returns the default of 0.9 (90%).
func WatchedFraction(pct int) float64 {
	if pct <= 0 {
		pct = 90
	}
	return float64(pct) / 100.0
}

// MinResumeFraction converts a min-resume percentage (e.g. 5) to a fraction
// (0.05). If pct <= 0, returns the default of 0.05 (5%).
func MinResumeFraction(pct int) float64 {
	if pct <= 0 {
		pct = 5
	}
	return float64(pct) / 100.0
}
