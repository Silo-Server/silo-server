package artworkstore

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkmetrics"
)

type HealthState string

const (
	HealthHealthy         HealthState = "healthy"
	HealthDegraded        HealthState = "degraded"
	HealthUnavailable     HealthState = "unavailable"
	HealthEmptyRebuilding HealthState = "empty_rebuilding"
	HealthWrongMount      HealthState = "wrong_mount"
)

const (
	// Open decision 17: two consecutive probe verdicts debounce ordinary
	// transitions. Healthy probes run every 30 seconds; failures back off from
	// five seconds to five minutes.
	healthTransitionThreshold  = 2
	healthProbeHealthyInterval = 30 * time.Second
	healthProbeInitialBackoff  = 5 * time.Second
	healthProbeMaxBackoff      = 5 * time.Minute
)

var ErrBackendUnavailable = errors.New("artworkstore: backend unavailable")
var ErrWrongMount = errors.New("artworkstore: artwork mount identity is absent")
var ErrRevisionMissing = errors.New("artworkstore: authoritative artwork revision miss")
var ErrStoreIdentity = errors.New("artworkstore: reachable store identity is invalid")
var ErrStoreNotEmpty = errors.New("artworkstore: store is not empty")
var ErrRebuildUnsupported = errors.New("artworkstore: explicit rebuild is supported only for local storage")

type healthTracker struct {
	mu          sync.RWMutex
	state       HealthState
	changedAt   time.Time
	pending     HealthState
	pendingHits int
	backend     string
}

func newHealthTracker(backend string, initial HealthState) *healthTracker {
	artworkmetrics.StoreHealth(backend, string(initial), string(initial))
	return &healthTracker{state: initial, changedAt: time.Now().UTC(), backend: backend}
}

func (h *healthTracker) current() (HealthState, time.Time) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.state, h.changedAt
}

func (h *healthTracker) force(state HealthState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != state {
		artworkmetrics.StoreHealthDuration(h.backend, string(h.state), time.Since(h.changedAt))
		artworkmetrics.StoreHealth(h.backend, string(h.state), string(state))
		h.state, h.changedAt = state, time.Now().UTC()
	}
	h.pending, h.pendingHits = "", 0
}

func (h *healthTracker) observe(state HealthState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state == h.state {
		h.pending, h.pendingHits = "", 0
		return
	}
	if h.pending != state {
		h.pending, h.pendingHits = state, 1
		return
	}
	h.pendingHits++
	if h.pendingHits >= healthTransitionThreshold {
		artworkmetrics.StoreHealthDuration(h.backend, string(h.state), time.Since(h.changedAt))
		artworkmetrics.StoreHealth(h.backend, string(h.state), string(state))
		h.state, h.changedAt = state, time.Now().UTC()
		h.pending, h.pendingHits = "", 0
	}
}

// note records request-path evidence without allowing repeated client requests
// to satisfy the debounce by themselves. A probe must confirm the failure.
func (h *healthTracker) note(state HealthState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if state == h.state {
		h.pending, h.pendingHits = "", 0
		return
	}
	if h.pending != state {
		h.pending, h.pendingHits = state, 1
	}
}

func (h *healthTracker) clearPending() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending, h.pendingHits = "", 0
}

func (h *Handle) Health() (HealthState, time.Time) {
	if h == nil || h.health == nil {
		return HealthUnavailable, time.Time{}
	}
	return h.health.current()
}

func (h *Handle) ReportFailure(err error) {
	if h == nil || h.health == nil || err == nil || errors.Is(err, ErrNotFound) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrInvalidKey) {
		return
	}
	if errors.Is(err, ErrWrongMount) {
		h.health.force(HealthWrongMount)
		return
	}
	var mismatch *PinMismatchError
	if errors.As(err, &mismatch) {
		h.health.force(HealthWrongMount)
		return
	}
	if errors.Is(err, ErrRevisionMissing) {
		h.health.force(HealthDegraded)
		return
	}
	if errors.Is(err, ErrContentMismatch) || errors.Is(err, ErrNotRegularFile) {
		h.health.force(HealthDegraded)
		return
	}
	h.health.note(HealthUnavailable)
	if h.probeSignal != nil {
		select {
		case h.probeSignal <- struct{}{}:
		default:
		}
	}
}

func (h *Handle) reportProbeFailure(err error) {
	if h == nil || h.health == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return
	}
	if errors.Is(err, ErrWrongMount) || errors.Is(err, ErrStoreIdentity) {
		h.health.force(HealthWrongMount)
		return
	}
	if errors.Is(err, ErrBackendUnavailable) {
		h.health.force(HealthUnavailable)
		return
	}
	var mismatch *PinMismatchError
	if errors.As(err, &mismatch) {
		h.health.force(HealthWrongMount)
		return
	}
	h.health.observe(HealthUnavailable)
}

func (h *Handle) reportSuccess() {
	if h != nil && h.health != nil {
		h.health.clearPending()
	}
}

