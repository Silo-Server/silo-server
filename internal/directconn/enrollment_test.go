package directconn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"

	"github.com/Silo-Server/silo-server/internal/tuning"
)

// Bulk revocation reads the store, so an entry that never registered (and so is
// absent from any peer cache) is revoked like the others.
func TestCoordinationRevoke(t *testing.T) {
	rows := []struct {
		name    string
		revoke  func(c *Coordination)
		revoked []string // devices of account 7 that must lose their entry; the rest keep it
		// otherGone is true when the other account's entry must go too.
		otherGone bool
	}{
		{name: "session", revoke: func(c *Coordination) { c.RevokeSession(t.Context(), 7, testSessionID) }, revoked: []string{"tv", "phone"}},
		{name: "session with no id selects nothing", revoke: func(c *Coordination) { c.RevokeSession(t.Context(), 7, "") }},
		{name: "same session id on another account", revoke: func(c *Coordination) { c.RevokeSession(t.Context(), 8, testSessionID) }, otherGone: true},
		{name: "profile on the device", revoke: func(c *Coordination) { c.RevokeProfileDevice(t.Context(), 7, "tv", "profile-1") }, revoked: []string{"tv"}},
		{name: "another profile on the device", revoke: func(c *Coordination) { c.RevokeProfileDevice(t.Context(), 7, "tv", "profile-2") }},
		{name: "account", revoke: func(c *Coordination) { c.RevokeAccount(t.Context(), 7) }, revoked: []string{"tv", "phone"}},
		{name: "account except a device", revoke: func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, testSessionID, "tv") }, revoked: []string{"phone"}},
		{name: "account except no device", revoke: func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, testSessionID, "") }, revoked: []string{"tv", "phone"}},
		{name: "account except a device it does not hold", revoke: func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, testSessionID, "watch") }, revoked: []string{"tv", "phone"}},
		{name: "account except no session", revoke: func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, "", "tv") }, revoked: []string{"tv", "phone"}},
		{name: "profile", revoke: func(c *Coordination) { c.RevokeProfile(t.Context(), 7, "profile-1") }, revoked: []string{"tv", "phone"}},
		{name: "another profile", revoke: func(c *Coordination) { c.RevokeProfile(t.Context(), 7, "profile-2") }},
		{name: "profile id on another account", revoke: func(c *Coordination) { c.RevokeProfile(t.Context(), 8, "profile-1") }, otherGone: true},
		{name: "empty profile id selects nothing", revoke: func(c *Coordination) { c.RevokeProfile(t.Context(), 7, "") }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			clock := newTestClock()
			var dropped []protocol.NodeKey
			c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: clock.Now,
				OnRemoved: func(k protocol.NodeKey) { dropped = append(dropped, k) }})
			if err != nil {
				t.Fatal(err)
			}
			keys := map[string]protocol.NodeKey{"tv": testNodeKey(1), "phone": testNodeKey(2)}
			tickets := map[string]string{}
			for device, key := range keys {
				tickets[device] = enrollTestDevice(t, c, 7, device, key).Ticket
			}
			otherAccount := enrollTestDevice(t, c, 8, "tv", testNodeKey(3)).Ticket

			row.revoke(c)

			gone := map[string]bool{}
			for _, device := range row.revoked {
				gone[device] = true
			}
			for device, ticket := range tickets {
				_, _, err := c.Authorize(t.Context(), ticket)
				if gone[device] && !errors.Is(err, ErrInvalidTicket) {
					t.Errorf("%s: revoked device still authorizes: %v", device, err)
				}
				if !gone[device] && err != nil {
					t.Errorf("%s: unrelated device lost its entry: %v", device, err)
				}
			}
			if _, _, err := c.Authorize(t.Context(), otherAccount); errors.Is(err, ErrInvalidTicket) != row.otherGone {
				t.Errorf("another account's entry: err = %v, want revoked = %v", err, row.otherGone)
			}
			want := len(row.revoked)
			if row.otherGone {
				want++
			}
			if len(dropped) != want {
				t.Errorf("peer removal hook ran for %d keys, want %d", len(dropped), want)
			}
		})
	}
}

