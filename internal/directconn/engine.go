package directconn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
)

const (
	// hostRefreshInterval is how often the host entry and the confirmations are
	// written again, so none lapses while the engine is healthy.
	hostRefreshInterval = 30 * time.Second
	// storeTimeout bounds one coordination store write from the engine's events.
	storeTimeout = 5 * time.Second

	reasonEngineStartFailed = "engine_start_failed"
	reasonPortBindFailed    = "udp_port_bind_failed"
	reasonStoreUnavailable  = "state_store_unavailable"
)

type engineHandle interface {
	LocalPort() uint16
	Changed() <-chan struct{}
	PacketConn() net.PacketConn
	SetPeers([]engine.Peer) error
	IsPeer(protocol.NodeKey) bool
	Registration() (protocol.Registration, error)
	Path(protocol.NodeKey) engine.PathStatus
	Close() error
}

type engineServer interface {
	Serve(net.PacketConn) error
	Shutdown(context.Context) error
}

type engineState struct {
	// engineControl owns transitions. mu protects settings, current and
	// published features; peersMu serializes peer application with detachment.
	// Constructors, peer application and teardown run without mu.
	mu           sync.Mutex
	peersMu      sync.Mutex
	values       settings
	applied      settings
	gate         ReplicaGate
	ready        bool
	retry        bool
	failed       bool
	current      engineHandle
	server       engineServer
	peers        []engine.Peer
	feature      Feature
	pending      []Feature
	wake         chan struct{}
	drift        chan engineHandle
	eventsCancel context.CancelFunc
	eventsDone   chan struct{}
	serveDone    chan struct{}
	shutdown     chan context.Context
	shutdownErr  error
	done         chan struct{}

	// restartPending is set by an unexpected Serve exit. The restart waits for a
	// settings read and, with Redis, a successful lease operation that both
	// complete after the exit: reads above exitReads and lease successes above
	// exitLease. restarted spends the budget of one restart; it refills when
	// failed clears. mu protects reads; engineControl owns the other four.
	restartPending bool
	restarted      bool
	reads          uint64
	exitReads      uint64
	exitLease      uint64

	// handlerReady is set by HandlerReady; with Config.AwaitHandler no engine
	// starts before it. mu protects it.
	handlerReady bool

	// graceless makes drainServer close every connection at once instead of
	// waiting out the grace period; the lifecycle sets it for a stop caused by
	// the lease and clears it after.
	graceless bool

	// coord, paths, storeDown and coordWorkers belong to the coordination
	// service attached with SetCoordination; mu protects the first three.
	coord        *Coordination
	paths        *pathTracker
	storeDown    bool
	coordWorkers sync.WaitGroup
}

// errStaleStart marks a candidate engine closed unpublished because the
// settings or the replica gate changed while it was built.
var errStaleStart = errors.New("direct engine start is stale")

func buildEngineConfig(cfg Config, values settings) (engine.Config, n4.Policy, bool, error) {
	policy := n4.DefaultServerPolicy()
	policy.Loss = n4.LossProfile(cfg.Tuning.DirectBBRLoss)
	key, err := apiNodeKey(cfg.SecretKey)
	if err != nil {
		return engine.Config{}, policy, values.enabled, err
	}
	if cfg.Tuning.DirectUDPPort < 1 || cfg.Tuning.DirectUDPPort > 65535 {
		return engine.Config{}, policy, values.enabled, errors.New("invalid direct UDP port")
	}
	c := engine.Config{PrivateKey: key, Port: uint16(cfg.Tuning.DirectUDPPort), GSO: cfg.Tuning.DirectGSO,
		PortMapping: values.portMapping, STUNServers: values.servers(), Logger: cfg.Logger.With(slog.String("component", "directconn"))}
	// Nil RelayedDataAllowed denies send and receive for every device policy.
	return c, policy, values.enabled, policy.Validate()
}

