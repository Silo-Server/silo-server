package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/directconn"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// revocationFixture is a coordination store holding two devices of account 7
// and one of account 8. The tv was enrolled by login session "session-tv", the
// phone by "session-phone" and account 8's tv by "session-tv".
type revocationFixture struct {
	store              directconn.CoordinationStore
	coord              *directconn.Coordination
	tv, phone, other   string // tickets
	dropped, announced []int
	dropCompat         func(userID int)
	announce           func(ctx context.Context, userID int)
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	f := &revocationFixture{store: directconn.NewCoordinationStore(nil, "")}
	coord, err := directconn.NewCoordination(directconn.CoordinationConfig{Store: f.store, AuthSecret: "synthetic-direct-revocation-secret"})
	if err != nil {
		t.Fatal(err)
	}
	f.coord = coord
	f.tv = enrollDeviceBy(t, coord, 7, "session-tv", "p-owner", "tv", 1)
	f.phone = enrollDeviceBy(t, coord, 7, "session-phone", "p-owner", "phone", 2)
	f.other = enrollDeviceBy(t, coord, 8, "session-tv", "p-owner", "tv", 3)
	f.dropCompat = func(userID int) { f.dropped = append(f.dropped, userID) }
	f.announce = func(_ context.Context, userID int) { f.announced = append(f.announced, userID) }
	return f
}

func revocationKey(b byte) protocol.NodeKey {
	var k protocol.NodeKey
	k[0], k[31] = b, 1
	return k
}

// enrollDevice enrolls device as login session "session-tv".
func enrollDevice(t *testing.T, coord *directconn.Coordination, user int, profile, device string, key byte) string {
	t.Helper()
	return enrollDeviceBy(t, coord, user, "session-tv", profile, device, key)
}

func enrollDeviceBy(t *testing.T, coord *directconn.Coordination, user int, session, profile, device string, key byte) string {
	t.Helper()
	enrollment, err := coord.Enroll(context.Background(), directconn.EnrollRequest{UserID: user, ProfileID: profile, SessionID: session, DeviceID: device, NodeKey: revocationKey(key), ConnectionPath: "automatic"})
	if err != nil {
		t.Fatal(err)
	}
	return enrollment.Ticket
}

// requireTickets fails unless each named ticket authorizes (valid) or is
// refused as invalid (!valid).
func requireTickets(t *testing.T, coord *directconn.Coordination, valid bool, tickets map[string]string) {
	t.Helper()
	for name, ticket := range tickets {
		_, _, err := coord.Authorize(context.Background(), ticket)
		if valid && err != nil {
			t.Errorf("%s ticket was withdrawn: %v", name, err)
		}
		if !valid && !errors.Is(err, directconn.ErrInvalidTicket) {
			t.Errorf("%s ticket after revocation: %v, want invalid", name, err)
		}
	}
}

// The hook that account disable, an admin password reset and account delete
// share withdraws every direct-connection entry of the account, whether or not
// the device registered, and leaves other accounts alone.
func TestAccountSessionsRevokedWithdrawsEveryEntry(t *testing.T) {
	for _, registered := range []bool{true, false} {
		name := "never registered"
		if registered {
			name = "registered"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newRevocationFixture(t)
			if registered {
				reg := protocol.Registration{NodeKey: revocationKey(1), DiscoKey: protocol.DiscoKey{1}, Endpoints: []netip.AddrPort{netip.MustParseAddrPort("203.0.113.9:41641")}}
				ref := server.DeviceRef{Account: "7", Device: "tv"}
				if err := f.store.PutRegistration(ctx, ref, reg, time.Now().Add(time.Minute), time.Now()); err != nil {
					t.Fatal(err)
				}
			}

			accountSessionsRevoked(f.dropCompat, f.coord, f.announce)(ctx, 7)

			requireTickets(t, f.coord, false, map[string]string{"tv": f.tv, "phone": f.phone})
			requireTickets(t, f.coord, true, map[string]string{"another account": f.other})
			if len(f.dropped) != 1 || f.dropped[0] != 7 || len(f.announced) != 1 || f.announced[0] != 7 {
				t.Errorf("compat drops %v, announcements %v, want one each for account 7", f.dropped, f.announced)
			}
		})
	}

	t.Run("enrollment unavailable", func(t *testing.T) {
		var steps []string
		hook := accountSessionsRevoked(
			func(int) { steps = append(steps, "drop") },
			nil,
			func(context.Context, int) { steps = append(steps, "announce") },
		)
		hook(context.Background(), 7)
		if len(steps) != 2 || steps[0] != "drop" || steps[1] != "announce" {
			t.Fatalf("steps = %v, want drop then announce", steps)
		}
	})
}

