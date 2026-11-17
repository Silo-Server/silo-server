package directconn

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
)

type auditedEnrollment struct {
	device string
	key    protocol.NodeKey
}

// The enrollment audit runs once per first enrollment of a node key for a
// device: not on a renewal of the same key, again on a key replacement, and an
// audit that cannot be written withdraws the entry it audited.
func TestEnrollmentAudit(t *testing.T) {
	var audited []auditedEnrollment
	var auditErr error
	c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: newTestClock().Now,
		Audit: func(_ context.Context, device string, key protocol.NodeKey) error {
			audited = append(audited, auditedEnrollment{device, key})
			return auditErr
		}})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(device string, key protocol.NodeKey) (Enrollment, error) {
		return c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: device, NodeKey: key, ConnectionPath: "automatic"})
	}
	requireAudited := func(step string, want ...auditedEnrollment) {
		t.Helper()
		if len(audited) != len(want) {
			t.Fatalf("%s: %d audit calls %v, want %d", step, len(audited), audited, len(want))
		}
		for i := range want {
			if audited[i] != want[i] {
				t.Fatalf("%s: audit call %d = %v, want %v", step, i, audited[i], want[i])
			}
		}
	}

	first, err := enroll("tv", testNodeKey(1))
	if err != nil {
		t.Fatal(err)
	}
	requireAudited("first enrollment", auditedEnrollment{"tv", testNodeKey(1)})

	if _, err := enroll("tv", testNodeKey(1)); err != nil {
		t.Fatal(err)
	}
	requireAudited("renewal of the same key", auditedEnrollment{"tv", testNodeKey(1)})

	if _, err := enroll("tv", testNodeKey(2)); err != nil {
		t.Fatal(err)
	}
	requireAudited("a new key on an enrolled device", auditedEnrollment{"tv", testNodeKey(1)}, auditedEnrollment{"tv", testNodeKey(2)})
	if _, _, err := c.Authorize(t.Context(), first.Ticket); !errors.Is(err, ErrInvalidTicket) {
		t.Fatalf("ticket of the replaced key: %v, want invalid", err)
	}

	auditErr = errors.New("audit store down")
	got, err := enroll("phone", testNodeKey(3))
	if !errors.Is(err, auditErr) {
		t.Fatalf("enrollment with a failed audit: err = %v, want it to wrap the audit error", err)
	}
	if got.Ticket != "" {
		t.Fatal("a ticket was issued for an enrollment whose audit failed")
	}
	requireAudited("failed audit", auditedEnrollment{"tv", testNodeKey(1)}, auditedEnrollment{"tv", testNodeKey(2)}, auditedEnrollment{"phone", testNodeKey(3)})
	if _, err := c.cfg.Store.Device(t.Context(), deviceRef(7, "phone"), c.clock()); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("entry after a failed audit: %v, want not found", err)
	}

	// A renewal needs no audit row, so a failing audit does not touch it.
	if _, err := enroll("tv", testNodeKey(2)); err != nil {
		t.Fatalf("renewal with a failing audit: %v", err)
	}
}

// Without an audit, enrollment behaves as before.
func TestEnrollmentWithoutAudit(t *testing.T) {
	c, err := NewCoordination(CoordinationConfig{Store: newMemoryStore(), AuthSecret: testAuthSecret, Clock: newTestClock().Now})
	if err != nil {
		t.Fatal(err)
	}
	enrollTestDevice(t, c, 7, "tv", testNodeKey(1))
}

// An audit that fails withdraws only the entry its own request wrote. A
// replacement key that another request enrolled and audited while the first
// audit was pending keeps its entry and its ticket.
func TestEnrollmentAuditFailureKeepsLaterReplacement(t *testing.T) {
	var c *Coordination
	var replacement Enrollment
	var replacementErr error
	auditFailure := errors.New("synthetic audit failure")
	audit := func(ctx context.Context, device string, key protocol.NodeKey) error {
		if key != testNodeKey(1) {
			return nil
		}
		// Another replica replaces the key and completes its audit while this
		// enrollment's audit transaction is still open.
		replacement, replacementErr = c.Enroll(ctx, EnrollRequest{UserID: 7, ProfileID: "profile-2", SessionID: testSessionID, DeviceID: device, NodeKey: testNodeKey(2), ConnectionPath: "automatic"})
		return auditFailure
	}
	var err error
	c, err = NewCoordination(CoordinationConfig{Store: NewCoordinationStore(nil, ""), AuthSecret: testAuthSecret, Clock: newTestClock().Now, Audit: audit})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "automatic"}); !errors.Is(err, auditFailure) {
		t.Fatalf("first enrollment: err = %v, want the audit failure", err)
	}
	if replacementErr != nil {
		t.Fatalf("replacement enrollment: %v", replacementErr)
	}
	if _, _, err := c.Authorize(t.Context(), replacement.Ticket); err != nil {
		t.Fatalf("the audited replacement's ticket after the earlier audit failed: %v, want it valid", err)
	}
}