// ReportInventoryMissing reconciles the degraded state with durable inventory
// after repair. It never overrides a backend-wide outage or an active rebuild.
func (h *Handle) ReportInventoryMissing(count int64) {
	if h == nil || h.health == nil {
		return
	}
	state, _ := h.Health()
	if count > 0 {
		if state == HealthHealthy {
			h.health.force(HealthDegraded)
		}
		return
	}
	if state == HealthDegraded {
		h.health.force(HealthHealthy)
	}
}

// CompleteRebuild leaves the authoritative-empty state once the durable
// recovery queue drains. Protected losses keep the store degraded; otherwise
// normal delivery health resumes.
func (h *Handle) CompleteRebuild(protectedLoss bool) {
	if h == nil || h.health == nil {
		return
	}
	if protectedLoss {
		h.health.force(HealthDegraded)
		return
	}
	h.health.force(HealthHealthy)
}

// BeginRebuild restores a persisted authoritative-empty state after restart.
// It is intentionally explicit: an ordinary inventory mismatch must never
// reinterpret a transport outage as an empty store.
func (h *Handle) BeginRebuild() {
	if h == nil || h.health == nil {
		return
	}
	h.health.force(HealthEmptyRebuilding)
}

// StartHealthProbes runs bounded periodic recovery checks. Event-path failures
// are observed immediately through ReportFailure; this loop confirms recovery
// without a fixed polling storm during a prolonged outage.
func (h *Handle) StartHealthProbes(ctx context.Context) {
	if h == nil {
		return
	}
	go func() {
		backoff := healthProbeInitialBackoff
		for {
			state, _ := h.Health()
			wait := healthProbeHealthyInterval
			if state == HealthUnavailable || state == HealthWrongMount {
				wait = backoff
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-h.probeSignal:
				timer.Stop()
			case <-timer.C:
			}
			if err := h.ProbeNow(ctx); err != nil {
				backoff *= 2
				if backoff > healthProbeMaxBackoff {
					backoff = healthProbeMaxBackoff
				}
			} else {
				backoff = healthProbeInitialBackoff
			}
		}
	}()
}

type healthStore struct {
	Store
	handle *Handle
}

func (s *healthStore) Health() (HealthState, time.Time) {
	return s.handle.Health()
}

func (s *healthStore) Root() string {
	if rooted, ok := s.Store.(interface{ Root() string }); ok {
		return rooted.Root()
	}
	return ""
}

func (s *healthStore) FreeSpaceBytes(ctx context.Context) (int64, error) {
	capacity, ok := s.Store.(CapacityProvider)
	if !ok {
		return 0, ErrNotFound
	}
	return capacity.FreeSpaceBytes(ctx)
}

func (s *healthStore) writable() error {
	state, _ := s.handle.Health()
	switch state {
	case HealthUnavailable:
		return ErrBackendUnavailable
	case HealthWrongMount:
		return ErrWrongMount
	default:
		return nil
	}
}

func (s *healthStore) WriteImmutable(ctx context.Context, key string, data []byte, metadata ObjectMetadata) error {
	if err := s.writable(); err != nil {
		return err
	}
	err := s.Store.WriteImmutable(ctx, key, data, metadata)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return err
}

func (s *healthStore) Open(ctx context.Context, key string) (*Object, error) {
	if err := s.writable(); err != nil {
		return nil, err
	}
	object, err := s.Store.Open(ctx, key)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return object, err
}

func (s *healthStore) Stat(ctx context.Context, key string) (ObjectInfo, error) {
	if err := s.writable(); err != nil {
		return ObjectInfo{}, err
	}
	info, err := s.Store.Stat(ctx, key)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return info, err
}

func (s *healthStore) Matches(ctx context.Context, key string, data []byte) (bool, error) {
	if err := s.writable(); err != nil {
		return false, err
	}
	matches, err := s.Store.Matches(ctx, key, data)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return matches, err
}

func (s *healthStore) Probe(ctx context.Context) error {
	if err := s.writable(); err != nil {
		return err
	}
	err := s.Store.Probe(ctx)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.reportProbeFailure(err)
	}
	return err
}

func (s *healthStore) ListPage(ctx context.Context, prefix, cursor string, limit int) ([]ObjectInfo, string, bool, error) {
	if err := s.writable(); err != nil {
		return nil, "", false, err
	}
	objects, next, done, err := s.Store.ListPage(ctx, prefix, cursor, limit)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return objects, next, done, err
}

func (s *healthStore) DeleteObjects(ctx context.Context, keys []string) (int, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	deleted, err := s.Store.DeleteObjects(ctx, keys)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return deleted, err
}

func (s *healthStore) DeletePrefixMaintenance(ctx context.Context, prefix string) (int, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	deleted, err := s.Store.DeletePrefixMaintenance(ctx, prefix)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return deleted, err
}

func (s *healthStore) CleanTempFiles(ctx context.Context, olderThan time.Duration) (int, error) {
	if err := s.writable(); err != nil {
		return 0, err
	}
	cleaner, ok := s.Store.(interface {
		CleanTempFiles(context.Context, time.Duration) (int, error)
	})
	if !ok {
		return 0, ErrNotFound
	}
	removed, err := cleaner.CleanTempFiles(ctx, olderThan)
	if err == nil {
		s.handle.reportSuccess()
	} else {
		s.handle.ReportFailure(err)
	}
	return removed, err
}
