package auth

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeDeviceLoginPurpose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		purpose   string
		temporary bool
		want      string
		wantTemp  bool
		wantErr   error
	}{
		{name: "legacy default", want: DeviceLoginPurposeLogin},
		{name: "explicit login", purpose: DeviceLoginPurposeLogin, want: DeviceLoginPurposeLogin},
		{name: "remote playback", purpose: DeviceLoginPurposeRemote, temporary: true, want: DeviceLoginPurposeRemote, wantTemp: true},
		{name: "temporary generic rejected", temporary: true, wantErr: ErrDeviceLoginBadPurpose},
		{name: "non-temporary remote rejected", purpose: DeviceLoginPurposeRemote, wantErr: ErrDeviceLoginBadPurpose},
		{name: "unknown rejected", purpose: "other", wantErr: ErrDeviceLoginBadPurpose},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, gotTemp, err := normalizeDeviceLoginPurpose(tt.purpose, tt.temporary)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want || gotTemp != tt.wantTemp {
				t.Fatalf("purpose = (%q, %v), want (%q, %v)", got, gotTemp, tt.want, tt.wantTemp)
			}
		})
	}
}

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

// TestQuickConnectCodeIsTheSecretsCode: a Quick Connect code is fixed by its
// secret, so the app can be told it again on every poll, and it is always
// six digits without a leading zero.
func TestQuickConnectCodeIsTheSecretsCode(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		secret, err := randomQuickConnectSecret()
		if err != nil {
			t.Fatal(err)
		}
		code := QuickConnectCode(secret)
		if len(code) != 6 || code[0] == '0' || strings.Trim(code, "0123456789") != "" {
			t.Fatalf("code %q for %q is not six digits without a leading zero", code, secret)
		}
		if again := QuickConnectCode(" " + secret + " "); again != code {
			t.Fatalf("code changed with surrounding space: %q, %q", code, again)
		}
		seen[code] = true
	}
	if len(seen) < 190 {
		t.Fatalf("200 secrets gave only %d distinct codes", len(seen))
	}
}
