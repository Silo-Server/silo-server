package streamtelemetry

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/httpstream"
)

type observationContextKey struct{}

type Observation struct {
	id       string
	registry *Registry
	route    MediaRoute
	Capture  CaptureSet

	bytesAccepted atomic.Int64
	cut           atomic.Bool

	mu            sync.Mutex
	attachment    *Attachment
	target        observationTarget
	firstWriteErr error
	released      bool
	countingOnly  bool
	reserved      bool
}

type observationTarget struct {
	session  *logicalSession
	transfer *transfer
}

// Observing reports whether this request is being observed, so a caller can
// skip building an Attachment — and any verification work it needs — when
// telemetry is off or the route is not enrolled.
func Observing(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	obs, _ := ctx.Value(observationContextKey{}).(*Observation)
	return obs != nil && obs.registry != nil
}

func (o *Observation) AddBytes(n int64) {
	if o != nil && n > 0 {
		o.bytesAccepted.Add(n)
	}
}

func (o *Observation) BytesAccepted() int64 {
	if o == nil {
		return 0
	}
	return o.bytesAccepted.Load()
}

func (o *Observation) recordWriteError(err error) {
	if o == nil || err == nil {
		return
	}
	o.mu.Lock()
	if o.firstWriteErr == nil {
		o.firstWriteErr = err
	}
	o.mu.Unlock()
}

func (o *Observation) outcome(ctxErr error, completed bool) httpstream.StreamOutcome {
	if !completed {
		return OutcomeUnknown
	}
	o.mu.Lock()
	err := o.firstWriteErr
	o.mu.Unlock()
	return httpstream.ClassifyOutcome(err, ctxErr)
}

func Attach(ctx context.Context, attachment Attachment) {
	if ctx == nil {
		return
	}
	obs, _ := ctx.Value(observationContextKey{}).(*Observation)
	if obs == nil || obs.registry == nil {
		return
	}
	obs.registry.attach(obs, attachment)
}

func newObservation(registry *Registry, route MediaRoute, capture CaptureSet) *Observation {
	return &Observation{
		id:       uuid.NewString(),
		registry: registry,
		route:    route,
		Capture:  capture,
	}
}
