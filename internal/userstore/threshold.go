package userstore

// ProgressThresholds bundles the watched and min-resume threshold percentages.
// Zero values mean "use defaults" (90% watched, 5% min-resume).
type ProgressThresholds struct {
	WatchedPct   int // mark completed above this % (default 90)
	MinResumePct int // discard progress below this % (default 5)
}

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

// ResolveProgressState classifies one progress event against the thresholds:
// skip (below the min-resume floor — discard entirely), completed (above the
// watched threshold — the returned position resets to 0 so completed rows
// hold no resume point, matching MarkWatched), or a plain in-progress update.
// The single home of the rule shared by both store backends and the
// offline-sync ingest; `completed` remains a one-way latch at the write site.
func ResolveProgressState(position, duration float64, t ProgressThresholds) (pos float64, completed, skip bool) {
	// Clamp malformed client input so negative values can never classify — or
	// persist — as real progress on any backend.
	if position < 0 {
		position = 0
	}
	if duration < 0 {
		duration = 0
	}
	if duration > 0 && position > 0 && position/duration < MinResumeFraction(t.MinResumePct) {
		return position, false, true
	}
	completed = duration > 0 && position/duration > WatchedFraction(t.WatchedPct)
	if completed {
		position = 0
	}
	return position, completed, false
}
