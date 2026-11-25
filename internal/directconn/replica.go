package directconn

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const censusInterval = 30 * time.Second

// leaseRetryInterval is how soon a failed lease operation is tried again, so one
// failed renewal leaves the 30-second cadence's next attempt well inside the
// 60-second self-stop instead of at its edge.
const leaseRetryInterval = 5 * time.Second

// replicaOperationTimeout bounds the census and, separately, the lease renewal
// or acquisition of one monitor update.
const replicaOperationTimeout = 2 * time.Second

// errRedisUnavailable is the census error of a deployment whose Redis is
// configured but has no usable client or no presence identity.
var errRedisUnavailable = errors.New("redis is configured but unusable for the replica census")

// ReplicaGate returns current permission to terminate and availability for new
// direct connections. Without Redis both are true, except where Config.RequireRedis
// makes Redis mandatory. A stale lease never permits termination, even when the
// last census or renewal operation is still running, and a fresh grant permits
// it only after engineLeaseTTL. Redis that is configured but unusable fails
// closed: only a deployment that configures no Redis and does not require it has
// no census and no lease.
func (s *Service) ReplicaGate() ReplicaGate {
	if s.redisUnavailable {
		return replicaGate(true, 1, errRedisUnavailable, false, false)
	}
	if s.replica == nil {
		return replicaGate(false, 1, nil, false, false)
	}
	return s.replica.snapshot()
}

