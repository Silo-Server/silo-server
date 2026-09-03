package usercollections

import (
	"testing"
	"time"
)

func TestNextSyncAfterFailureUsesConfiguredCron(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, time.September, 3, 10, 15, 0, 0, time.UTC)
	next := nextSyncAfterFailure("0 * * * *", after)
	earliest := time.Date(2026, time.September, 3, 11, 0, 0, 0, time.UTC)
	latest := earliest.Add(15 * time.Minute)
	if next.Before(earliest) || !next.Before(latest) {
		t.Fatalf("next = %v, want [%v, %v)", next, earliest, latest)
	}
}

func TestNextSyncAfterFailureParksInvalidSchedule(t *testing.T) {
	t.Parallel()

	after := time.Date(2026, time.September, 3, 10, 15, 0, 0, time.UTC)
	if got, want := nextSyncAfterFailure("invalid", after), after.Add(24*time.Hour); !got.Equal(want) {
		t.Fatalf("next = %v, want %v", got, want)
	}
}
