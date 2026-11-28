package directconn

import (
	"context"
	"errors"
	"testing"

	"github.com/darthShadow/shunpike/coordination/server"
)

// An enrollment checks its household profile after it writes, as it checks the
// login session: a profile deleted between the request's start and its write
// has no revocation listing left to withdraw the entry, so Enroll withdraws it
// and refuses.
func TestEnrollWithdrawsAnEntryWhoseProfileWasDeleted(t *testing.T) {
	for _, tc := range []struct {
		name      string
		active    bool
		checkErr  error
		wantErr   error
		wantEntry bool
	}{
		{name: "profile deleted", wantErr: ErrProfileGone},
		{name: "profile check fails", checkErr: errors.New("postgres down"), wantErr: server.ErrUnavailable},
		{name: "profile live", active: true, wantEntry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newTestClock()
			store := newMemoryStore()
			ref := deviceRef(7, "tv")
			c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: clock.Now,
				ProfileActive: func(ctx context.Context, userID int, profileID string) (bool, error) {
					if userID != 7 || profileID != "profile-1" {
						t.Errorf("profile checked = %d/%q, want 7/profile-1", userID, profileID)
					}
					if _, err := store.Device(ctx, ref, clock.Now()); err != nil {
						t.Errorf("entry when the profile is checked: %v, want it already written", err)
					}
					return tc.active, tc.checkErr
				}})
			if err != nil {
				t.Fatal(err)
			}

			enrollment, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "automatic"})

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || enrollment.Ticket != "" {
					t.Fatalf("Enroll = ticket issued %t, %v; want %v and no ticket", enrollment.Ticket != "", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Enroll: %v", err)
			}
			_, err = store.Device(t.Context(), ref, clock.Now())
			if gone := errors.Is(err, server.ErrNotFound); gone == tc.wantEntry {
				t.Fatalf("entry after Enroll: %v, want present %t", err, tc.wantEntry)
			}
		})
	}
}
