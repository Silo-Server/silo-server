package catalog

import (
	"testing"
	"time"
)

func TestInferAirTimezone_UsesNetworkBeforeCountry(t *testing.T) {
	t.Parallel()

	got := InferAirTimezone([]string{"BBC One"}, []string{"Japan"})
	if got != "Europe/London" {
		t.Fatalf("timezone = %q, want Europe/London", got)
	}
}

func TestInferAirTimezone_UsesSingleTimezoneCountry(t *testing.T) {
	t.Parallel()

	got := InferAirTimezone(nil, []string{"South Korea"})
	if got != "Asia/Seoul" {
		t.Fatalf("timezone = %q, want Asia/Seoul", got)
	}
}

func TestValidateAirTimezone(t *testing.T) {
	t.Parallel()

	if !ValidateAirTimezone("America/New_York") {
		t.Fatal("expected America/New_York to be valid")
	}
	if ValidateAirTimezone("Eastern") {
		t.Fatal("expected Eastern to be invalid")
	}
}

func TestCalendarEventAirAt_ConvertsSourceTimezoneToUTC(t *testing.T) {
	t.Parallel()

	airDate := time.Date(2026, time.May, 28, 0, 0, 0, 0, time.UTC)
	airTime := "23:30"
	airTimezone := "Asia/Tokyo"

	got := CalendarEventAirAt(airDate, &airTime, &airTimezone)
	if got == nil {
		t.Fatal("expected air_at")
	}

	want := time.Date(2026, time.May, 28, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("air_at = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestCalendarEventLocalTime_ConvertsZonedEventToViewer(t *testing.T) {
	t.Parallel()

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	airDate := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	airTime := "21:00"
	airTimezone := "Europe/London"

	got, hasTime := CalendarEventLocalTime(airDate, &airTime, &airTimezone, ny)
	if !hasTime {
		t.Fatal("expected hasTime=true for a zoned event")
	}
	// 21:00 GMT == 16:00 EST, same calendar day.
	want := time.Date(2026, time.January, 1, 16, 0, 0, 0, ny)
	if !got.Equal(want) {
		t.Fatalf("local time = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestCalendarEventLocalTime_UsesRawWallClockWithoutTimezone(t *testing.T) {
	t.Parallel()

	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	airDate := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	airTime := "20:00"

	got, hasTime := CalendarEventLocalTime(airDate, &airTime, nil, ny)
	if !hasTime {
		t.Fatal("expected hasTime=true when air_time is present")
	}
	// No source timezone: the wall-clock time is shown as-is (8:00 PM), not converted.
	if got.Hour() != 20 || got.Minute() != 0 {
		t.Fatalf("local time = %02d:%02d, want 20:00", got.Hour(), got.Minute())
	}
	if got.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("local date = %s, want 2026-01-01", got.Format("2006-01-02"))
	}
}

func TestCalendarEventLocalTime_DateOnlyEntryHasNoTime(t *testing.T) {
	t.Parallel()

	airDate := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	got, hasTime := CalendarEventLocalTime(airDate, nil, nil, nil)
	if hasTime {
		t.Fatal("expected hasTime=false when air_time is absent")
	}
	if got.Format("2006-01-02") != "2026-01-01" {
		t.Fatalf("local date = %s, want 2026-01-01", got.Format("2006-01-02"))
	}
}

func TestCalendarEventAirAt_UsesDSTForSourceDate(t *testing.T) {
	t.Parallel()

	airTime := "20:00"
	airTimezone := "America/New_York"
	winter := CalendarEventAirAt(time.Date(2026, time.January, 15, 0, 0, 0, 0, time.UTC), &airTime, &airTimezone)
	summer := CalendarEventAirAt(time.Date(2026, time.July, 15, 0, 0, 0, 0, time.UTC), &airTime, &airTimezone)
	if winter == nil || summer == nil {
		t.Fatal("expected air_at values")
	}
	if winter.Hour() != 1 {
		t.Fatalf("winter UTC hour = %d, want 1", winter.Hour())
	}
	if summer.Hour() != 0 {
		t.Fatalf("summer UTC hour = %d, want 0", summer.Hour())
	}
}
