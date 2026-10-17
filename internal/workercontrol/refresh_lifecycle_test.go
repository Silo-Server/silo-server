//go:build unix

package workercontrol

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func refreshUnavailable(request *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("unavailable")), Request: request}
}

func TestRefreshFailurePreservesActualProducer(t *testing.T) {
	for _, failure := range []string{"503", "stalled-response"} {
		t.Run(failure, func(t *testing.T) {
			f := newStopFixture(t)
			generation, pid, input := f.start(t, "work", 0)
			base := f.runtime.client.Transport
			var calls atomic.Int32
			f.runtime.client.Transport = refreshTransport(func(request *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(request.URL.Path, "/snapshot") {
					return base.RoundTrip(request)
				}
				if calls.Add(1) == 2 {
					if failure == "stalled-response" {
						<-request.Context().Done()
						return nil, request.Context().Err()
					}
					return refreshUnavailable(request), nil
				}
				return base.RoundTrip(request)
			})
			f.runtime.Run()
			deadline := time.NewTimer(4 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for {
				if syscall.Kill(pid, 0) != nil || !f.runtime.inputs.Allowed(input) || !f.runtime.workAllowed("work", generation) {
					t.Fatal("one failed snapshot retired the still-authorized producer or source")
				}
				f.runtime.mu.Lock()
				sequence := f.runtime.sequence
				f.runtime.mu.Unlock()
				// Four later signed snapshots cross the original lease deadline.
				// The producer must survive without a test-supplied keepalive.
				if sequence >= 5 {
					if f.jobs(t) != 1 || len(f.runtime.tracker.snapshot()) != 1 {
						t.Fatal("healthy producer lost its job or session registration")
					}
					return
				}
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatal("snapshot retry did not restore normal refreshes")
				}
			}
		})
	}
}

func TestRefreshOutageStillStopsActualProducerWithinLease(t *testing.T) {
	f := newStopFixture(t)
	generation, pid, input := f.start(t, "work", 0)
	base := f.runtime.client.Transport
	var unavailable atomic.Bool
	sent := make(chan time.Time, 1)
	f.runtime.client.Transport = refreshTransport(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/snapshot") {
			if unavailable.Load() {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			sent <- time.Now()
		}
		return base.RoundTrip(request)
	})
	if err := f.runtime.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	lastSent := <-sent
	unavailable.Store(true)
	f.runtime.Run()
	deadline := time.NewTimer(time.Until(lastSent.Add(2250 * time.Millisecond)))
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH && !f.runtime.inputs.Allowed(input) && !f.runtime.workAllowed("work", generation) && len(f.runtime.tracker.snapshot()) == 0 {
			if time.Since(lastSent) < 1800*time.Millisecond {
				t.Fatal("outage fixture retired work before the signed lease expired")
			}
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("sustained outage exceeded the two-second authority lease and bounded teardown")
		}
	}
}

func TestRefreshCloseCancelsSharedFlight(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	r := newRefreshFixture(t, func(_ http.ResponseWriter, request *http.Request, _ refreshChallenge, reply func()) {
		close(entered)
		<-request.Context().Done()
		close(canceled)
		// A completed late reply cannot reopen the closed authority.
		reply()
	})
	done := make(chan error, 1)
	go func() { done <- r.refresh(t.Context()) }()
	<-entered
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-canceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("runtime close left shared authority request alive")
	}
	if err := awaitRefresh(t, done); err == nil || r.workAllowed("work", 4) {
		t.Fatal("closed runtime accepted late authority")
	}
}

func TestRefreshSignedStopSnapshotRetiresActualProducer(t *testing.T) {
	f := newStopFixture(t)
	generation, pid, input := f.start(t, "work", 0)
	if err := f.store.Stop(t.Context(), 17, "work"); err != nil {
		t.Fatal(err)
	}
	if err := f.runtime.refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if syscall.Kill(pid, 0) == syscall.ESRCH && !f.runtime.inputs.Allowed(input) && !f.runtime.workAllowed("work", generation) && len(f.runtime.tracker.snapshot()) == 0 {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("successful stopped-work snapshot retained producer or source")
		}
	}
}
