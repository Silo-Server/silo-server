package directconn

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/darthShadow/shunpike/engine"
)

// joinFixture is a service whose first check is running and blocked in its
// probe, over a store that counts reads and fails them on demand. The check has
// published no snapshot yet. The nth probe reports the address 192.0.2.n:3478,
// so a snapshot shows which check produced it.
type joinFixture struct {
	svc     *Service
	store   *failingSettings
	probes  *atomic.Int32
	release func()
}

// newJoinFixture must run inside a synctest bubble. The check it starts is
// released by the returned function or by the end of the bubble.
func newJoinFixture(t *testing.T) *joinFixture {
	t.Helper()
	store := &failingSettings{memorySettings: &memorySettings{values: map[string]string{"direct_connections.enabled": "true", "direct_connections.stun_servers": "first.example:3478"}}}
	cfg := testConfig(store.memorySettings)
	cfg.Settings = store
	release := make(chan struct{})
	probes := new(atomic.Int32)
	cfg.NAT = func(ctx context.Context, _ *slog.Logger, _ []string) (engine.NATReport, error) {
		n := probes.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return engine.NATReport{UDP: true, GlobalV4: probeAddress(n)}, ctx.Err()
	}
	svc := New(t.Context(), cfg)
	if err := svc.SettingsChanged(t.Context()); err != nil {
		t.Fatal(err)
	}
	synctest.Wait()
	if got := probes.Load(); got != 1 {
		t.Fatalf("first check probes=%d, want 1 running", got)
	}
	var once atomic.Bool
	f := &joinFixture{svc: svc, store: store, probes: probes}
	f.release = func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(f.release)
	return f
}

// probeAddress is the address the nth probe observes.
func probeAddress(n int32) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, byte(n)}), 3478)
}

// observedBy reports the address a snapshot recorded from its probe.
func observedBy(t *testing.T, st Status) string {
	t.Helper()
	return feature(t, st.Features, "nat_mapping").Detail
}

type statusResult struct {
	status Status
	err    error
}

// statusAsync starts Status in its own goroutine and returns its snapshot and
// error.
func (f *joinFixture) statusAsync(t *testing.T, refresh bool) <-chan statusResult {
	t.Helper()
	done := make(chan statusResult, 1)
	go func() {
		st, err := f.svc.Status(t.Context(), refresh)
		done <- statusResult{status: st, err: err}
	}()
	return done
}

// A Status call without a snapshot joins the check that is running and returns
// that check's snapshot, not the follow-up's. With no pending read it reads
// nothing. With a pending read it reads first: a failed read is ignored and the
// call still returns the running check; a successful read of different values
// schedules one follow-up behind that check, which the next Status returns.
func TestStatusWithoutSnapshotJoinsRunningCheck(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pending    bool // an earlier read failed
		readFails  bool // the read this call makes fails
		change     bool // the stored STUN server differs when that read succeeds
		wantReads  int32
		wantProbes int32 // probes run once the follow-up, if any, has finished
	}{
		{name: "no pending read", wantReads: 0, wantProbes: 1},
		{name: "pending read fails", pending: true, readFails: true, wantReads: 1, wantProbes: 1},
		{name: "pending read unchanged", pending: true, wantReads: 1, wantProbes: 1},
		{name: "pending read changed", pending: true, change: true, wantReads: 1, wantProbes: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newJoinFixture(t)
				if tc.pending {
					failSettingsChange(t, f.svc, f.store, 1)
				}
				if tc.change {
					f.store.set("direct_connections.stun_servers", "second.example:3478")
				}
				if tc.readFails {
					f.store.failReads.Store(1)
				}
				base := f.store.reads.Load()
				call := f.statusAsync(t, false)
				synctest.Wait()
				select {
				case res := <-call:
					t.Fatalf("status returned %v before the running check finished", res.err)
				default:
				}
				if got := f.store.reads.Load() - base; got != tc.wantReads {
					t.Fatalf("store reads=%d, want %d", got, tc.wantReads)
				}
				f.release()
				res := <-call
				if res.err != nil {
					t.Fatalf("status error = %v, want the running check's result", res.err)
				}
				if got, want := observedBy(t, res.status), probeAddress(1).String(); got != want {
					t.Fatalf("status snapshot observed %q, want %q from the first check", got, want)
				}
				synctest.Wait()
				if got := f.probes.Load(); got != tc.wantProbes {
					t.Fatalf("probe calls=%d, want %d", got, tc.wantProbes)
				}
				// The next Status returns the newest completed check: the follow-up
				// when one ran, else the first check again.
				next, err := f.svc.Status(t.Context(), false)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := observedBy(t, next), probeAddress(tc.wantProbes).String(); got != want {
					t.Fatalf("following status observed %q, want %q from probe %d", got, want, tc.wantProbes)
				}
			})
		})
	}
}

// A refresh that joins a running check because an earlier read failed reads
// settings first. When that read fails too, it returns the read error at once
// and does not wait for the running check, as docs/architecture/direct-connections.md
// says. The running check is unaffected and schedules no follow-up.
func TestRefreshJoiningRunningCheckReturnsFailedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newJoinFixture(t)
		failSettingsChange(t, f.svc, f.store, 1)
		f.store.failReads.Store(1)
		call := f.statusAsync(t, true)
		synctest.Wait()
		select {
		case res := <-call:
			if !errors.Is(res.err, errSettingsRead) {
				t.Fatalf("refresh error = %v, want the settings read error", res.err)
			}
			if len(res.status.Features) != 0 {
				t.Fatalf("refresh returned a snapshot with its read error: %+v", res.status)
			}
		default:
			t.Fatal("refresh still waits for the running check after its own read failed, want the read error")
		}
		f.release()
		synctest.Wait()
		if got := f.probes.Load(); got != 1 {
			t.Fatalf("probe calls=%d, want 1: a failed read schedules no follow-up", got)
		}
	})
}