// projectDirect turns an otherwise on direct row off while new direct
// connections are unavailable: when the replica gate says so, or when the
// engine is stopped for a stated cause such as a failed start. A restart in
// progress or a start still held for its handler is not a cause.
func (s *Service) projectDirect(features []Feature, enabled bool) {
	if !enabled {
		return
	}
	reason := ""
	if gate := s.ReplicaGate(); !gate.Available {
		reason = gate.Reason
	} else if e := s.engineFeature(); e.State == stateOff && e.Reason != "" && e.Reason != reasonDisabled {
		reason = e.Reason
	}
	if reason == "" {
		return
	}
	for i := range features {
		if features[i].Feature == featureDirect && features[i].State == stateOn {
			features[i].State, features[i].Reason = stateOff, reason
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
				s.projectDirect(features, s.last.status.Enabled)
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
	// grant identifies the open lease grant and holdUntil is when it first permits
	// the engine to run; both are zero values without a grant.
	grant     uint64
	holdUntil time.Time
	changed   chan struct{}
	stop      chan context.Context
	done      chan struct{}
	stopErr   error

	// runCtx ends every census and lease operation when interrupt is called, so
	// a stalled Redis call cannot hold the stop and its join. start replaces
	// the run state of a monitor that ran before.
	runCtx    context.Context //nolint:containedctx // The monitor owns its operations' lifetime, as Service does.
	interrupt context.CancelFunc

	// running and used belong to the goroutine that calls start and halt.
	running bool
	used    bool

	// leaseOK counts successful lease renewals and acquisitions. A pending
	// engine restart waits for it to rise past its value at the exit.
	leaseOK uint64
}

func newReplicaMonitor(presence replicaPresence, lease *redisEngineLease, logger *slog.Logger) *replicaMonitor {
	runCtx, interrupt := context.WithCancel(context.Background())
	return &replicaMonitor{presence: presence, lease: lease, logger: logger, pending: true, changed: make(chan struct{}, 1),
		stop: make(chan context.Context), done: make(chan struct{}), runCtx: runCtx, interrupt: interrupt}
}

// snapshot reads the gate now. A grant younger than engineLeaseTTL reads as
// pending: the process that held the key before it may still be serving until
// its own self-stop and teardown end, which the TTL bounds.
func (m *replicaMonitor) snapshot() ReplicaGate {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	held, pending := m.held && now.Before(m.deadline), m.pending
	if held && now.Before(m.holdUntil) {
		held, pending = false, true
	}
	return replicaGate(m.lease != nil, m.count, m.censusErr, held, pending)
}

// window returns the open grant and the instant its lease stops permitting
// packets. The packet fence binds to the grant an engine started under.
func (m *replicaMonitor) window() (grant uint64, until time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.grant, m.deadline
}

func (m *replicaMonitor) leaseSuccesses() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.leaseOK
}

// start runs the census and lease until halt. Only the lifecycle calls it.
func (m *replicaMonitor) start(ctx context.Context) {
	if m.running {
		return
	}
	if m.used {
		m.runCtx, m.interrupt = context.WithCancel(context.Background()) //nolint:fatcontext // The previous run has ended; this replaces its run state.
		m.stop, m.done, m.stopErr = make(chan context.Context), make(chan struct{}), nil
	}
	m.used, m.running = true, true
	go m.run(ctx)
}

// halt interrupts the census and lease operation in flight, ends the run loop,
// releases the lease under a fresh bound even when ctx has ended, and returns
// the gate to its starting state. Only the lifecycle calls it.
func (m *replicaMonitor) halt(ctx context.Context) error {
	if !m.running {
		return nil
	}
	m.interrupt()
	select {
	case m.stop <- ctx:
	case <-m.done:
	}
	<-m.done
	m.running = false
	m.mu.Lock()
	m.count, m.censusErr, m.held, m.pending = 0, nil, false, true
	m.deadline, m.holdUntil, m.grant = time.Time{}, time.Time{}, 0
	m.mu.Unlock()
	return m.stopErr
}

func (m *replicaMonitor) notify() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (m *replicaMonitor) run(ctx context.Context) {
	defer close(m.done)
	if m.lease == nil {
		return
	}
	cleanup := ctx
	defer func() {
		// The stop context is the shutdown deadline and may have ended while the
		// engine drained; the release must not inherit that.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(cleanup), 2*time.Second)
		defer cancel()
		if err := m.lease.release(cleanup); err != nil {
			m.stopErr = err
			m.logger.WarnContext(cleanup, "direct connections: engine lease release failed", "component", "directconn", "error", err)
		}
	}()
	ticker := time.NewTicker(censusInterval)
	defer ticker.Stop()
	retry, hold := time.NewTimer(time.Hour), time.NewTimer(time.Hour)
	retry.Stop()
	hold.Stop()
	defer retry.Stop()
	defer hold.Stop()
	// step runs one update, then arms the retry after a failed lease operation and
	// the wake-up at the end of a grant's hold.
	step := func() {
		retry.Stop()
		if m.update(ctx) {
			retry.Reset(leaseRetryInterval)
		}
		hold.Stop()
		m.mu.Lock()
		if m.held && time.Now().Before(m.holdUntil) {
			hold.Reset(time.Until(m.holdUntil))
		}
		m.mu.Unlock()
	}
	step()
	for {
		select {
		case cleanup = <-m.stop:
			return
		case <-ticker.C:
			// After an interrupt only the stop remains.
			if m.runCtx.Err() == nil {
				step()
			}
		case <-retry.C:
			if m.runCtx.Err() == nil {
				step()
			}
		case <-hold.C:
			m.notify()
		}
	}
}

// update runs one census and one lease operation. Each has its own
// replicaOperationTimeout, so a slow census cannot expire the renewal, and an
// interrupt ends the operation in flight. An interrupted update records nothing.
// It reports whether a lease operation failed, which earns a retry. A renewal
// that completes after the grant's local deadline does not extend that grant: the
// engine's packet path was cut at the deadline, so the success opens a new grant
// with its own hold. A renewal that completes at the deadline instant still
// belongs to it, as the lifecycle's own deadline check treats it.
func (m *replicaMonitor) update(ctx context.Context) (leaseFailed bool) {
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
	var succeeded, renewed bool
	// The deadline of the grant this update renews, read before the renewal moves it.
	expires := m.lease.lastRenew.Add(engineLeaseSelfStop)
	if !m.lease.lastRenew.IsZero() {
		succeeded, leaseErr = m.lease.renew(leaseCtx, now)
		renewed = succeeded
	}
	if m.lease.lastRenew.IsZero() && censusErr == nil && count <= 1 {
		var acquired bool
		acquired, leaseErr = m.lease.acquire(leaseCtx, now)
		pending = acquired || leaseErr != nil
		succeeded = acquired
	}
	if m.runCtx.Err() != nil {
		return false
	}
	if leaseErr != nil {
		m.logger.WarnContext(leaseCtx, "direct connections: engine lease unavailable", "component", "directconn", "error", leaseErr)
	}
	m.mu.Lock()
	// The deadline is judged under the lock that the fence reads the window under.
	// The fence reads the clock after that lock, so a renewal that commits on or
	// just before the old deadline can still leave the fence latched while the
	// grant stays held.
	if renewed && time.Now().After(expires) {
		m.lease.open(m.lease.lastRenew)
	}
	m.count, m.censusErr = count, censusErr
	m.held, m.pending = m.lease.held(time.Now()), pending
	m.deadline = m.lease.lastRenew.Add(engineLeaseSelfStop)
	m.grant = m.lease.grant
	m.holdUntil = time.Time{}
	if !m.lease.grantStart.IsZero() {
		m.holdUntil = m.lease.grantStart.Add(engineLeaseTTL)
	}
	if succeeded {
		m.leaseOK++
	}
	m.mu.Unlock()
	m.notify()
	return leaseErr != nil
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
