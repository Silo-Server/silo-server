package triggers

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

func TestStartSpreadKeepsRunsInsideTheWindow(t *testing.T) {
	at := func(day, hour, minute int) time.Time {
		return time.Date(2026, time.October, day, hour, minute, 0, 0, time.UTC)
	}
	daily := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeDaily, TimeOfDay: "04:00"}
	saturday := taskmanager.TriggerConfig{Type: taskmanager.TriggerTypeWeekly, TimeOfDay: "04:00", DayOfWeek: int(time.Saturday)}

	cases := []struct {
		name    string
		trigger interface {
			taskmanager.SpreadableTrigger
			calcNextRun(time.Time) time.Time
		}
		spread time.Duration
		now    time.Time
		slot   time.Time
	}{
		{"daily without spread", NewDailyTrigger(daily), 0, at(24, 3, 0), at(24, 4, 0)},
		{"daily before the slot", NewDailyTrigger(daily), 4 * time.Hour, at(24, 3, 0), at(24, 4, 0)},
		// A rearm after a run that started inside the window must wait for the
		// next slot, or the task would run twice in one day.
		{"daily inside the window", NewDailyTrigger(daily), 4 * time.Hour, at(24, 5, 30), at(25, 4, 0)},
		{"weekly inside the window", NewWeeklyTrigger(saturday), 4 * time.Hour, at(24, 5, 30), at(31, 4, 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.trigger.SetStartSpread(tc.spread)
			// Without a spread the window holds only the slot itself.
			end := tc.slot.Add(max(tc.spread, time.Nanosecond))
			offset := false
			for range 200 {
				next := tc.trigger.calcNextRun(tc.now)
				if next.Before(tc.slot) || !next.Before(end) {
					t.Fatalf("next run = %s, want in [%s, %s)", next, tc.slot, end)
				}
				offset = offset || next.After(tc.slot)
			}
			if tc.spread > 0 && !offset {
				t.Fatalf("200 runs all started at %s, want a random offset", tc.slot)
			}
		})
	}
}
