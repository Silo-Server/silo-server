package backup

import (
	"testing"
	"time"
	_ "time/tzdata"
)

// Every night gets one scheduled backup, also on the nights the clocks
// change: the first server to fire takes the night's backup, and another
// server firing later that night skips it. On the spring-forward night a
// backup.time in the skipped hour (02:30 in New York and Berlin) doesn't
// exist; the previous night's slot still has to come out a calendar day
// earlier, not a day before tonight's moved slot (#4707).
func TestNightlySlotBacksUpEveryNightAcrossDaylightSaving(t *testing.T) {
	nights := [][2]time.Time{
		{time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)},
		{time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)},
	}
	for _, zone := range []string{"America/New_York", "Europe/Berlin"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatalf("load %s: %v", zone, err)
		}
		for _, at := range []string{"00:30", "01:30", "02:30", "03:30", "04:00"} {
			hour, minute := int(at[0]-'0')*10+int(at[1]-'0'), int(at[3]-'0')*10+int(at[4]-'0')
			var last *time.Time
			for _, span := range nights {
				for day := span[0]; day.Before(span[1]); day = day.AddDate(0, 0, 1) {
					// A run just after time.Date's instant for backup.time. On
					// the spring-forward night that instant is moved; the real
					// trigger (triggers.wallClock) fires at it or at the clock
					// jump, and both give the same slot.
					fire := time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 30, 0, loc)
					slot, done := nightlySlotDone(fire, at, loc, last)
					if done {
						t.Errorf("%s %s: the run at %v skipped the night (slot %v, last %v)", zone, at, fire, slot, last)
						continue
					}
					last = &slot
					if _, done := nightlySlotDone(fire.Add(20*time.Minute), at, loc, last); !done {
						t.Errorf("%s %s: a second server at %v backed up the night again (slot %v)", zone, at, fire.Add(20*time.Minute), slot)
					}
				}
				last = nil
			}
		}
	}
}

// nightlySlotDone is the slot a run at now records and whether a backup
// already took that night, given the last recorded slot.
func nightlySlotDone(now time.Time, at string, loc *time.Location, last *time.Time) (time.Time, bool) {
	slot, prev := nightlySlot(now, at, loc)
	return slot, slotDone(last, prev)
}