func (s *Service) initEngine() {
	s.lifecycle = &engineState{wake: make(chan struct{}, 1), drift: make(chan engineHandle, 1), done: make(chan struct{}), shutdown: make(chan context.Context),
		feature: Feature{Feature: "engine", Kind: "local", State: stateOff, Reason: reasonDisabled}}
	if s.cfg.newEngine == nil {
		s.cfg.newEngine = func(ctx context.Context, c engine.Config) (engineHandle, error) { return engine.New(ctx, c) }
	}
	if s.cfg.newServer == nil && s.cfg.ServerFactory != nil {
		s.cfg.newServer = func(e engineHandle, p n4.Policy) engineServer {
			instance, ok := e.(*engine.Engine)
			if !ok {
				return nil
			}
			srv := s.cfg.ServerFactory(instance, p)
			if srv == nil {
				return nil
			}
			return srv
		}
	}
	go s.engineControl()
}

// Shutdown stops answering holds, drains HTTP/3 until drainReserve before ctx's
// deadline (when it has one) and then closes any open streams, closes the region,
// removes the API host entry, closes the engine synchronously, then stops and
// joins the lease monitor, which releases the lease under its own bound even when
// ctx has expired, then cancels the service context. It waits for all lifecycle
// goroutines even after ctx expires; engine close has no timeout wrapper.
// Repeated calls return the same shutdown error.
func (s *Service) Shutdown(ctx context.Context) error {
	l := s.lifecycle
	// Held netmaps go first: the control loop may be busy, and a device must not
	// wait out its hold while the HTTP servers drain.
	s.beginStop()
	select {
	case l.shutdown <- ctx:
	case <-l.done:
	}
	<-l.done
	return l.shutdownErr
}

// HandlerReady releases engine starts held by Config.AwaitHandler. The lifecycle
// then starts the engine if settings and the replica gate allow it, so the
// HTTP/3 server factory never waits for a handler on the lifecycle goroutine.
func (s *Service) HandlerReady() {
	l := s.lifecycle
	l.mu.Lock()
	l.handlerReady = true
	l.mu.Unlock()
	s.wakeEngine()
}

func (s *Service) wakeEngine() {
	if s.lifecycle == nil {
		return
	}
	select {
	case s.lifecycle.wake <- struct{}{}:
	default:
	}
}

// setEngineSettings hands a successful settings read to the lifecycle. The
// first read, changed values and a settings-change event retry a failed start;
// the minute recheck and Status rereads of unchanged values do not, so a failed
// start waits for a settings or gate change.
func (s *Service) setEngineSettings(values settings, event bool) {
	l := s.lifecycle
	l.mu.Lock()
	retry := event || !l.ready || values != l.values
	l.values, l.ready = values, true
	l.reads++
	if l.current == nil && l.feature.State == stateOff {
		if reason := s.engineStopReason(values, s.ReplicaGate()); reason != "" {
			l.feature.Reason = reason
		}
	}
	if retry {
		l.retry = true
	}
	l.mu.Unlock()
	s.wakeEngine()
}

func (s *Service) engineFeature() Feature {
	s.lifecycle.mu.Lock()
	defer s.lifecycle.mu.Unlock()
	return s.lifecycle.feature
}

// SetPeers replaces leased peers, copying the input, and replays them before
// constructing the next HTTP/3 server. A live engine validates the list first;
// a list it rejects is returned as an error and is not kept, so a restart never
// replays it. With no live engine the list is staged unvalidated for the next
// start.
func (s *Service) SetPeers(peers []engine.Peer) error {
	l := s.lifecycle
	l.peersMu.Lock()
	defer l.peersMu.Unlock()
	staged := slices.Clone(peers)
	for i := range staged {
		staged[i].Endpoints = slices.Clone(staged[i].Endpoints)
		if c := staged[i].Confirmation; c != nil {
			copyOf := *c
			staged[i].Confirmation = &copyOf
		}
	}
	l.mu.Lock()
	e := l.current
	l.mu.Unlock()
	if e != nil {
		if err := e.SetPeers(staged); err != nil {
			return err
		}
	}
	l.peers = staged
	return nil
}