// issued names an entry a revocation test starts from: the device it is for and
// the login session that enrolled it.
type issued struct{ session, device string }

// An entry belongs to the login session that last enrolled or renewed it. A
// password change keeps an entry only when that session is the kept session and
// the device is the kept session's device. Ending one session withdraws what that
// session issued, whatever device label it named, and nothing else.
func TestCoordinationRevokeBySession(t *testing.T) {
	const kept, other = "session-kept", "session-other"
	rows := []struct {
		name     string
		enrolled []issued
		revoke   func(c *Coordination)
		// survivors are the devices of account 7 that keep their entry; the
		// rest must lose it.
		survivors []string
	}{
		{name: "password change keeps the kept session's entry on the kept device",
			enrolled:  []issued{{kept, "tv"}},
			revoke:    func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, kept, "tv") },
			survivors: []string{"tv"}},
		{name: "password change withdraws the kept device's entry made by another session",
			enrolled: []issued{{other, "tv"}},
			revoke:   func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, kept, "tv") }},
		{name: "password change withdraws the kept session's entry on another device",
			enrolled: []issued{{kept, "watch"}},
			revoke:   func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, kept, "tv") }},
		{name: "password change among several entries keeps only the one that matches both",
			enrolled:  []issued{{kept, "tv"}, {kept, "watch"}, {other, "phone"}},
			revoke:    func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, kept, "tv") },
			survivors: []string{"tv"}},
		{name: "password change with no kept device withdraws every entry",
			enrolled: []issued{{kept, "tv"}},
			revoke:   func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, kept, "") }},
		{name: "password change with no kept session withdraws every entry",
			enrolled: []issued{{kept, "tv"}},
			revoke:   func(c *Coordination) { c.RevokeAccountExceptSession(t.Context(), 7, "", "tv") }},
		{name: "ending a session withdraws every entry it issued, under any device label",
			enrolled:  []issued{{other, "tv"}, {other, "phone"}, {kept, "watch"}},
			revoke:    func(c *Coordination) { c.RevokeSession(t.Context(), 7, other) },
			survivors: []string{"watch"}},
		{name: "ending a session that issued nothing withdraws nothing",
			enrolled:  []issued{{kept, "tv"}},
			revoke:    func(c *Coordination) { c.RevokeSession(t.Context(), 7, other) },
			survivors: []string{"tv"}},
		{name: "ending a session leaves the device's entry that a newer session renewed",
			enrolled:  []issued{{other, "tv"}, {kept, "tv"}},
			revoke:    func(c *Coordination) { c.RevokeSession(t.Context(), 7, other) },
			survivors: []string{"tv"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			clock := newTestClock()
			var dropped []protocol.NodeKey
			c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: clock.Now,
				OnRemoved: func(k protocol.NodeKey) { dropped = append(dropped, k) }})
			if err != nil {
				t.Fatal(err)
			}
			// The later of two entries for one device replaces the earlier.
			tickets := map[string]string{}
			for i, e := range row.enrolled {
				tickets[e.device] = enrollTestDeviceSession(t, c, 7, e.session, e.device, testNodeKey(byte(i+1))).Ticket
			}
			otherAccount := enrollTestDeviceSession(t, c, 8, kept, "tv", testNodeKey(100)).Ticket
			dropped = nil // a replacement above also reports the key it dropped

			row.revoke(c)

			alive := map[string]bool{}
			for _, device := range row.survivors {
				alive[device] = true
			}
			for device, ticket := range tickets {
				_, _, err := c.Authorize(t.Context(), ticket)
				if alive[device] && err != nil {
					t.Errorf("%s: entry that must stay was withdrawn: %v", device, err)
				}
				if !alive[device] && !errors.Is(err, ErrInvalidTicket) {
					t.Errorf("%s: entry that must go still authorizes: %v", device, err)
				}
			}
			if _, _, err := c.Authorize(t.Context(), otherAccount); err != nil {
				t.Errorf("another account's entry was withdrawn: %v", err)
			}
			if want := len(tickets) - len(row.survivors); len(dropped) != want {
				t.Errorf("peer removal hook ran for %d keys, want %d", len(dropped), want)
			}
		})
	}
}

