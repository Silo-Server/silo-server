package usercollections

import "testing"

func TestResolveSyncScheduleBoundsRegularAccounts(t *testing.T) {
	t.Parallel()

	daily, err := ResolveSyncSchedule("daily", false)
	if err != nil {
		t.Fatalf("resolve daily: %v", err)
	}
	if daily == nil || *daily != "0 3 * * *" {
		t.Fatalf("daily = %v, want 0 3 * * *", daily)
	}

	if _, err := ResolveSyncSchedule("0 * * * *", false); err == nil {
		t.Fatal("regular account accepted an hourly cron schedule")
	}
}

func TestResolveSyncScheduleAllowsAdminCron(t *testing.T) {
	t.Parallel()

	for _, schedule := range append(append([]string(nil), AdminSyncSchedulePresets...), "15 */2 * * *") {
		got, err := ResolveSyncSchedule(schedule, true)
		if err != nil {
			t.Fatalf("resolve %q: %v", schedule, err)
		}
		if got == nil || *got != schedule {
			t.Fatalf("resolve %q = %v", schedule, got)
		}
	}

	if _, err := ResolveSyncSchedule("not a cron", true); err == nil {
		t.Fatal("admin account accepted an invalid cron schedule")
	}
}

func TestResolveSyncScheduleDisablesAutomaticSync(t *testing.T) {
	t.Parallel()

	got, err := ResolveSyncSchedule("  ", true)
	if err != nil {
		t.Fatalf("resolve empty schedule: %v", err)
	}
	if got != nil {
		t.Fatalf("empty schedule = %q, want nil", *got)
	}
}
