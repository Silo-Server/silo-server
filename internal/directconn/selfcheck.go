package directconn

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/darthShadow/shunpike/engine"
	"github.com/darthShadow/shunpike/engine/n4"
	"github.com/quic-go/quic-go/http3"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/tuning"
)

// NATProber measures STUN reachability using only the supplied servers.
type NATProber func(context.Context, *slog.Logger, []string) (engine.NATReport, error)

// PortMapProber discovers router services without creating a mapping.
type PortMapProber func(context.Context, *slog.Logger) (engine.PortMappingReport, error)

// CapabilitySource reads local socket capabilities without network traffic.
type CapabilitySource func(context.Context, *slog.Logger) (engine.CapabilityReport, error)

// Config supplies settings, startup facts, and optional probe implementations.
// Nil probe functions use the engine probes. Settings is required.
type Config struct {
	// SecretKey is the bootstrap master key, copied before deriving the API node identity.
	SecretKey []byte
	// ServerFactory constructs a fresh HTTP/3 server for each engine start.
	// Nil leaves the UDP engine without an HTTP listener until one is supplied.
	ServerFactory func(*engine.Engine, n4.Policy) *http3.Server
	newEngine     func(context.Context, engine.Config) (engineHandle, error)
	newServer     func(engineHandle, n4.Policy) engineServer
	Settings      SettingsStore
	Logger        *slog.Logger
	Tuning        tuning.Config
	Adjustments   []tuning.Adjustment
	HeldAtStart   bool
	NAT           NATProber
	PortMap       PortMapProber
	Capabilities  CapabilitySource
	// ReplicaPresence is the process marker already registered by main. A nil
	// RedisClient disables the census and lease for a standalone deployment.
	ReplicaPresence *cache.APIReplicaPresence
	RedisClient     *redis.Client
	// RedisUnavailable reports that Redis is configured but its client could not
	// be built. The gate then fails closed with replica_census_unavailable
	// instead of reading a missing client as a deployment without Redis.
	RedisUnavailable bool
	// RequireRedis makes a missing RedisClient fail closed the same way: with
	// several API processes and no shared lease the engine could run twice. Main
	// sets it for server.mode api; a single integrated process runs without Redis.
	RequireRedis bool
	// AwaitHandler holds every engine start until HandlerReady is called, so the
	// engine binds its UDP port only once the HTTP handler it serves exists.
	AwaitHandler bool
	// recheckInterval is the period of the service's own settings reread. Zero
	// means settingsRecheck; a negative value turns the reread off, for tests.
	recheckInterval time.Duration
}

type checkRun struct {
	done   chan struct{}
	values settings
	status Status
}

// Service serializes checks under its lifetime context and publishes snapshots.
type Service struct {
	ctx               context.Context //nolint:containedctx // The service owns asynchronous checks; request contexts bound only reads and waits.
	cancel            context.CancelFunc
	checkWorkers      sync.WaitGroup
	cfg               Config
	mu                sync.Mutex
	settingsGate      chan struct{}
	running           *checkRun
	last              *checkRun
	followUp          bool
	readPending       bool      // a settings read failed; the next Status reads again
	rereading         bool      // a background reread runs; Status starts no second
	readAt            time.Time // last successful settings read; Status rereads after settingsRecheck
	listeners         []Listener
	logged            map[string]logKey
	replica           *replicaMonitor
	replicaEventsDone chan struct{}
	lifecycle         *engineState
	redisUnavailable  bool // Redis is configured but has no usable client or presence identity

	// readFailures counts failed reads and failed gate waits. A read clears
	// readPending only if the count did not change while it ran.
	readFailures atomic.Uint64

	// beforeFailureRecorded, when set, runs before markReadFailed takes mu. Tests
	// use it to hold a failed read at the recording boundary.
	beforeFailureRecorded func()
}