// A password change that signs out the other sessions keeps only the
// enrollment that the kept login session issued for the device it was made
// from, and withdraws the rest. A device enrolled by another session, or the
// kept session's enrollment of another device, goes. When the kept session's
// device cannot be told, the hook fails closed and withdraws every entry (a
// failed read never leaves a revoked device enrolled).
func TestAccountOtherSessionsRevokedKeepsCallerDevice(t *testing.T) {
	for _, row := range []struct {
		name        string
		keptSession string // "" means session-tv
		keptDev     string
		readErr     error
		keepsTV     bool
	}{
		{name: "caller device kept", keptDev: "tv", keepsTV: true},
		{name: "kept device enrolled by another session", keptSession: "session-phone", keptDev: "tv"},
		{name: "kept session enrolled another device", keptDev: "phone"},
		{name: "kept device cannot be read", readErr: errors.New("read failed")},
		{name: "kept session has no device id"},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := context.Background()
			f := newRevocationFixture(t)
			keptSession := cmp.Or(row.keptSession, "session-tv")
			var asked []string
			keptDevice := func(_ context.Context, userID int, sessionID string) (string, error) {
				asked = append(asked, sessionID)
				if userID != 7 {
					t.Errorf("kept device read for account %d, want 7", userID)
				}
				return row.keptDev, row.readErr
			}

			accountOtherSessionsRevoked(f.dropCompat, f.coord, f.announce, keptDevice)(ctx, 7, keptSession)

			requireTickets(t, f.coord, false, map[string]string{"phone": f.phone})
			if row.keepsTV {
				requireTickets(t, f.coord, true, map[string]string{"tv": f.tv})
			} else {
				requireTickets(t, f.coord, false, map[string]string{"tv": f.tv})
			}
			requireTickets(t, f.coord, true, map[string]string{"another account": f.other})
			if len(asked) != 1 || asked[0] != keptSession {
				t.Errorf("kept device read for sessions %v, want [%s]", asked, keptSession)
			}
			if len(f.dropped) != 1 || f.dropped[0] != 7 || len(f.announced) != 1 || f.announced[0] != 7 {
				t.Errorf("compat drops %v, announcements %v, want one each for account 7", f.dropped, f.announced)
			}
		})
	}

	t.Run("enrollment unavailable", func(t *testing.T) {
		var steps []string
		hook := accountOtherSessionsRevoked(
			func(int) { steps = append(steps, "drop") },
			nil,
			func(context.Context, int) { steps = append(steps, "announce") },
			func(context.Context, int, string) (string, error) {
				t.Error("kept device read with no enrollment")
				return "", nil
			},
		)
		hook(context.Background(), 7, "session-tv")
		if len(steps) != 2 || steps[0] != "drop" || steps[1] != "announce" {
			t.Fatalf("steps = %v, want drop then announce", steps)
		}
	})
}

// Deleting a profile withdraws exactly the enrollments whose entry records
// that profile. A device last enrolled by another profile, another profile's
// device and another account's entry stay.
func TestProfileDeleteWithdrawsItsEnrollments(t *testing.T) {
	ctx := context.Background()
	f := newRevocationFixture(t)
	tvA := enrollDevice(t, f.coord, 7, "p-a", "tv-a", 11)
	phoneA := enrollDevice(t, f.coord, 7, "p-a", "phone-a", 12)
	tabletB := enrollDevice(t, f.coord, 7, "p-b", "tablet-b", 13)
	// One device enrolled by p-a first, then by p-b: its entry records p-b.
	enrollDevice(t, f.coord, 7, "p-a", "shared", 14)
	shared := enrollDevice(t, f.coord, 7, "p-b", "shared", 14)
	otherAccountA := enrollDevice(t, f.coord, 8, "p-a", "tv-a", 15)

	db, err := sql.Open("sqlite3", "file:"+strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatal(err)
	}
	store := userdb.NewSQLiteUserStore(db)
	for _, id := range []string{"p-owner", "p-a", "p-b"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	handler := handlers.NewProfileHandler(singleStoreProvider{store: store})
	handler.DirectRevoker = f.coord // as router.go wires deps.DirectRevoker, which main.go sets to the coordination

	err = handler.DeleteProfile(ctx, handlers.ProfileDeleteCommand{
		UserID: 7, ProfileID: "p-a", ActiveProfileID: "p-owner",
		VerifyProfile: func(*userstore.Profile) error { return nil },
	})
	if err != nil {
		t.Fatalf("delete p-a: %v", err)
	}

	requireTickets(t, f.coord, false, map[string]string{"p-a tv": tvA, "p-a phone": phoneA})
	requireTickets(t, f.coord, true, map[string]string{
		"p-b tablet": tabletB, "shared device last enrolled by p-b": shared,
		"owner device": f.tv, "another account's p-a entry": otherAccountA,
	})
}

// singleStoreProvider serves one store for every account.
type singleStoreProvider struct{ store userstore.UserStore }

func (p singleStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	return p.store, nil
}

func (singleStoreProvider) Close() error { return nil }
