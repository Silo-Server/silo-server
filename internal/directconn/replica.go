package directconn

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const censusInterval = 30 * time.Second

// replicaOperationTimeout bounds the census and, separately, the lease renewal
// or acquisition of one monitor update.
const replicaOperationTimeout = 2 * time.Second

// errRedisUnavailable is the census error of a deployment whose Redis is
// configured but has no usable client or no presence identity.
var errRedisUnavailable = errors.New("redis is configured but unusable for the replica census")

// ReplicaGate returns current permission to terminate and availability for new
// direct connections. Without Redis both are true. A stale lease never permits
// termination, even when the last census or renewal operation is still running.
// Redis that is configured but unusable fails closed: only a deployment that
// configures no Redis has no census and no lease.
func (s *Service) ReplicaGate() ReplicaGate {
	if s.redisUnavailable {
		return replicaGate(true, 1, errRedisUnavailable, false, false)
	}
	if s.replica == nil {
		return replicaGate(false, 1, nil, false, false)
	}
	return s.replica.snapshot()
}

func (s *Service) projectReplicaGate(features []Feature, enabled bool) {
	if !enabled {
		return
	}
	gate := s.ReplicaGate()
	if gate.Available {
		return
	}
	for i := range features {
		if features[i].Feature == featureDirect && features[i].State == stateOn {
			features[i].State, features[i].Reason = stateOff, gate.Reason
		}
	}
}

// The census has one notification reader. Each notification wakes the
// lifecycle, also before the first snapshot exists, so a lease operation that
// completes a pending restart reaches it.
func (s *Service) replicaEvents() {
	defer close(s.replicaEventsDone)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.replica.changed:
			s.mu.Lock()
			if s.last != nil {
				features := append([]Feature(nil), s.last.status.Features...)
				s.projectReplicaGate(features, s.last.status.Enabled)
				s.logFeatures(s.ctx, s.last.status.Enabled, features)
			}
			s.mu.Unlock()
			s.wakeEngine()
		}
	}
}

type replicaPresence interface {
	Count(context.Context) (int, error)
	Refresh(context.Context) error
}

type replicaMonitor struct {
	presence  replicaPresence
	lease     *redisEngineLease
	logger    *slog.Logger
	mu        sync.Mutex
	count     int
	censusErr error
	held      bool
	pending   bool
	deadline  time.Time
	changed   chan struct{}
	stop      chan context.Context
	done      chan struct{}
	stopErr   error

	// runCtx ends every census and lease operation when interrupt is called, so
	// a stalled Redis call cannot hold the stop and its join.
	runCtx    context.Context //nolint:containedctx // The monitor owns its operations' lifetime, as Service does.
	interrupt context.CancelFunc

	// leaseOK counts successful lease renewals and acquisitions. A pending
	// engine restart waits for it to rise past its value at the exit.
	leaseOK uint64
}

func newReplicaMonitor(presence replicaPresence, lease *redisEngineLease, logger *slog.Logger) *replicaMonitor {
	runCtx, interrupt := context.WithCancel(context.Background())
	return &replicaMonitor{presence: presence, lease: lease, logger: logger, pending: true, changed: make(chan struct{}, 1),
		stop: make(chan context.Context), done: make(chan struct{}), runCtx: runCtx, interrupt: interrupt}
}

func (m *replicaMonitor) snapshot() ReplicaGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	return replicaGate(m.lease != nil, m.count, m.censusErr, m.held && time.Now().Before(m.deadline), m.pending)
}

func (m *replicaMonitor) leaseSuccesses() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leaseOK
}

func (m *replicaMonitor) run(ctx context.Context) {
	defer close(m.done)
	if m.lease == nil {
		return
	}
	cleanup := context.Background()
	defer func() {
		cleanup, cancel := context.WithTimeout(cleanup, 2*time.Second)
		defer cancel()
		if err := m.lease.release(cleanup); err != nil {
			m.stopErr = err
			m.logger.WarnContext(cleanup, "direct connections: engine lease release failed", "component", "directconn", "error", err)
		}
	}()
	m.update(ctx)
	ticker := time.NewTicker(censusInterval)
	defer ticker.Stop()
	for {
		select {
		case cleanup = <-m.stop:
			return
		case <-ticker.C:
			// After an interrupt only the stop remains.
			if m.runCtx.Err() == nil {
				m.update(ctx)
			}
		}
	}
}

// update runs one census and one lease operation. Each has its own
// replicaOperationTimeout, so a slow census cannot expire the renewal, and an
// interrupt ends the operation in flight. An interrupted update records nothing.
func (m *replicaMonitor) update(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopInterrupt := context.AfterFunc(m.runCtx, cancel)
	defer stopInterrupt()
	censusCtx, cancelCensus := context.WithTimeout(ctx, replicaOperationTimeout)
	count, censusErr := m.presence.Count(censusCtx)
	if censusErr == nil && count == 0 {
		censusErr = m.presence.Refresh(censusCtx)
		count = 1
	}
	cancelCensus()
	leaseCtx, cancelLease := context.WithTimeout(ctx, replicaOperationTimeout)
	defer cancelLease()
	now := time.Now()
	pending := true
	var leaseErr error
	var succeeded bool
	if !m.lease.lastRenew.IsZero() {
		succeeded, leaseErr = m.lease.renew(leaseCtx, now)
	}
	if m.lease.lastRenew.IsZero() && censusErr == nil && count <= 1 {
		var acquired bool
		acquired, leaseErr = m.lease.acquire(leaseCtx, now)
		pending = acquired || leaseErr != nil
		succeeded = acquired
	}
	if m.runCtx.Err() != nil {
		return
	}
	if leaseErr != nil {
		m.logger.WarnContext(leaseCtx, "direct connections: engine lease unavailable", "component", "directconn", "error", leaseErr)
	}
	m.mu.Lock()
	m.count, m.censusErr = count, censusErr
	m.held, m.pending = m.lease.held(time.Now()), pending
	m.deadline = m.lease.lastRenew.Add(engineLeaseSelfStop)
	if succeeded {
		m.leaseOK++
	}
	m.mu.Unlock()
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

// ReplicaGate separates permission to keep terminating established sessions
// from availability for new direct connections. Reason is empty when available.
type ReplicaGate struct {
	EngineMayRun bool
	Available    bool
	Reason       string
}

func replicaGate(redisConfigured bool, count int, censusErr error, held, pending bool) ReplicaGate {
	if !redisConfigured {
		return ReplicaGate{true, true, ""}
	}
	gate := ReplicaGate{EngineMayRun: held}
	switch {
	case censusErr != nil:
		gate.Reason = "replica_census_unavailable"
	case count > 1:
		gate.Reason = "multiple_api_replicas"
	case held:
		gate.Available = true
	case pending:
		gate.Reason = "replica_lease_pending"
	default:
		gate.Reason = "replica_lease_held"
	}
	return gate
}