// New starts the minute settings recheck, without running probes. With Redis it
// prepares the replica census and lease, which run only while direct connections
// are on. The caller starts the first check through SettingsChanged; canceling
// ctx starts ordered shutdown. Call Shutdown to drain with a caller's deadline
// and wait for the service to finish.
func New(ctx context.Context, cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.NAT == nil {
		cfg.NAT = engine.CheckNAT
	}
	if cfg.PortMap == nil {
		cfg.PortMap = engine.ProbePortMapping
	}
	if cfg.Capabilities == nil {
		cfg.Capabilities = engine.ProbeCapabilities
	}
	cfg.SecretKey = append([]byte(nil), cfg.SecretKey...)
	cfg.Adjustments = append([]tuning.Adjustment(nil), cfg.Adjustments...)
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{ctx: ctx, cancel: cancel, cfg: cfg, settingsGate: make(chan struct{}, 1), logged: make(map[string]logKey)}
	switch {
	case cfg.RedisUnavailable || (cfg.RedisClient != nil && cfg.ReplicaPresence.ID() == ""):
		// Without a client or an identity there is no census or lease to read, and a
		// shared lease value would let one replica release another's lease.
		s.redisUnavailable = true
		cfg.Logger.WarnContext(ctx, "direct connections: Redis is configured but unusable for the replica census; the engine stays off", "component", "directconn")
	case cfg.RedisClient == nil && cfg.RequireRedis:
		s.redisUnavailable = true
		cfg.Logger.WarnContext(ctx, "direct connections: this server mode needs Redis for the engine lease; the engine stays off", "component", "directconn")
	case cfg.RedisClient != nil:
		s.replica = newReplicaMonitor(cfg.ReplicaPresence, &redisEngineLease{client: cfg.RedisClient, id: cfg.ReplicaPresence.ID()}, cfg.Logger)
		s.replicaEventsDone = make(chan struct{})
	}
	// The lifecycle exists before any goroutine that wakes it starts.
	s.initEngine()
	if s.replica != nil {
		// Only the lifecycle starts and stops the monitor; it stops it after
		// draining HTTP/3.
		go s.replicaEvents()
	}
	interval := cfg.recheckInterval
	if interval == 0 {
		interval = settingsRecheck
	}
	if interval > 0 {
		// Shutdown joins it with the check workers after canceling ctx.
		s.checkWorkers.Add(1)
		go s.recheckSettings(interval)
	}
	return s
}

// recheckSettings rereads settings on every tick, whether or not the feature is
// on, so a missed settings-change event or a failed read reaches the engine and
// the snapshot within one interval. A tick while a reread runs does nothing.
func (s *Service) recheckSettings(interval time.Duration) {
	defer s.checkWorkers.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			if s.claimReread() {
				s.rereadSettings()
			}
		}
	}
}

// claimReread takes the rereading flag. It reports false when a reread already
// runs, so callers start no second one.
func (s *Service) claimReread() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rereading {
		return false
	}
	s.rereading = true
	return true
}

// SettingsChanged reads effective settings under ctx and starts a check only on
// a difference. It never waits for probes. Changes during a check coalesce into
// one reread after that check; probes run under the service's lifetime context.
func (s *Service) SettingsChanged(ctx context.Context) error {
	_, err := s.reconcile(ctx, false, true)
	return err
}

// reconcile bounds the gate wait and the settings read by checkTimeout and ends
// them with the service context, for every caller; the check it starts runs on
// s.ctx and does not inherit the deadline or the caller's cancellation.
// A failed call sets readPending so the next Status reads settings again. An
// event read, from a settings-change signal, also retries a failed engine start.
func (s *Service) reconcile(ctx context.Context, refresh, event bool) (*checkRun, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	return s.readAndStart(ctx, refresh, event)
}

// markReadFailed sets readPending and counts the failure, so a read that began
// earlier cannot clear it.
func (s *Service) markReadFailed() {
	if s.beforeFailureRecorded != nil {
		s.beforeFailureRecorded()
	}
	s.mu.Lock()
	s.readPending = true
	s.readFailures.Add(1)
	s.mu.Unlock()
}

