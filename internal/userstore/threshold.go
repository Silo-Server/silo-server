package userstore

// ProgressThresholds bundles the watched and min-resume threshold percentages.
// Zero values mean "use defaults" (90% watched, 5% min-resume).
type ProgressThresholds struct {
	WatchedPct   int // mark completed above this % (default 90)
	MinResumePct int // discard progress below this % (default 5)
	// CreditsStartSeconds is where the played file's end credits start, on
	// the timeline the duration measures; zero when unknown. Reaching it
	// marks the item watched even below WatchedPct.
	CreditsStartSeconds float64
}

// Watched reports whether position finishes an item of duration seconds:
// past the watched threshold, or into its end credits.
func (t ProgressThresholds) Watched(position, duration float64) bool {
	if duration <= 0 {
		return false
	}
	if position/duration > WatchedFraction(t.WatchedPct) {
		return true
	}
	return t.CreditsStartSeconds > 0 && t.CreditsStartSeconds < duration && position >= t.CreditsStartSeconds
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
// completed (Watched — the returned position resets to 0 so completed rows
// hold no resume point, matching MarkWatched), skip (below the min-resume
// floor — discard entirely), or a plain in-progress update. Completion is
// decided first, so credits that start below the min-resume floor still
// finish the item.
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
	if t.Watched(position, duration) {
		return 0, true, false
	}
	if duration > 0 && position > 0 && position/duration < MinResumeFraction(t.MinResumePct) {
		return position, false, true
	}
	return position, false, false
}