// engineControl observes the lease deadline independently of Redis operations.
// The census keeps its single notification reader; feature publication wakes us.
func (s *Service) engineControl() {
	l := s.lifecycle
	defer close(l.done)
	var drift engineHandle
	for {
		s.reconcileEngine(drift)
		drift = nil
		active := l.current != nil
		s.publishEngineFeatures()
		var deadline <-chan time.Time
		var timer *time.Timer
		// The loop drains servers itself and clears serveDone after, so a close
		// seen here while the engine is current is an unexpected exit.
		var serveDone <-chan struct{}
		if active {
			serveDone = l.serveDone
		}
		if active && s.replica != nil {
			s.replica.mu.Lock()
			expires := s.replica.deadline
			s.replica.mu.Unlock()
			// A renewal completed at the deadline still belongs to that instant.
			// Self-stop applies once its age exceeds 60 seconds.
			timer = time.NewTimer(max(0, time.Until(expires.Add(time.Nanosecond))))
			deadline = timer.C
		}
		select {
		case ctx := <-l.shutdown:
			if timer != nil {
				timer.Stop()
			}
			s.stopEngine(reasonDisabled, ctx)
			s.publishEngineFeatures()
			return
		case <-s.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			ctx, cancel := context.WithTimeout(context.Background(), checkTimeout)
			s.stopEngine(reasonDisabled, ctx)
			cancel()
			s.publishEngineFeatures()
			return
		case <-l.wake:
		case drift = <-l.drift:
		case <-deadline:
		case <-serveDone:
			s.serveExited()
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *Service) reconcileEngine(drift engineHandle) {
	l := s.lifecycle
	l.mu.Lock()
	values, ready := l.values, l.ready
	// Keep the retry flag while a start is held: HandlerReady runs the start.
	held := s.cfg.AwaitHandler && !l.handlerReady
	var retry bool
	if !held {
		retry = l.retry
		l.retry = false
	}
	l.mu.Unlock()
	if !ready {
		return
	}
	// The census and lease run only while direct connections are on. Starting
	// them before the handler exists lets a fresh grant's hold overlap startup.
	if s.replica != nil && values.enabled {
		s.replica.start(context.WithoutCancel(s.ctx))
	}
	if held {
		// No engine runs before its handler is ready, so a disable in that window
		// has nothing to close before it stops the monitor and releases the lease.
		if s.replica != nil && !values.enabled {
			s.haltReplica()
		}
		return
	}
	gate := s.ReplicaGate()
	if retry || gate != l.gate {
		l.failed = false
		l.restarted = false
		l.mu.Lock()
		l.gate = gate
		l.mu.Unlock()
	}
	if reason := s.engineStopReason(values, gate); reason != "" {
		// A stop the lease forces closes connections at once: the lease no longer
		// covers them.
		l.graceless = values.enabled && reason == gate.Reason
		s.stopEngine(reason, nil)
		l.graceless = false
		if s.replica != nil && !values.enabled {
			// The engine is closed first, so the lease outlives it.
			s.haltReplica()
		}
		l.restartPending = false
		l.mu.Lock()
		l.feature.State, l.feature.Reason = stateOff, reason
		l.mu.Unlock()
		return
	}
	if l.current != nil && (drift == l.current || l.applied != values) {
		s.stopEngine("", nil)
		// Re-read settings and the lease after teardown before starting again.
		s.wakeEngine()
		return
	}
	if l.current != nil {
		l.mu.Lock()
		l.feature.Reason = s.liveReason(gate)
		l.mu.Unlock()
		return
	}
	if l.restartPending {
		if !s.restartReady() {
			return
		}
		l.restartPending, l.restarted = false, true
	} else if l.failed {
		return
	}
	if err := s.startEngine(values); err != nil {
		if errors.Is(err, errStaleStart) {
			// Not a failure: the newer input is already waiting.
			s.wakeEngine()
			return
		}
		l.failed = true
		reason := reasonEngineStartFailed
		if errors.Is(err, engine.ErrPortUnavailable) {
			reason = reasonPortBindFailed
		}
		l.mu.Lock()
		l.feature.State, l.feature.Reason = stateOff, reason
		l.pending = append(l.pending, l.feature)
		l.mu.Unlock()
		s.cfg.Logger.WarnContext(s.ctx, "direct connections: engine start failed", "component", "directconn", "error", err)
	}
}

// restartReady reports whether a settings read and, with Redis, a successful
// lease renewal or acquisition have both completed since the unexpected exit.
// Without Redis there is no lease, so the read alone is enough.
func (s *Service) restartReady() bool {
	l := s.lifecycle
	l.mu.Lock()
	read := l.reads > l.exitReads
	l.mu.Unlock()
	return read && (s.replica == nil || s.replica.leaseSuccesses() > l.exitLease)
}

// serveExited withdraws an engine whose HTTP/3 Serve returned while it was
// current. One critical section detaches the engine, turns the feature off and
// records both restart marks, with no blocking step before it, so a peer update
// no longer reaches the failed engine and a lease operation that completes
// during the publication counts as after the exit. Then it publishes, joins the
// event worker, so no host write follows the removal of the API host entry,
// removes that entry, and drains and closes the detached engine. The first exit
// schedules one restart and starts a settings reread for it; a second exit
// before any settings or gate change leaves the engine failed.
func (s *Service) serveExited() {
	l := s.lifecycle
	l.peersMu.Lock()
	l.mu.Lock()
	e := l.current
	l.current = nil
	l.feature.State, l.feature.Reason = stateOff, reasonEngineStartFailed
	l.pending = append(l.pending, l.feature)
	l.exitReads = l.reads
	if s.replica != nil {
		l.exitLease = s.replica.leaseSuccesses()
	}
	l.mu.Unlock()
	l.peersMu.Unlock()
	s.publishEngineFeatures()
	s.cfg.Logger.WarnContext(s.ctx, "direct connections: HTTP/3 serve exited; engine withdrawn", "component", "directconn", "restart", !l.restarted)
	if l.restarted {
		l.failed = true
	} else {
		l.restartPending = true
		s.startAsyncReread()
	}
	if e == nil {
		return
	}
	s.joinEngineEvents()
	// Devices are not offered a host whose HTTP/3 server no longer serves.
	if coord, _ := s.coordination(); coord != nil {
		_ = s.deleteHost(coord, "host withdrawal")
	}
	s.closeEngine(e, reasonEngineStartFailed, nil)
}

// deleteHost removes the API host entry and every confirmation under it,
// bounded by storeTimeout, and classifies the result as the store write what.
// On Redis the removal is fenced by the lease: while another process holds it,
// the removal is refused and that process's entries stay.
func (s *Service) deleteHost(c *Coordination, what string) error {
	ctx, cancel := context.WithTimeout(s.ctx, storeTimeout)
	defer cancel()
	err := c.srv.DeleteHost(ctx, coordinationHostLabel)
	s.storeResult(ctx, what, err)
	return err //nolint:wrapcheck // The caller only branches on success; storeResult logged the error.
}

// haltReplica stops the census and lease and releases the lease. A release
// failure is logged: the lease expires on its own.
func (s *Service) haltReplica() {
	if err := s.replica.halt(context.Background()); err != nil {
		s.cfg.Logger.WarnContext(s.ctx, "direct connections: engine lease release failed", "component", "directconn", "error", err)
	}
}

func (s *Service) engineStopReason(values settings, gate ReplicaGate) string {
	switch {
	case !values.enabled:
		return reasonDisabled
	case s.cfg.Tuning.DirectUDPPort < 1 || s.cfg.Tuning.DirectUDPPort > 65535:
		return "udp_port_invalid"
	case !gate.EngineMayRun:
		return gate.Reason
	default:
		return ""
	}
}

// startEngine builds an engine for values and publishes it. It rechecks the
// settings and the replica gate after construction, which can outlast the
// lease: a candidate that no longer has permission is closed unpublished and
// errStaleStart is returned.
func (s *Service) startEngine(values settings) error {
	l := s.lifecycle
	c, policy, _, err := buildEngineConfig(s.cfg, values)
	if err != nil {
		return err
	}
	// One fence per engine, bound to the grant it starts under: the HTTP/3
	// packet path, the engine's discovery and the coordination writes close
	// together. A grant that changes during construction is in its hold, so the
	// stale check below closes the candidate.
	var fence *grantFence
	if s.replica != nil {
		grant, _ := s.replica.window()
		fence = newGrantFence(s.replica, grant)
		c.Window = fence.until
	}
	// The engine dials its own region at construction. The gate holds that dial
	// until the host entry exists, so the region never misses the API host.
	coord, paths := s.coordination()
	host := &hostPublication{fence: fence}
	if coord != nil {
		host.gate = newDialGate()
		c.Signaling = &engine.Signaling{RegionID: 0, Dial: coord.dialLocal(host.gate, s.ctx)}
	}
	e, err := s.cfg.newEngine(s.ctx, c)
	if err != nil {
		if errors.Is(err, engine.ErrPortUnavailable) {
			return fmt.Errorf("direct UDP port unavailable: %w", err)
		}
		return err
	}
	l.peersMu.Lock()
	if coord != nil {
		// The records and confirmations of an earlier engine describe its paths,
		// not this one's: a device is ready again when this engine confirms it.
		paths.reset()
		// The cache may have changed since the last notification was handled.
		entries := coord.directory.Devices()
		paths.setDevices(entries)
		l.peers = enginePeers(entries)
	}
	err = e.SetPeers(l.peers)
	l.mu.Lock()
	stale := false
	if err == nil {
		stale = l.values != values || s.engineStopReason(l.values, s.ReplicaGate()) != ""
	}
	if !stale {
		l.current = e
	}
	l.mu.Unlock()
	l.peersMu.Unlock()
	if stale {
		if cerr := e.Close(); cerr != nil {
			s.cfg.Logger.WarnContext(s.ctx, "direct connections: engine close failed", "component", "directconn", "error", cerr)
		}
		return errStaleStart
	}
	if err != nil {
		s.stopEngine(reasonEngineStartFailed, nil)
		return fmt.Errorf("apply direct engine peers: %w", err)
	}
	if coord != nil {
		// The host entry exists before the first event or timer tick. A store
		// outage here leaves the engine up and the gate shut; the next tick clears
		// the previous holder's entries if that is still due, writes the entry and
		// opens the gate.
		s.publishHost(coord, e, host)
	}
	if s.cfg.newServer != nil {
		l.server = s.cfg.newServer(e, policy)
		if l.server != nil {
			srv := l.server
			done := make(chan struct{})
			l.serveDone = done
			conn := fenceFor(e.PacketConn(), fence)
			go func() {
				defer close(done)
				if err := srv.Serve(conn); err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, http.ErrServerClosed) {
					s.cfg.Logger.WarnContext(s.ctx, "direct connections: HTTP/3 serve stopped", "component", "directconn", "error", err)
				}
			}()
		}
	}
	eventsCtx, cancel := context.WithCancel(s.ctx)
	l.eventsCancel, l.eventsDone = cancel, make(chan struct{})
	go s.engineEvents(eventsCtx, e, host, l.eventsDone)
	l.applied = values
	l.mu.Lock()
	l.feature.State, l.feature.Reason = stateOn, s.liveReason(l.gate)
	l.pending = append(l.pending, l.feature)
	l.mu.Unlock()
	return nil
}

// engineEvents is the sole reader of Changed for this engine. On each event it
// checks for port drift first, then writes the host entry and publishes the
// confirmation changes. A 30 s tick writes the host entry again and renews every
// live confirmation, because an unchanged pong fires no event. A drift restart
// ends this goroutine; the next engine's start begins another. Stop joins it
// before closing the engine, including when the next engine replaces it.
func (s *Service) engineEvents(ctx context.Context, e engineHandle, host *hostPublication, done chan struct{}) {
	defer close(done)
	coord, _ := s.coordination()
	ticker := time.NewTicker(hostRefreshInterval)
	defer ticker.Stop()
	published := map[protocol.NodeKey]protocol.Confirmation{}
	for {
		renew := false
		select {
		case <-ctx.Done():
			return
		case <-e.Changed():
			if int(e.LocalPort()) != s.cfg.Tuning.DirectUDPPort {
				select {
				case s.lifecycle.drift <- e:
				case <-ctx.Done():
				}
				return
			}
		case <-ticker.C:
			renew = true
		}
		if coord == nil {
			continue
		}
		if reg, ok := s.publishHost(coord, e, host); ok {
			published = s.syncConfirmations(ctx, coord, host.fence, reg.Confirmations, published, renew)
		}
	}
}

// hostPublication is one engine's host entry state. startEngine and then the
// engine's event worker use it in turn, never at once.
type hostPublication struct {
	// gate holds the engine's first region dial until the host entry exists.
	gate *dialGate
	// cleared is set once the previous holder's host entry and confirmations
	// are gone.
	cleared bool
	// fence is the engine's grant fence; nil without a lease.
	fence *grantFence
}

// publishHost writes the engine's host entry. Before the first write it removes
// the API host entry and every confirmation under it, so no device reads a
// previous lease holder's confirmations beside this engine's host; on Redis the
// removal is fenced by the lease. Until that removal succeeds nothing is
// published and the dial gate stays shut. It returns the engine's registration
// and whether confirmations may be published: the engine was still open to give
// a registration and the removal is done. A store failure is classified but
// does not hide the registration. The first host write that the store accepts
// drops any remembered miss for the host's key and opens the dial gate. Once
// the engine's grant fence has closed it writes nothing and returns false.
func (s *Service) publishHost(c *Coordination, e engineHandle, host *hostPublication) (protocol.Registration, bool) {
	reg, err := e.Registration()
	if err != nil {
		return protocol.Registration{}, false
	}
	if !host.cleared {
		if !host.fence.open() || s.deleteHost(c, "host cleanup") != nil {
			return reg, false
		}
		host.cleared = true
	}
	if !host.fence.open() {
		return reg, false
	}
	ctx, cancel := context.WithTimeout(s.ctx, storeTimeout)
	defer cancel()
	err = c.putHost(ctx, reg)
	s.storeResult(ctx, "host entry", err)
	if err == nil {
		host.gate.release(func() { c.directory.Invalidate(reg.NodeKey) })
	}
	return reg, true
}

// syncConfirmations publishes the engine's confirmations: a changed or new one
// is set, an unchanged one is set again only when renew is true, and one the
// engine dropped is cleared on an event, not on a renewal. It returns the set
// that is now published. A set that failed keeps the previously published value
// of its peer, if any, and a clear that failed stays in the set, so the next
// call retries. Once fence has closed it writes nothing, and a clear it skips
// stays in the set.
func (s *Service) syncConfirmations(ctx context.Context, c *Coordination, fence *grantFence, current []protocol.Confirmation, published map[protocol.NodeKey]protocol.Confirmation, renew bool) map[protocol.NodeKey]protocol.Confirmation {
	_, paths := s.coordination()
	next := make(map[protocol.NodeKey]protocol.Confirmation, len(current))
	present := make(map[protocol.NodeKey]bool, len(current))
	for _, confirmation := range current {
		present[confirmation.Peer] = true
		if old, ok := published[confirmation.Peer]; ok && old == confirmation && !renew {
			next[confirmation.Peer] = confirmation
			continue
		}
		if !fence.open() {
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, storeTimeout)
		err := c.setConfirmation(writeCtx, confirmation)
		cancel()
		if err == nil {
			next[confirmation.Peer] = confirmation
			paths.setConfirmed(confirmation.Peer, true)
			// Observe the path now, so a peer's Since is when its path became
			// direct and not when something first read it.
			s.devicePath(confirmation.Peer)
			s.storeResult(ctx, "confirmation", nil)
		} else if !errors.Is(err, server.ErrNotFound) {
			// The store may still hold the previous confirmation. Keeping it in next
			// lets the following call retry the write, or clear it once the engine
			// drops the peer. A missing device holds none: its confirmations went
			// with it.
			if old, ok := published[confirmation.Peer]; ok {
				next[confirmation.Peer] = old
			}
			s.storeResult(ctx, "confirmation", err)
		}
	}
	for peer, old := range published {
		if present[peer] {
			continue
		}
		if renew {
			// The engine fires Changed when it drops a confirmation, and that event
			// clears it. A renewal only extends what is live, so a dropped one
			// lapses at the end of its stored lifetime.
			next[peer] = old
			continue
		}
		if !fence.open() {
			next[peer] = old
			continue
		}
		writeCtx, cancel := context.WithTimeout(ctx, storeTimeout)
		err := c.clearConfirmation(writeCtx, peer)
		cancel()
		if err == nil || errors.Is(err, server.ErrNotFound) {
			paths.setConfirmed(peer, false)
			continue
		}
		next[peer] = old
		s.storeResult(ctx, "confirmation", err)
	}
	return next
}

// storeResult records whether the coordination store answered. An outage sets
// the engine's reason to state_store_unavailable until a write succeeds; the
// first failure of an outage is logged. Any other failure is logged and changes
// nothing, a fenced write refused because another process holds the lease
// included: the next census fails the renewal and stops the engine.
func (s *Service) storeResult(ctx context.Context, what string, err error) {
	l := s.lifecycle
	down := errors.Is(err, server.ErrUnavailable)
	if err != nil && !down {
		s.cfg.Logger.WarnContext(ctx, "direct connections: coordination write failed", "component", "directconn", "write", what, "error", err)
		return
	}
	l.mu.Lock()
	changed := l.storeDown != down
	l.storeDown = down
	if changed && l.current != nil {
		l.feature.Reason = s.liveReason(l.gate)
		l.pending = append(l.pending, l.feature)
	}
	l.mu.Unlock()
	if changed {
		if down {
			s.cfg.Logger.WarnContext(ctx, "direct connections: coordination store unavailable", "component", "directconn", "write", what)
		}
		s.wakeEngine()
	}
}

// liveReason is the reason a running engine reports. The caller holds l.mu.
func (s *Service) liveReason(gate ReplicaGate) string {
	if s.lifecycle.storeDown {
		return reasonStoreUnavailable
	}
	return gate.Reason
}

// stopEngine detaches the current engine and tears it down; with a shutdown
// context it also runs the shutdown steps, with or without an engine. A
// non-empty reason publishes the engine row off with that reason; an empty
// reason is a restart and leaves the row as it is until the next start or stop
// sets it.
func (s *Service) stopEngine(reason string, shutdownCtx context.Context) {
	l := s.lifecycle
	l.peersMu.Lock()
	l.mu.Lock()
	e := l.current
	// Peer updates now target the next start rather than the closing engine.
	l.current = nil
	l.mu.Unlock()
	l.peersMu.Unlock()
	s.teardownEngine(e, reason, shutdownCtx)
}

// teardownEngine ends a detached engine: it joins the event worker, then
// closeEngine drains, closes and, on shutdown, releases the lease. On a stop the
// lease forces, the lease may already have passed to another process: the packet
// fence has cut the engine off by then, and the drain closes connections at once.
// A nil engine with no shutdown context has nothing to end.
func (s *Service) teardownEngine(e engineHandle, reason string, shutdownCtx context.Context) {
	if e == nil && shutdownCtx == nil {
		return
	}
	s.joinEngineEvents()
	s.closeEngine(e, reason, shutdownCtx)
}

// joinEngineEvents cancels and joins the event worker of the engine just
// detached, so no host or confirmation write runs once it returns.
func (s *Service) joinEngineEvents() {
	l := s.lifecycle
	if l.eventsCancel != nil {
		l.eventsCancel()
		<-l.eventsDone
		l.eventsCancel = nil
	}
}

// closeEngine drains the HTTP/3 server and closes e, which is nil at a shutdown
// with no engine. On shutdown it closes the region and removes the API host
// entry between the drain and the close, then stops the replica monitor, which
// releases the lease, and joins the workers. The engine closes before the lease
// is released, so no successor holds the lease while this engine is open, and
// the host entry is removed while this process still holds it.
func (s *Service) closeEngine(e engineHandle, reason string, shutdownCtx context.Context) {
	l := s.lifecycle
	if l.server != nil {
		s.drainServer(shutdownCtx)
	}
	if shutdownCtx != nil {
		s.beginStop()
		if coord, _ := s.coordination(); coord != nil {
			l.shutdownErr = errors.Join(l.shutdownErr, coord.closeForShutdown(e != nil))
		}
	}
	if e != nil {
		if err := e.Close(); err != nil {
			if shutdownCtx != nil {
				l.shutdownErr = errors.Join(l.shutdownErr, err)
			}
			s.cfg.Logger.WarnContext(s.ctx, "direct connections: engine close failed", "component", "directconn", "error", err)
		}
	}
	if shutdownCtx != nil {
		if s.replica != nil {
			// halt ends a census or renewal in flight, so the stop does not wait for it.
			l.shutdownErr = errors.Join(l.shutdownErr, s.replica.halt(shutdownCtx))
		}
		// The service context ends last: the engine and its workers ran under it
		// until the engine closed.
		s.cancelService()
		s.checkWorkers.Wait()
		l.coordWorkers.Wait()
		if s.replicaEventsDone != nil {
			<-s.replicaEventsDone
		}
	}
	if reason == "" {
		// A restart: the engine row goes from its old state to the new one, with
		// no off row in between.
		return
	}
	l.mu.Lock()
	l.feature.State, l.feature.Reason = stateOff, reason
	l.pending = append(l.pending, l.feature)
	l.mu.Unlock()
}

// drainServer shuts the HTTP/3 server down and joins its Serve goroutine. A nil
// shutdownCtx uses a checkTimeout bound and leaves failures to the log. When
// shutdownCtx has a deadline, open streams may run until drainReserve before it;
// the server then closes them, so the steps after the drain keep that time.
func (s *Service) drainServer(shutdownCtx context.Context) {
	l := s.lifecycle
	ctx := shutdownCtx
	cancel := func() {}
	switch {
	case ctx != nil:
		if deadline, ok := ctx.Deadline(); ok {
			ctx, cancel = context.WithDeadline(ctx, deadline.Add(-drainReserve))
		}
	case l.graceless:
		// An ended context makes the server close every connection now.
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
	default:
		ctx, cancel = context.WithTimeout(context.Background(), checkTimeout)
	}
	switch err := l.server.Shutdown(ctx); {
	case err == nil:
	case shutdownCtx != nil && shutdownCtx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
		// The server closed the streams that were still open when the drain
		// time ended. That is the designed cut, not a failure.
		s.cfg.Logger.WarnContext(shutdownCtx, "direct connections: open HTTP/3 streams closed at the drain deadline", "component", "directconn")
	default:
		if shutdownCtx != nil {
			l.shutdownErr = errors.Join(l.shutdownErr, err)
		}
		s.cfg.Logger.WarnContext(ctx, "direct connections: HTTP/3 shutdown failed", "component", "directconn", "error", err)
	}
	cancel()
	<-l.serveDone
	l.serveDone = nil
	l.server = nil
}

// cancelService cancels the service context under mu. Registration in
// readAndStart checks s.ctx under the same lock, so no check worker registers
// after this returns.
func (s *Service) cancelService() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel()
}

// startAsyncReread starts a background settings reread that Shutdown joins:
// the slot is claimed under the lock cancelService takes, so no reread starts
// once shutdown has begun. It starts nothing while a reread runs.
func (s *Service) startAsyncReread() {
	s.mu.Lock()
	if s.rereading || s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.rereading = true
	s.checkWorkers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.checkWorkers.Done()
		s.rereadSettings()
	}()
}

// Publish outside the lifecycle lock, matching Status's service-to-lifecycle
// lock order. Keep transitions until the first probe snapshot exists.
func (s *Service) publishEngineFeatures() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return
	}
	l := s.lifecycle
	l.mu.Lock()
	features := append(l.pending, l.feature)
	l.pending = nil
	l.mu.Unlock()
	// The direct row follows the engine row, so it is republished with it.
	for _, f := range s.last.status.Features {
		if f.Feature == featureDirect {
			direct := []Feature{f}
			s.projectDirect(direct, s.last.status.Enabled)
			features = append(features, direct[0])
		}
	}
	s.logFeatureStates(s.ctx, s.last.status.Enabled, features, false)
}