// Serialize settings reads through publication so a slow older read cannot
// replace newer settings. Waiting readers remain cancelable without holding mu.
// A failed read is recorded before the gate is released, so the next reader sees
// it. A caller that fails before it holds the gate records its own failure, which
// may land while another read holds the gate: that read clears readPending only
// if no failure was recorded since it began.
func (s *Service) readAndStart(ctx context.Context, refresh, event bool) (*checkRun, error) {
	select {
	case s.settingsGate <- struct{}{}:
		defer func() { <-s.settingsGate }()
	case <-ctx.Done():
		s.markReadFailed()
		return nil, ctx.Err()
	case <-s.ctx.Done():
		s.markReadFailed()
		return nil, s.ctx.Err()
	}
	failuresBefore := s.readFailures.Load()
	if refresh {
		// A refresh joins a running check without a read, unless an earlier read
		// failed: then it reads, so the change is scheduled behind that check.
		s.mu.Lock()
		run, pending := s.running, s.readPending
		s.mu.Unlock()
		if run != nil && !pending {
			return run, nil
		}
	}
	values, err := readSettings(ctx, s.cfg.Settings)
	if err != nil {
		s.markReadFailed()
		return nil, err
	}
	s.setEngineSettings(values, event)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readAt = time.Now()
	if s.readFailures.Load() == failuresBefore {
		s.readPending = false
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if s.running != nil {
		if values != s.running.values {
			s.followUp = true
		}
		return s.running, nil
	}
	if !refresh && s.last != nil && values == s.last.values {
		return s.last, nil
	}
	return s.startLocked(values), nil
}

func (s *Service) startLocked(values settings) *checkRun {
	run := &checkRun{done: make(chan struct{}), values: values}
	s.running = run
	s.checkWorkers.Add(1)
	go s.run(run)
	return run
}

// Status returns the last completed snapshot. Refresh starts a new check or
// joins the current one; after a failed read it reads settings before joining
// and returns the read error if that read fails.
// With no snapshot it waits for the first check. Caller cancellation stops only
// that wait; the shared check has its own 15 s bound, and the settings read that
// starts it has a separate 15 s bound. A caller whose context has ended gets its
// error. After a failed settings read, a call without refresh that has a
// snapshot returns it at once and rereads settings in the background (one reread
// at a time, on the service's lifetime context, bounded by checkTimeout). The
// reread starts a check when the values changed; a later call returns it once it
// completes. If the reread fails, the snapshot stands and the next call tries
// again. The same background reread runs once the last successful settings read
// is settingsRecheck old, so a missed settings-change event cannot hold the
// snapshot on old settings indefinitely.
func (s *Service) Status(ctx context.Context, refresh bool) (Status, error) {
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	run := s.last
	snapshot := run != nil && !refresh
	if !snapshot {
		run = s.running
	}
	pending := s.readPending
	aged := time.Since(s.readAt) >= settingsRecheck
	// The reread holds a check-worker slot, claimed before shutdown cancels the
	// service, so Shutdown joins it.
	reread := snapshot && (pending || aged) && !s.rereading && s.ctx.Err() == nil
	if reread {
		s.rereading = true
		s.checkWorkers.Add(1)
	}
	s.mu.Unlock()
	if reread {
		go func() {
			defer s.checkWorkers.Done()
			s.rereadSettings()
		}()
	}
	if run == nil || (pending && !snapshot) {
		fresh, err := s.reconcile(ctx, refresh, false)
		switch {
		case err != nil && (run == nil || refresh):
			return Status{}, err
		case err == nil && !snapshot:
			run = fresh
		}
	}
	select {
	case <-ctx.Done():
		return Status{}, ctx.Err()
	case <-s.ctx.Done():
		return Status{}, s.ctx.Err()
	case <-run.done:
	}
	// With both ready, select picks at random: the caller's end still wins.
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := run.status
	out.Features = append([]Feature(nil), out.Features...)
	s.projectDirect(out.Features, out.Enabled)
	out.Features = append(out.Features, s.engineFeature())
	out.Listeners = append([]Listener(nil), s.listeners...)
	out.Knobs = statusKnobs(s.cfg.Tuning, s.cfg.Adjustments)
	return out, nil
}

// rereadSettings rereads settings in the background for a Status call that
// returned its snapshot or for the minute recheck. Its caller sets rereading
// before starting it. A failed read keeps the last good values and leaves
// readPending set for the next call.
func (s *Service) rereadSettings() {
	_, err := s.reconcile(s.ctx, false, false)
	s.mu.Lock()
	s.rereading = false
	s.mu.Unlock()
	if err != nil && s.ctx.Err() == nil {
		s.cfg.Logger.WarnContext(s.ctx, "direct connections: settings reread failed", "component", "directconn", "error", err)
	}
}

const checkTimeout = 15 * time.Second

// settingsRecheck is the period of the service's own settings reread and the
// age after which a status read rereads settings, so a missed settings-change
// event leaves the engine and the snapshot on old settings for at most about a
// minute plus one bounded read.
const settingsRecheck = time.Minute

func (s *Service) run(run *checkRun) {
	defer s.checkWorkers.Done()
	ctx, cancel := context.WithTimeout(s.ctx, checkTimeout)
	defer cancel()
	caps, capErr := s.cfg.Capabilities(ctx, s.cfg.Logger)
	if caps.Platform == "" {
		caps.Platform = runtime.GOOS
	}
	var nat engine.NATReport
	var natErr error
	var mapping engine.PortMappingReport
	var mappingErr error
	if run.values.enabled {
		nat, natErr = s.cfg.NAT(ctx, s.cfg.Logger, run.values.servers())
		if run.values.portMapping {
			mapping, mappingErr = s.cfg.PortMap(ctx, s.cfg.Logger)
		}
	}
	status := Status{Enabled: run.values.enabled, CheckedAt: time.Now(), Features: featureStates(run.values, s.cfg.HeldAtStart, caps, capErr, nat, natErr, mapping, mappingErr)}
	s.mu.Lock()
	features := append([]Feature(nil), status.Features...)
	s.projectDirect(features, status.Enabled)
	features = append(features, s.engineFeature())
	s.logFeatures(s.ctx, run.values.enabled, features)
	run.status = status
	s.last = run
	s.running = nil
	close(run.done)
	followUp := s.followUp
	s.followUp = false
	s.mu.Unlock()
	if !followUp {
		return
	}
	if s.ctx.Err() != nil {
		return
	}
	reloadCtx, reloadCancel := context.WithTimeout(s.ctx, checkTimeout)
	defer reloadCancel()
	// The follow-up is a read the service schedules itself, not a settings-change
	// event, so it does not retry a failed engine start unless values changed.
	if _, err := s.reconcile(reloadCtx, false, false); err != nil {
		s.cfg.Logger.WarnContext(s.ctx, "direct connections: settings reload failed", "component", "directconn", "error", err)
	}
}