// A renewal of a key whose first audit has not committed is not a renewal of an
// audited entry: it runs the audit itself, so no ticket for the key is issued
// before an audit row for it has committed.
func TestEnrollmentRenewalDuringPendingAudit(t *testing.T) {
	auditFailure := errors.New("synthetic audit failure")
	for _, tc := range []struct {
		name string
		// renewalAuditFails decides the outcome of the renewal's own audit.
		renewalAuditFails bool
	}{
		{"renewal audit fails", true},
		{"renewal audit commits", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c *Coordination
			var renewal Enrollment
			var renewalErr error
			committed, calls := 0, 0
			audit := func(ctx context.Context, device string, key protocol.NodeKey) error {
				calls++
				if calls > 1 {
					// The renewal's audit.
					if tc.renewalAuditFails {
						return auditFailure
					}
					committed++
					return nil
				}
				// The first enrollment's audit transaction is open while the same
				// device renews the same key.
				renewal, renewalErr = c.Enroll(ctx, EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: device, NodeKey: key, ConnectionPath: "automatic"})
				if renewalErr == nil && committed == 0 {
					t.Error("the renewal received a ticket while no audit row for the key had committed")
				}
				return auditFailure
			}
			var err error
			c, err = NewCoordination(CoordinationConfig{Store: NewCoordinationStore(nil, ""), AuthSecret: testAuthSecret, Clock: newTestClock().Now, Audit: audit})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "automatic"}); !errors.Is(err, auditFailure) {
				t.Fatalf("first enrollment: err = %v, want the audit failure", err)
			}
			if calls != 2 {
				t.Fatalf("audit calls = %d, want 2: the first enrollment and the renewal", calls)
			}
			if tc.renewalAuditFails {
				if !errors.Is(renewalErr, auditFailure) || renewal.Ticket != "" {
					t.Fatalf("renewal with a failed audit: ticket issued = %t, err = %v; want the audit failure and no ticket", renewal.Ticket != "", renewalErr)
				}
				return
			}
			if renewalErr != nil {
				t.Fatalf("renewal with a committed audit: %v", renewalErr)
			}
			// The first request's failed audit leaves the renewal's audited entry.
			if _, _, err := c.Authorize(t.Context(), renewal.Ticket); err != nil {
				t.Fatalf("the renewal's ticket after the first audit failed: %v, want it valid", err)
			}
		})
	}
}

// An entry with this request's key that was written but never marked audited,
// as a process that stopped between the write and the audit leaves it, is
// audited by the next enrollment of that key. Once audited, renewals add no row.
func TestEnrollmentAuditsUnauditedEntry(t *testing.T) {
	clock := newTestClock()
	store := NewCoordinationStore(nil, "")
	audits := 0
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: clock.Now,
		Audit: func(context.Context, string, protocol.NodeKey) error { audits++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	policy, err := json.Marshal(devicePolicy{ConnectionPath: "automatic", ProfileID: "profile-1"})
	if err != nil {
		t.Fatal(err)
	}
	unaudited := server.Device{Ref: deviceRef(7, "tv"), NodeKey: testNodeKey(1), Policy: policy, LeaseExpires: clock.Now().Add(enrollmentLease)}
	if _, err := store.PutDevice(t.Context(), unaudited, maxDirectDevicesPerAccount, clock.Now()); err != nil {
		t.Fatal(err)
	}
	enrollTestDevice(t, c, 7, "tv", testNodeKey(1))
	if audits != 1 {
		t.Fatalf("audit calls after enrolling over an unaudited entry = %d, want 1", audits)
	}
	enrollTestDevice(t, c, 7, "tv", testNodeKey(1))
	if audits != 1 {
		t.Fatalf("audit calls after renewing the audited entry = %d, want 1", audits)
	}
}

// readFailureStore fails the next Device read once failNext is set.
type readFailureStore struct {
	CoordinationStore
	failNext bool
}

func (s *readFailureStore) Device(ctx context.Context, ref server.DeviceRef, now time.Time) (server.Device, error) {
	if s.failNext {
		s.failNext = false
		return server.Device{}, server.ErrUnavailable
	}
	return s.CoordinationStore.Device(ctx, ref, now)
}

// A stored entry that cannot be read is not a missing entry: the enrollment is
// refused as a store outage, records no row and leaves the entry as it was.
func TestEnrollmentRefusesWhenTheEntryCannotBeRead(t *testing.T) {
	store := &readFailureStore{CoordinationStore: NewCoordinationStore(nil, "")}
	audits := 0
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: newTestClock().Now,
		Audit: func(context.Context, string, protocol.NodeKey) error { audits++; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	first := enrollTestDevice(t, c, 7, "tv", testNodeKey(1))
	store.failNext = true
	renewal, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "automatic"})
	if !errors.Is(err, server.ErrUnavailable) || renewal.Ticket != "" {
		t.Fatalf("enrollment with an unreadable entry: ticket issued = %t, err = %v; want server.ErrUnavailable and no ticket", renewal.Ticket != "", err)
	}
	if audits != 1 {
		t.Fatalf("audit calls = %d, want 1: the refused enrollment records none", audits)
	}
	if _, _, err := c.Authorize(t.Context(), first.Ticket); err != nil {
		t.Fatalf("the stored entry after the refused enrollment: %v, want it unchanged", err)
	}
}
