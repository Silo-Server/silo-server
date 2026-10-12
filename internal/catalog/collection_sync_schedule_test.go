package catalog

import (
	"testing"
	"time"
)

// TestComputeNextSyncAtFromUsesLocalWallTime checks that a cron expression is
// evaluated on the node's local clock whatever zone the reference time is in,
// and that the result keeps the reference time's location.
func TestComputeNextSyncAtFromUsesLocalWallTime(t *testing.T) {
	local := time.FixedZone("UTC-5", -5*60*60)
	saved := time.Local
	time.Local = local
	t.Cleanup(func() { time.Local = saved })

	after := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC) // 07:00 local
	next := ComputeNextSyncAtFrom("30 4 * * *", after)
	if next == nil {
		t.Fatal("ComputeNextSyncAtFrom returned nil")
	}
	if next.Location() != time.UTC {
		t.Errorf("location = %s, want UTC", next.Location())
	}
	earliest := time.Date(2026, time.July, 2, 4, 30, 0, 0, local)
	if next.Before(earliest) || !next.Before(earliest.Add(15*time.Minute)) {
		t.Errorf("next = %s, want within 15 minutes after %s", next, earliest)
	}
}

// TestParseCronExpressionAcceptsZonePrefix checks that a schedule may still
// name its own zone with a TZ= or CRON_TZ= prefix, as /api/v1 always allowed.
func TestParseCronExpressionAcceptsZonePrefix(t *testing.T) {
	for _, expr := range []string{"30 4 * * *", "CRON_TZ=UTC 30 4 * * *", "TZ=America/Chicago 30 4 * * *"} {
		if err := ParseCronExpression(expr); err != nil {
			t.Errorf("ParseCronExpression(%q) = %v", expr, err)
		}
	}
}

// TestComputeNextSyncAtFromHonorsZonePrefix checks that a schedule with a TZ=
// or CRON_TZ= prefix runs in the zone the prefix names, not on the node's
// local clock.
func TestComputeNextSyncAtFromHonorsZonePrefix(t *testing.T) {
	local := time.FixedZone("UTC-5", -5*60*60)
	saved := time.Local
	time.Local = local
	t.Cleanup(func() { time.Local = saved })

	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skipf("time zone database unavailable: %v", err)
	}
	after := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	for schedule, earliest := range map[string]time.Time{
		"CRON_TZ=UTC 30 4 * * *":   time.Date(2026, time.July, 2, 4, 30, 0, 0, time.UTC),
		"TZ=Asia/Tokyo 30 4 * * *": time.Date(2026, time.July, 2, 4, 30, 0, 0, tokyo),
	} {
		next := ComputeNextSyncAtFrom(schedule, after)
		if next == nil {
			t.Fatalf("ComputeNextSyncAtFrom(%q) returned nil", schedule)
		}
		if next.Location() != time.UTC {
			t.Errorf("ComputeNextSyncAtFrom(%q) location = %s, want UTC", schedule, next.Location())
		}
		if next.Before(earliest) || !next.Before(earliest.Add(15*time.Minute)) {
			t.Errorf("ComputeNextSyncAtFrom(%q) = %s, want within 15 minutes after %s", schedule, next, earliest)
		}
	}
}
