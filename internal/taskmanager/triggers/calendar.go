package triggers

import "time"

// wallClock is hour:minute in now's location on the calendar day days after
// now's date. time.Date moves a time that a daylight-saving change skips by
// the gap's length, forward or back (Go doesn't say which). Moved back, it
// lands before the clocks change, on the previous day for a change at
// midnight, so wallClock returns the moment the clocks jump instead. A
// skipped time therefore runs at or after the jump, and never before
// time.Date's instant, which is the slot taskmanager.FiringSlot and the
// backup task's nightly slot name for it.
func wallClock(now time.Time, days, hour, minute int) time.Time {
	slot := time.Date(now.Year(), now.Month(), now.Day()+days, hour, minute, 0, 0, now.Location())
	asked := time.Date(now.Year(), now.Month(), now.Day()+days, hour, minute, 0, 0, time.UTC)
	got := time.Date(slot.Year(), slot.Month(), slot.Day(), slot.Hour(), slot.Minute(), 0, 0, time.UTC)
	if got.Before(asked) {
		_, jump := slot.ZoneBounds()
		return jump
	}
	return slot
}
