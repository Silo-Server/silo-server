// Package streamtelemetrytest holds helpers shared by the per-family telemetry
// tests. It is test-only support and is never imported by production code.
package streamtelemetrytest

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// settleTimeout bounds the wait. It is generous because the thing being waited
// on is a handler goroutine finishing, which under a loaded `go test ./...` can
// be descheduled for a surprisingly long time — and a helper that gives up early
// would reintroduce exactly the flake it exists to remove.
const settleTimeout = 5 * time.Second

// SweepSettled returns a snapshot taken once no observation is still in flight.
//
// Byte accounting is folded on the SERVER goroutine, after the writer finishes a
// slice — but the client's read can complete the moment the kernel has delivered
// the last byte, which is strictly earlier. A test that sweeps as soon as its
// HTTP call returns is therefore racing the handler, and will intermittently see
// an open observation with zero bytes on a request that transferred fine. That is
// the monitoring contract working correctly ("in flight, not yet accounted"), not
// a defect, so the fix belongs in the test rather than in the registry.
//
// Use this wherever an assertion depends on a completed request's accounting.
// Where the assertion is about state DURING a request, call Sweep directly —
// this helper would wait for the very thing being observed.
func SweepSettled(t testing.TB, registry *streamtelemetry.Registry) streamtelemetry.Snapshot {
	t.Helper()
	deadline := time.Now().Add(settleTimeout)
	for {
		snapshot := registry.Sweep()
		if !hasOpenObservations(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("observations still in flight after %s: %+v", settleTimeout, snapshot.Sessions)
		}
		time.Sleep(time.Millisecond)
	}
}

func hasOpenObservations(snapshot streamtelemetry.Snapshot) bool {
	for _, session := range snapshot.Sessions {
		if session.OpenObservations > 0 {
			return true
		}
	}
	for _, transfer := range snapshot.Transfers {
		if transfer.OpenObservations > 0 {
			return true
		}
	}
	return false
}
