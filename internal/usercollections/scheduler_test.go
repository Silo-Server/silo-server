package usercollections

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
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

func TestBoundedSyncSchedulePolicy(t *testing.T) {
	t.Parallel()

	for label, schedule := range AllowedSyncSchedules {
		if !isBoundedSyncSchedule(schedule) {
			t.Errorf("%s schedule %q was not accepted", label, schedule)
		}
	}
	for _, schedule := range []string{"0 * * * *", "0 */6 * * *", "0 3 * * 1", "15 2 * * *"} {
		if isBoundedSyncSchedule(schedule) {
			t.Errorf("admin-only schedule %q was accepted as bounded", schedule)
		}
	}

	if !requiresScheduleDowngrade("0 * * * *", false) {
		t.Fatal("regular account retained an hourly schedule")
	}
	if requiresScheduleDowngrade("0 * * * *", true) {
		t.Fatal("admin account lost an hourly schedule")
	}
	if requiresScheduleDowngrade(AllowedSyncSchedules["daily"], false) {
		t.Fatal("regular account lost its bounded daily schedule")
	}
}

func TestScheduledCollectionEligibility(t *testing.T) {
	t.Parallel()

	hourly := "0 * * * *"
	daily := AllowedSyncSchedules["daily"]
	tests := []struct {
		name         string
		collection   *userstore.Collection
		expected     string
		allowAdmin   bool
		wantEligible bool
	}{
		{name: "matching admin schedule", collection: &userstore.Collection{SyncSchedule: &hourly}, expected: hourly, allowAdmin: true, wantEligible: true},
		{name: "automatic sync disabled", collection: &userstore.Collection{}, expected: hourly, allowAdmin: true},
		{name: "schedule edited", collection: &userstore.Collection{SyncSchedule: &daily}, expected: hourly, allowAdmin: true},
		{name: "administrator demoted", collection: &userstore.Collection{SyncSchedule: &hourly}, expected: hourly},
		{name: "bounded schedule after demotion", collection: &userstore.Collection{SyncSchedule: &daily}, expected: daily, wantEligible: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := scheduledCollectionEligible(tt.collection, tt.expected, tt.allowAdmin); got != tt.wantEligible {
				t.Fatalf("scheduledCollectionEligible() = %v, want %v", got, tt.wantEligible)
			}
		})
	}
}
