package triggers

import (
	"testing"
	"time"
	_ "time/tzdata" // the zones below load even where the host has no zoneinfo

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// Daily and weekly triggers keep their wall-clock time when a daylight-saving
// change makes a day 23 or 25 hours long, and a time the change skips runs
// once, after the clocks jump.
func TestNextRunKeepsWallClockAcrossDaylightSaving(t *testing.T) {
	zone := func(name string) func(time.Month, int, int, int) time.Time {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return func(month time.Month, day, hour, minute int) time.Time {
			return time.Date(2026, month, day, hour, minute, 0, 0, loc)
		}
	}
	// New York falls back on Sunday 2026-11-01 and springs forward on Sunday
	// 2026-03-08, both at 02:00. Berlin springs forward at 02:00 on Sunday
	// 2026-03-29, and Santiago at midnight on Sunday 2026-09-06.
	ny, berlin, santiago := zone("America/New_York"), zone("Europe/Berlin"), zone("America/Santiago")
	daily := func(timeOfDay string) func(time.Time) time.Time {
		return NewDailyTrigger(taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: timeOfDay}).calcNextRun
	}
	sunday := func(timeOfDay string) func(time.Time) time.Time {
		return NewWeeklyTrigger(taskmanager.TriggerConfig{
			Type: taskmanager.TriggerTypeWeekly, TimeOfDay: timeOfDay, DayOfWeek: int(time.Sunday),
		}).calcNextRun
	}
	for _, tc := range []struct {
		name    string
		nextRun func(time.Time) time.Time
		now     time.Time
		want    time.Time
	}{
		{"daily into fall back", daily("04:00"), ny(time.October, 31, 4, 10), ny(time.November, 1, 4, 0)},
		{"weekly into fall back", sunday("04:00"), ny(time.October, 25, 4, 10), ny(time.November, 1, 4, 0)},
		// time.Date moves the skipped 02:30 back to 01:30 EST, which a rearm
		// at 01:45 has passed. The day still runs, when the clocks jump.
		{"daily skipped time moved back", daily("02:30"), ny(time.March, 8, 1, 45), ny(time.March, 8, 3, 0)},
		// time.Date moves the skipped 02:30 forward to 03:30 CEST, after the
		// jump. It stays there, where the backup task's nightly slot puts it
		// too; firing at the 03:00 jump would have that task skip the night.
		{"daily skipped time moved forward", daily("02:30"), berlin(time.March, 29, 1, 50), berlin(time.March, 29, 3, 30)},
		// time.Date moves the skipped Sunday 00:00 back to Saturday 23:00. A
		// rearm on Saturday at 23:30 still runs it that Sunday, at the jump.
		{"weekly skipped time moved to the day before", sunday("00:00"), santiago(time.September, 5, 23, 30), santiago(time.September, 6, 1, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.nextRun(tc.now); !got.Equal(tc.want) {
				t.Fatalf("calcNextRun(%s) = %s; want %s", tc.now, got, tc.want)
			}
		})
	}
}
