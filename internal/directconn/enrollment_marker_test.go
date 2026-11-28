package directconn

import (
	"context"
	"errors"
	"testing"

	"github.com/darthShadow/shunpike/coordination/protocol"
)

// A renewal whose write replaces a key that another enrollment stored after
// the renewal's read stores an entry for a key that has no audit row yet. Until
// its own audit commits, no other request may take that entry for an audited
// one: a request that renews the key in that window, while the first request's
// audit fails and its withdrawal finds the entry changed, would otherwise leave
// the key without any audit row.
func TestAReplacingRenewalsEntryIsNotAuditedUntilItsAuditCommits(t *testing.T) {
	var audited []auditedEnrollment
	// duringAudit runs inside the third audit, the replacing renewal's own.
	var duringAudit func() error
	store := &raceBeforeWriteStore{CoordinationStore: newMemoryStore()}
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: newTestClock().Now,
		Audit: func(_ context.Context, device string, key protocol.NodeKey) error {
			audited = append(audited, auditedEnrollment{device, key})
			if len(audited) == 3 && duringAudit != nil {
				return duringAudit()
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(key protocol.NodeKey) error {
		_, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: key, ConnectionPath: "automatic"})
		return err
	}
	if err := enroll(testNodeKey(1)); err != nil {
		t.Fatal(err)
	}
	// The renewal of key 1 reads key 1, audited; key 2 lands before its write.
	store.before = func() {
		if err := enroll(testNodeKey(2)); err != nil {
			t.Error(err)
		}
	}
	// While that renewal's audit runs (the third audit call: key 1, key 2, then
	// this one), another request renews key 1; the audit then fails.
	duringAudit = func() error {
		if err := enroll(testNodeKey(1)); err != nil {
			t.Error(err)
		}
		return errors.New("audit store unavailable")
	}

	if err := enroll(testNodeKey(1)); err == nil {
		t.Fatal("fixture: the replacing renewal succeeded; want its audit to fail")
	}

	// Key 1, key 2, key 1 over key 2 (fails), and the renewal inside that audit.
	want := []auditedEnrollment{{"tv", testNodeKey(1)}, {"tv", testNodeKey(2)}, {"tv", testNodeKey(1)}, {"tv", testNodeKey(1)}}
	if len(audited) != len(want) {
		t.Fatalf("audit calls = %v; the renewal inside the replacing request's audit took its entry for an audited one, want %v", audited, want)
	}
}
