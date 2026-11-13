package taskmanager

import (
	"context"
	"time"
)

// ClusterFiringTask marks a task that every API server schedules but that
// must run once per scheduled firing across them. Before a trigger runs it,
// the manager claims the firing through its FiringClaims; a server that loses
// the claim skips the firing and rearms. Manual runs never claim.
type ClusterFiringTask interface {
	ClaimsFirings() bool
}

// FiringClaims records which process runs a scheduled firing.
type FiringClaims interface {
	// ClaimFiring records a claim for the task at slot and reports true,
	// unless a claim for the task at slot, or one taken less than window
	// ago, already exists. Claims of one task are serialized, so of any
	// set of calls that would conflict exactly one reports true.
	ClaimFiring(ctx context.Context, key string, slot time.Time, window time.Duration) (bool, error)
}

// StartupClaimWindow is how long a startup firing's claim keeps other
// servers' startup firings of the task from running: API servers of one
// cluster start, or register a new task, within it, so a startup trigger runs
// once per cluster start rather than once per server.
const StartupClaimWindow = 10 * time.Minute

// FiringSlot is the cluster-wide identity of a firing of trigger at firedAt,
// the same on every server whose clock and time zone agree, and the window in
// which an earlier claim of the task also covers it. ok is false when the
// trigger names no slot every server would agree on (a malformed time of day,
// an interval that isn't positive, an unknown type, or no trigger at all): a
// claim keyed on firedAt alone could be won on several servers.
//
//   - daily and weekly: the scheduled instant nearest firedAt, so a start
//     spread or a late timer still names the firing it serves; no window.
//   - interval: firedAt truncated to the interval, plus a window of the
//     interval less a tenth (at most a minute) for timing slack, so two
//     servers whose timers straddle a slot boundary still run once, and a
//     server that lost a claim and rearmed waits a whole interval.
//   - startup: firedAt, with StartupClaimWindow.
func FiringSlot(trigger TriggerConfig, firedAt time.Time) (slot time.Time, window time.Duration, ok bool) {
	switch trigger.Type {
	case TriggerTypeDaily:
		slot, ok = nearestSlot(firedAt, trigger.TimeOfDay, 1, firedAt.Weekday())
		return slot, 0, ok
	case TriggerTypeWeekly:
		slot, ok = nearestSlot(firedAt, trigger.TimeOfDay, 7, time.Weekday(trigger.DayOfWeek))
		return slot, 0, ok
	case TriggerTypeInterval:
		interval := time.Duration(trigger.IntervalMs) * time.Millisecond
		if interval <= 0 {
			return time.Time{}, 0, false
		}
		return firedAt.Truncate(interval), interval - min(interval/10, time.Minute), true
	case TriggerTypeStartup:
		return firedAt, StartupClaimWindow, true
	default:
		return time.Time{}, 0, false
	}
}

// nearestSlot is the instant at timeOfDay on weekday nearest at, in at's
// location, for a schedule that repeats every periodDays days. ok is false
// for a time of day that doesn't parse.
func nearestSlot(at time.Time, timeOfDay string, periodDays int, weekday time.Weekday) (time.Time, bool) {
	clock, err := time.Parse("15:04", timeOfDay)
	if err != nil {
		return time.Time{}, false
	}
	day := at.AddDate(0, 0, int(weekday)-int(at.Weekday()))
	candidate := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, at.Location())
	best := candidate
	for _, offset := range []int{-periodDays, periodDays} {
		other := candidate.AddDate(0, 0, offset)
		if other.Sub(at).Abs() < best.Sub(at).Abs() {
			best = other
		}
	}
	return best, true
}

func claimsFirings(task Task) bool {
	claimer, ok := task.(ClusterFiringTask)
	return ok && claimer.ClaimsFirings()
}

// SetFiringClaims sets where ClusterFiringTask firings are claimed. With
// none, such a task runs every firing, as on a single server.
func (m *TaskManager) SetFiringClaims(claims FiringClaims) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.firingClaims = claims
}

// claimFiring reports whether this process runs a firing of w's task by
// fired. A claim that cannot be recorded fails closed: no server may be sure
// it won, so this one skips the firing.
func (m *TaskManager) claimFiring(ctx context.Context, w *taskWorker, fired Trigger) bool {
	if !claimsFirings(w.task) {
		return true
	}
	m.mu.RLock()
	claims := m.firingClaims
	m.mu.RUnlock()
	if claims == nil {
		return true
	}
	var trigger TriggerConfig
	firedAt := time.Now()
	if fired != nil {
		trigger = fired.Config()
		// A daily or weekly timer that wakes many hours late (a paused
		// process, a clock jump) still names the firing it was armed for,
		// not the one nearest the late wake-up. A due time in the future
		// means a run rearmed the trigger since it fired; keep the wake-up.
		if trigger.Type == TriggerTypeDaily || trigger.Type == TriggerTypeWeekly {
			if due := fired.NextRunTime(); !due.IsZero() && !due.After(firedAt) {
				firedAt = due
			}
		}
	}
	slot, window, ok := FiringSlot(trigger, firedAt)
	if !ok {
		// No trigger fired (a schedule change woke the loop), or its slot
		// isn't one every server would compute: running here could repeat on
		// another server, so skip.
		m.logger.WarnContext(ctx, "scheduled task firing has no cluster-wide slot; skipping it",
			"task", w.task.Key(), "trigger", trigger.Type)
		return false
	}
	won, err := claims.ClaimFiring(ctx, w.task.Key(), slot, window)
	if err != nil {
		m.logger.WarnContext(ctx, "claiming scheduled task firing failed; skipping it",
			"task", w.task.Key(), "error", err)
		return false
	}
	if !won {
		m.logger.DebugContext(ctx, "scheduled task firing runs on another server",
			"task", w.task.Key(), "slot", slot)
	}
	return won
}

// reloadLastResult reads w's latest run from history after another server
// took its firing. Only local runs update lastResult, so without this a
// server that keeps losing claims shows the run it loaded at startup as the
// task's last execution.
func (m *TaskManager) reloadLastResult(ctx context.Context, w *taskWorker) {
	latest, err := m.historyRepo.GetLatest(ctx, w.task.Key())
	if err != nil || latest == nil {
		return
	}
	w.mu.Lock()
	if w.lastResult == nil || latest.CompletedAt.After(w.lastResult.CompletedAt) {
		w.lastResult = latest
	}
	w.mu.Unlock()
}