// An enrollment without its issuing login session is refused and leaves no entry:
// no revocation could select it.
func TestEnrollNeedsTheIssuingSession(t *testing.T) {
	clock := newTestClock()
	store := newMemoryStore()
	c := newTestCoordination(t, store, clock)

	_, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "automatic"})

	if err == nil {
		t.Fatal("Enroll with no session succeeded")
	}
	if _, err := store.Device(t.Context(), deviceRef(7, "tv"), clock.Now()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("entry after the refused enrollment: %v, want ErrNotFound", err)
	}
}

// A password change cannot tell whom an entry belongs to when its policy has no
// readable session, so it withdraws the entry.
func TestRevokeAccountExceptSessionWithdrawsEntriesWithNoReadableOwner(t *testing.T) {
	for name, policy := range map[string]string{
		"policy is not JSON":      "not json",
		"policy names no session": `{"connection_path":"automatic","profile_id":"profile-1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			clock := newTestClock()
			store := newMemoryStore()
			c := newTestCoordination(t, store, clock)
			ref := deviceRef(7, "tv")
			if _, err := store.PutDevice(t.Context(), server.Device{Ref: ref, NodeKey: testNodeKey(1), Policy: []byte(policy), LeaseExpires: clock.Now().Add(enrollmentLease)}, maxDirectDevicesPerAccount, clock.Now()); err != nil {
				t.Fatal(err)
			}

			c.RevokeAccountExceptSession(t.Context(), 7, testSessionID, "tv")

			if _, err := store.Device(t.Context(), ref, clock.Now()); !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("entry with no readable owner after the password change: %v, want ErrNotFound", err)
			}
		})
	}
}

// afterListStore runs after once, right after the first ListDevices answers: the
// window in which another request changes an entry a revocation has listed.
type afterListStore struct {
	CoordinationStore
	after func()
}

func (s *afterListStore) ListDevices(ctx context.Context, account server.AccountID, now time.Time) ([]server.Device, error) {
	devices, err := s.CoordinationStore.ListDevices(ctx, account, now)
	if f := s.after; f != nil {
		s.after = nil
		f()
	}
	return devices, err
}

// A revocation withdraws the entry it listed only while that entry is unchanged,
// and judges an entry that changed again against the same rule.
func TestCoordinationRevokeSessionReevaluatesAChangedEntry(t *testing.T) {
	const ending, surviving = "session-ending", "session-surviving"
	key := testNodeKey(1)
	t.Run("the audit marker flips: the ending session still owns the entry and it is withdrawn", func(t *testing.T) {
		clock := newTestClock()
		store := &afterListStore{CoordinationStore: newMemoryStore()}
		c := newTestCoordination(t, store, clock)
		ticket := enrollTestDeviceSession(t, c, 7, ending, "tv", key).Ticket
		listed := devicePolicy{ConnectionPath: "automatic", ProfileID: "profile-1", SessionID: ending}
		audited := listed
		audited.Audited = true
		store.after = func() {
			if swapped, err := store.SwapDevicePolicy(t.Context(), deviceRef(7, "tv"), key, marshalPolicy(listed), marshalPolicy(audited), clock.Now()); err != nil || !swapped {
				t.Errorf("SwapDevicePolicy = %v, %v; want a write", swapped, err)
			}
		}

		c.RevokeSession(t.Context(), 7, ending)

		if _, _, err := c.Authorize(t.Context(), ticket); !errors.Is(err, ErrInvalidTicket) {
			t.Fatalf("entry the ending session still owns: %v, want invalid", err)
		}
	})
	t.Run("another session renews the entry: it stays", func(t *testing.T) {
		clock := newTestClock()
		store := &afterListStore{CoordinationStore: newMemoryStore()}
		c := newTestCoordination(t, store, clock)
		enrollTestDeviceSession(t, c, 7, ending, "tv", key)
		var renewed string
		store.after = func() { renewed = enrollTestDeviceSession(t, c, 7, surviving, "tv", key).Ticket }

		c.RevokeSession(t.Context(), 7, ending)

		if _, _, err := c.Authorize(t.Context(), renewed); err != nil {
			t.Fatalf("entry a surviving session renewed after the listing: %v", err)
		}
	})
	t.Run("password change: the kept session renews an entry listed as another session's: it stays", func(t *testing.T) {
		clock := newTestClock()
		store := &afterListStore{CoordinationStore: newMemoryStore()}
		c := newTestCoordination(t, store, clock)
		enrollTestDeviceSession(t, c, 7, ending, "tv", key)
		var renewed string
		store.after = func() { renewed = enrollTestDeviceSession(t, c, 7, surviving, "tv", key).Ticket }

		c.RevokeAccountExceptSession(t.Context(), 7, surviving, "tv")

		if _, _, err := c.Authorize(t.Context(), renewed); err != nil {
			t.Fatalf("entry the kept session renewed after the listing: %v", err)
		}
	})
}

// churnStore changes the live entry just before each conditional delete, so
// the policy the delete names never matches; deleteErr fails the delete instead.
type churnStore struct {
	CoordinationStore
	deletes   int
	deleteErr error
}

func (s *churnStore) DeleteDeviceIf(ctx context.Context, ref server.DeviceRef, nodeKey protocol.NodeKey, policy []byte, now time.Time) (bool, error) {
	s.deletes++
	if s.deleteErr != nil {
		return false, s.deleteErr
	}
	live, err := s.Device(ctx, ref, now)
	if err != nil {
		return false, err
	}
	var p devicePolicy
	if err := json.Unmarshal(live.Policy, &p); err != nil {
		return false, err
	}
	changed := p
	changed.Audited = !p.Audited
	if _, err := s.SwapDevicePolicy(ctx, ref, nodeKey, live.Policy, marshalPolicy(changed), now); err != nil {
		return false, err
	}
	return s.CoordinationStore.DeleteDeviceIf(ctx, ref, nodeKey, policy, now)
}

// A revocation that keeps finding its entry changed stops after a bounded number
// of tries and logs once; the entry then lapses at its lease end. A failed delete
// is logged once with its reason.
func TestCoordinationRevokeSessionGivesUpAndLogsOnce(t *testing.T) {
	rows := []struct {
		name       string
		deleteErr  error
		wantReason string
		wantTries  int
	}{
		{name: "the entry keeps changing", wantReason: "reason=entry_changing", wantTries: revokeAttempts},
		{name: "the delete fails", deleteErr: server.ErrUnavailable, wantReason: "reason=state_store_unavailable", wantTries: 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			clock := newTestClock()
			store := &churnStore{CoordinationStore: newMemoryStore(), deleteErr: row.deleteErr}
			var logs bytes.Buffer
			c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: clock.Now, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
			if err != nil {
				t.Fatal(err)
			}
			enrollTestDeviceSession(t, c, 7, "session-ending", "tv", testNodeKey(1))

			c.RevokeSession(t.Context(), 7, "session-ending")

			if store.deletes != row.wantTries {
				t.Errorf("conditional deletes = %d, want %d", store.deletes, row.wantTries)
			}
			if got := strings.Count(logs.String(), "direct connections: revocation failed"); got != 1 {
				t.Fatalf("revocation failure logged %d times, want once:\n%s", got, logs.String())
			}
			if !strings.Contains(logs.String(), row.wantReason) {
				t.Fatalf("log lacks %q:\n%s", row.wantReason, logs.String())
			}
		})
	}
}

// A store failure does not fail the event: the entry lapses at its lease end.
func TestCoordinationRevokeStoreFailureIsLoggedOnce(t *testing.T) {
	clock := newTestClock()
	store := &outageStore{CoordinationStore: newMemoryStore()}
	var logs bytes.Buffer
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: clock.Now,
		Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []string{"tv", "phone"} {
		enrollTestDevice(t, c, 7, device, testNodeKey(byte(len(device))))
	}
	store.fail = true
	c.RevokeAccount(t.Context(), 7)
	if got := strings.Count(logs.String(), "direct connections: revocation failed"); got != 1 {
		t.Fatalf("revocation failure logged %d times, want once:\n%s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "reason=state_store_unavailable") {
		t.Fatalf("log lacks the reason:\n%s", logs.String())
	}
}

// The ticket expiry, the reported expires_at and the stored lease end are the
// same instant even when the clock has a fractional second.
func TestEnrollmentExpiryAgreesToTheSecond(t *testing.T) {
	clock := newTestClock()
	clock.Advance(700 * time.Millisecond)
	store := newMemoryStore()
	c := newTestCoordination(t, store, clock)

	enrollment := enrollTestDevice(t, c, 7, "tv", testNodeKey(1))

	principal, device, err := c.Authorize(t.Context(), enrollment.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if !principal.Expires.Equal(enrollment.ExpiresAt) || !device.LeaseExpires.Equal(enrollment.ExpiresAt) {
		t.Fatalf("ticket expiry %v, reported expiry %v, stored lease end %v; want one instant", principal.Expires, enrollment.ExpiresAt, device.LeaseExpires)
	}
}

// CapabilityHost reads no settings: it answers from the values the service last
// read, so the coordination requests of every enrolled device cost no settings
// read. Before the first read it says so. A disable is visible at once, while
// the engine is still stopping, and the engine's state is reported apart.
func TestCapabilityHostReadsNoSettings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{
			"direct_connections.enabled":      "true",
			"direct_connections.stun_servers": "stun1.example.org:3478, stun2.example.org:3478",
		}}}
		cfg := testConfig(store.memorySettings)
		cfg.Settings = store
		cfg.SecretKey = bytes.Repeat([]byte{0x41}, 32)
		cfg.Tuning, _ = tuning.Parse(func(string) string { return "" })
		cfg.recheckInterval = -1
		var mu sync.Mutex
		var events []string
		stopStarted, release := make(chan struct{}), make(chan struct{})
		cfg.newEngine = func(_ context.Context, c engine.Config) (engineHandle, error) {
			// Close holds the engine in its stop until the test releases it.
			return &lifecycleEngine{port: c.Port, changed: make(chan struct{}, 1), events: &events, eventsMu: &mu, id: 1, closeStarted: stopStarted, closeRelease: release}, nil
		}
		svc := New(context.Background(), cfg)
		releaseStop := sync.OnceFunc(func() { close(release) })
		defer shutdownService(t, svc)
		defer releaseStop()

		if _, err := svc.CapabilityHost(t.Context()); !errors.Is(err, ErrSettingsNotRead) {
			t.Fatalf("CapabilityHost before any read = %v, want an error wrapping ErrSettingsNotRead", err)
		}
		if err := svc.SettingsChanged(t.Context()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := store.reads.Load(); n != 1 {
			t.Fatalf("settings reads after the first read = %d, want 1", n)
		}
		wantServers := []string{"stun1.example.org:3478", "stun2.example.org:3478"}
		for range 5 {
			host, err := svc.CapabilityHost(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !host.Enabled || !host.EngineRunning || !slices.Equal(host.STUNServers, wantServers) {
				t.Fatalf("CapabilityHost = %+v, want enabled, the engine running and servers %q", host, wantServers)
			}
		}
		if n := store.reads.Load(); n != 1 {
			t.Fatalf("settings reads after five CapabilityHost calls = %d, want 1", n)
		}

		// The service reads the disable and stops the engine, which does not
		// finish yet. The capability follows the toggle at once.
		svc.setEngineSettings(settings{enabled: false, stunServers: store.values["direct_connections.stun_servers"]}, true)
		<-stopStarted
		host, err := svc.CapabilityHost(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if host.Enabled || !host.EngineRunning {
			t.Fatalf("CapabilityHost while the engine stops = %+v, want disabled with the engine still running", host)
		}
		releaseStop()
		synctest.Wait()
		if host, err = svc.CapabilityHost(t.Context()); err != nil || host.Enabled || host.EngineRunning || host.Available {
			t.Fatalf("CapabilityHost after the engine stopped = %+v, %v, want disabled with the engine stopped", host, err)
		}
		if n := store.reads.Load(); n != 1 {
			t.Fatalf("settings reads at the end = %d, want 1", n)
		}
	})
}
