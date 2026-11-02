package playback

import "time"

// Watch-time measurement limits. Clients do not report a playback rate, so a
// position advance is accepted as viewing when it fits a rate up to
// watchTimeMaxRate plus watchTimeSlackSeconds of reporting jitter; anything
// larger is a seek. These are deliberately loose: the cost of a wrong guess is
// a few seconds credited to a short skip, not hours credited to a resume.
const (
	watchTimeMaxRate      = 2.0
	watchTimeSlackSeconds = 2.0
)

// WatchTime measures how much of a session was actually watched, from the
// progress samples the player reports. Session.Position is only where the
// player is now: a play resumed at the one-hour mark reports an hour of
// position after a minute of viewing, and a seek moves it without anyone
// watching. WatchTime instead credits the position advance between
// consecutive samples while the player was playing, and wall-clock time while
// it was paused, so resumes, seeks, buffering and pauses do not count as
// viewing.
//
// The zero value is ready to use; Observe the first sample to start it. It is
// a plain value so the copies GetSession hands out carry it along.
type WatchTime struct {
	// Measured reports whether any sample was observed. A session that saw
	// none has no measurement, not a measurement of zero.
	Measured bool
	// StartPosition is the position of the first observed sample, in seconds.
	StartPosition float64
	// PlayedSeconds is the content advanced through by normal playback.
	PlayedSeconds float64
	// PausedSeconds is the wall-clock time spent paused.
	PausedSeconds float64
	// Since and Until are when the first and the latest sample were observed.
	// A copy whose measurement starts after another copy's ended measured a
	// later stretch of the play, not the same one.
	Since time.Time
	Until time.Time

	lastPosition float64
	lastPaused   bool
	// pausedAt is when the player confirmed a pause whose position no sample
	// has reported yet; zero otherwise.
	pausedAt time.Time
}

// Observe records a progress sample: the player's position and pause state
// at now. The interval since the previous sample is attributed to the pause
// state the player was in during it, which is the state the previous sample
// reported.
func (w *WatchTime) Observe(position float64, paused bool, now time.Time) {
	if position < 0 {
		position = 0
	}
	if !w.Measured {
		w.Measured = true
		w.StartPosition = position
		w.Since = now
		w.Until = now
		w.lastPosition = position
		w.lastPaused = paused
		return
	}

	elapsed := secondsBetween(w.Until, now)
	if !w.pausedAt.IsZero() {
		if paused {
			// The player played until the confirmed pause and was paused
			// after it, so only the time before it can explain the advance.
			w.PausedSeconds += secondsBetween(w.pausedAt, now)
			elapsed = secondsBetween(w.Until, w.pausedAt)
		}
		// Otherwise it resumed at some unknown point since, and the whole
		// interval is judged as playing.
		w.pausedAt = time.Time{}
	}
	if w.lastPaused {
		w.PausedSeconds += elapsed
	} else if advance := position - w.lastPosition; advance > 0 &&
		advance <= elapsed*watchTimeMaxRate+watchTimeSlackSeconds {
		w.PlayedSeconds += advance
	}
	// A rewind, or an advance too large to have been played, is a seek:
	// nothing is credited, and measurement continues from the new position.

	w.Until = now
	w.lastPosition = position
	w.lastPaused = paused
}

// Pause records that the player confirmed a pause at now without reporting
// where it held. If the next sample is still paused, it supplies that
// position: the advance to it is judged against the playing time before now,
// and the time after now counts as paused, so a seek made while paused is not
// credited as viewing.
func (w *WatchTime) Pause(now time.Time) {
	if w.Measured && !w.lastPaused && w.pausedAt.IsZero() {
		w.pausedAt = now
	}
}

func secondsBetween(from, to time.Time) float64 {
	return max(to.Sub(from).Seconds(), 0)
}
