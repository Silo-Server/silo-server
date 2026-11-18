package auth

import (
	"errors"
	"testing"
	"time"
)

func TestSameApprovedIdentityIncludesProfile(t *testing.T) {
	t.Parallel()

	userID := 7
	profileID := "profile-a"
	record := &deviceLoginRecord{
		ApprovedByUserID:  &userID,
		ApprovedProfileID: &profileID,
	}
	if !sameApprovedIdentity(record, userID, profileID) {
		t.Fatal("matching user/profile was not idempotent")
	}
	if sameApprovedIdentity(record, userID, "profile-b") {
		t.Fatal("different profile was treated as the same identity")
	}
	if sameApprovedIdentity(record, 8, profileID) {
		t.Fatal("different user was treated as the same identity")
	}
}

func TestValidateDeviceLoginDecisionTerminalStates(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Minute)
	tests := []struct {
		status string
		want   error
	}{
		{status: DeviceLoginStatusPending},
		{status: DeviceLoginStatusApproved},
		{status: DeviceLoginStatusDenied, want: ErrDeviceLoginDenied},
		{status: DeviceLoginStatusConsumed, want: ErrDeviceLoginConsumed},
		{status: DeviceLoginStatusCanceled, want: ErrDeviceLoginCanceled},
		{status: "unexpected", want: ErrDeviceLoginConflict},
	}
	for _, tt := range tests {
		record := &deviceLoginRecord{Status: tt.status, ExpiresAt: future}
		if err := validateDeviceLoginDecision(record); !errors.Is(err, tt.want) {
			t.Fatalf("status %q error = %v, want %v", tt.status, err, tt.want)
		}
	}
}
