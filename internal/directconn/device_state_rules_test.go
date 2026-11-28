package directconn

import (
	"context"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
)

// A replica that has not read its settings yet does not know whether direct
// connections are on. It must not answer "not enrolled" for a direct_only
// device another replica enrolled: the playback handler would then plan that
// device as a regular one and hand it a proxy URL. It either reads the store
// or reports that it cannot answer.
func TestDeviceBeforeTheFirstSettingsReadKeepsDirectOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := newHostFixture(t, "enabled")
		defer host.cleanup()
		if _, err := host.coord.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: testNodeKey(1), ConnectionPath: "direct_only"}); err != nil {
			t.Fatal(err)
		}
		if d, ok, err := host.svc.Device(t.Context(), 7, "tv"); err != nil || !ok || d.Policy != "direct_only" {
			t.Fatalf("fixture: Device on the replica that read its settings = %+v, ok %v, %v; want direct_only", d, ok, err)
		}

		// A second replica over the same store, before its first settings read.
		fresh := newLifecycleFixture(t, "enabled")
		defer fresh.cleanup()
		coord, err := NewCoordination(CoordinationConfig{Store: host.store, AuthSecret: testAuthSecret, Clock: time.Now})
		if err != nil {
			t.Fatal(err)
		}
		fresh.svc.SetCoordination(coord)
		d, ok, err := fresh.svc.Device(t.Context(), 7, "tv")
		if err == nil && (!ok || d.Policy != "direct_only") {
			t.Fatalf("Device before the first settings read = %+v, ok %v, no error; a direct_only device answered as not enrolled", d, ok)
		}
	})
}

// The path tracker's confirmations belong to the engine that published them.
// After the engine restarts, a device is not API-ready until the new engine
// confirms it, even when the new engine sees the same direct path at once.
func TestEngineRestartDropsTheOldEnginesConfirmations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := newHostFixture(t, "enabled")
		defer host.cleanup()
		f := host.lifecycleFixture
		principal := host.enrollAndRegister(7, "tv", testNodeKey(1), "automatic")
		endpoint := netip.MustParseAddrPort("203.0.113.9:4000")
		setPath := func(e *lifecycleEngine) {
			e.stateMu.Lock()
			e.paths = map[protocol.NodeKey]engine.PathStatus{principal.NodeKey: {Class: pathClassDirect, Endpoint: endpoint}}
			e.stateMu.Unlock()
		}
		setPath(f.engines[0])
		_, paths := f.svc.coordination()
		paths.setConfirmed(principal.NodeKey, true)
		if d, ok, err := f.svc.Device(t.Context(), 7, "tv"); err != nil || !ok || !d.APIReady {
			t.Fatalf("fixture: Device before the restart = %+v, ok %v, %v; want API-ready", d, ok, err)
		}

		// Port drift restarts the engine, as in the drift restart lifecycle case.
		f.engines[0].port++
		f.engines[0].changed <- struct{}{}
		synctest.Wait()
		if f.calls != 2 || len(f.engines) != 2 {
			t.Fatalf("fixture: %d engines built, want the restart to build a second", f.calls)
		}
		setPath(f.engines[1])
		if d, ok, err := f.svc.Device(t.Context(), 7, "tv"); err != nil || !ok || d.APIReady {
			t.Fatalf("Device after the engine restart = %+v, ok %v, %v; want not API-ready until the new engine confirms the device", d, ok, err)
		}
	})
}

// raceBeforeWriteStore runs before once, just before the next device write, so
// a test can land another enrollment between an Enroll's read and its write.
type raceBeforeWriteStore struct {
	CoordinationStore
	before func()
}

func (s *raceBeforeWriteStore) PutDeviceReplacing(ctx context.Context, d server.Device, maxPerAccount int, now time.Time) (protocol.NodeKey, error) {
	if before := s.before; before != nil {
		s.before = nil
		before()
	}
	return s.CoordinationStore.PutDeviceReplacing(ctx, d, maxPerAccount, now) //nolint:wrapcheck // A test double passes the store's result through.
}

// A renewal whose write replaces a key another enrollment stored after the
// renewal's read is a key replacement, and a key replacement is audited
// (TestEnrollmentAudit). The audit marker read before the write does not carry
// over a key the write displaced.
func TestEnrollmentAuditsARenewalThatReplacedAConcurrentKey(t *testing.T) {
	var audited []auditedEnrollment
	store := &raceBeforeWriteStore{CoordinationStore: newMemoryStore()}
	c, err := NewCoordination(CoordinationConfig{Store: store, AuthSecret: testAuthSecret, Clock: newTestClock().Now,
		Audit: func(_ context.Context, device string, key protocol.NodeKey) error {
			audited = append(audited, auditedEnrollment{device, key})
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	enroll := func(key protocol.NodeKey) {
		t.Helper()
		if _, err := c.Enroll(t.Context(), EnrollRequest{UserID: 7, ProfileID: "profile-1", SessionID: testSessionID, DeviceID: "tv", NodeKey: key, ConnectionPath: "automatic"}); err != nil {
			t.Fatal(err)
		}
	}
	enroll(testNodeKey(1))
	// The renewal of key 1 reads key 1, audited; key 2 lands before its write.
	store.before = func() { enroll(testNodeKey(2)) }
	enroll(testNodeKey(1))

	stored, err := store.Device(t.Context(), deviceRef(7, "tv"), c.clock())
	if err != nil || stored.NodeKey != testNodeKey(1) {
		t.Fatalf("fixture: stored key = %v, %v; want key 1 written over key 2", stored.NodeKey, err)
	}
	want := []auditedEnrollment{{"tv", testNodeKey(1)}, {"tv", testNodeKey(2)}, {"tv", testNodeKey(1)}}
	if len(audited) != len(want) {
		t.Fatalf("audit calls = %v; the write that replaced key 2 with key 1 was not audited, want %v", audited, want)
	}
}
