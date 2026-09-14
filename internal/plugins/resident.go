package plugins

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// ResidentState is one step of the resident supervisor's per-installation
// state machine: stopped → starting → running, with backoff after a crash
// or a failed start and failed once the consecutive-failure budget is spent.
type ResidentState string

const (
	ResidentStopped  ResidentState = "stopped"
	ResidentStarting ResidentState = "starting"
	ResidentRunning  ResidentState = "running"
	ResidentBackoff  ResidentState = "backoff"
	ResidentFailed   ResidentState = "failed"
)

// RuntimeState is the admin-visible view of one installation's process.
// Resident is true for installations the supervisor owns (started at boot,
// restarted on crash); the other fields describe the supervisor's machine.
// For a lazily started plugin State is running or stopped and the counters
// stay zero.
type RuntimeState struct {
	Resident      bool
	State         ResidentState
	RestartCount  int
	LastError     string
	LastStartedAt *time.Time
	NextRestartAt *time.Time
}

const (
	DefaultResidentMinBackoff  = time.Second
	DefaultResidentMaxBackoff  = time.Minute
	DefaultResidentStableAfter = 5 * time.Minute
	DefaultResidentMaxFailures = 10
)

// ErrNotResident reports a supervisor operation on an installation it does
// not own.
var ErrNotResident = errors.New("plugin installation is not resident")

// residentCapabilityTypes lists the capability types whose installations
// must run as residents: started when the API listener is up, restarted
// after a crash, stopped before the HTTP drain. Network access providers are
// the first kind; later kinds are one more entry here.
var residentCapabilityTypes = []string{capability.NetworkAccessProvider}

// IsResidentCapabilityType reports whether an installation declaring the
// capability type must run as a resident.
func IsResidentCapabilityType(capabilityType string) bool {
	for _, resident := range residentCapabilityTypes {
		if capabilityType == resident {
			return true
		}
	}
	return false
}

// ResidentOptions tunes the supervisor. Zero values take the defaults above.
type ResidentOptions struct {
	MinBackoff  time.Duration
	MaxBackoff  time.Duration
	StableAfter time.Duration
	MaxFailures int
	Logger      *slog.Logger
	// now and jitter are test seams.
	now    func() time.Time
	jitter func(time.Duration) time.Duration
}

func (o ResidentOptions) withDefaults() ResidentOptions {
	if o.MinBackoff <= 0 {
		o.MinBackoff = DefaultResidentMinBackoff
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = DefaultResidentMaxBackoff
	}
	if o.MaxBackoff < o.MinBackoff {
		o.MaxBackoff = o.MinBackoff
	}
	if o.StableAfter <= 0 {
		o.StableAfter = DefaultResidentStableAfter
	}
	if o.MaxFailures <= 0 {
		o.MaxFailures = DefaultResidentMaxFailures
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.jitter == nil {
		o.jitter = defaultBackoffJitter
	}
	return o
}

// defaultBackoffJitter spreads restarts by up to a quarter of the base delay
// so several residents that crashed together do not restart in lockstep.
func defaultBackoffJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(base)/4 + 1))
}

// residentBackoff is the base delay before the failures-th consecutive
// restart: min doubling each time, capped at max.
func residentBackoff(failures int, minBackoff, maxBackoff time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	delay := minBackoff
	for i := 1; i < failures; i++ {
		delay *= 2
		if delay >= maxBackoff || delay <= 0 {
			return maxBackoff
		}
	}
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

type residentEntry struct {
	id          int
	version     string
	installPath string
	// hostIdentity is the host identity the running process was launched
	// under (a proxy's instance-state scope). A change means the process
	// holds another host's in-memory identity and is replaced.
	hostIdentity      string
	runtimeGeneration int64

	state         ResidentState
	failures      int
	restartCount  int
	lastError     string
	lastStartedAt time.Time
	nextRestartAt time.Time

	timer *time.Timer
	// gen invalidates an in-flight start or a pending timer when the entry
	// is reset, halted, or superseded by a newer start.
	gen uint64
}

func (e *residentEntry) snapshot() RuntimeState {
	state := RuntimeState{Resident: true, State: e.state, RestartCount: e.restartCount, LastError: e.lastError}
	if !e.lastStartedAt.IsZero() {
		at := e.lastStartedAt
		state.LastStartedAt = &at
	}
	if e.state == ResidentBackoff && !e.nextRestartAt.IsZero() {
		at := e.nextRestartAt
		state.NextRestartAt = &at
	}
	return state
}

func (e *residentEntry) cancelTimer() {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

// ResidentSupervisor keeps resident installations running. It is armed by
// Enable once the API listener is bound, reconciles the desired set on every
// plugin lifecycle change, learns about crashes from the plugin host's exit
// handler, and is halted before the HTTP drain.
type ResidentSupervisor struct {
	service *Service
	opts    ResidentOptions

	// reconcileMu serializes Reconcile end to end. The desired set and the
	// gate result are computed outside mu (they read the database), so two
	// overlapping reconciles could otherwise apply results in the wrong
	// order: an older one resurrecting a resident a newer one just removed,
	// or starting one after the gate closed. Lifecycle hooks, the proxy's
	// event follower, and the poll all call Reconcile concurrently.
	reconcileMu sync.Mutex

	mu      sync.Mutex
	armed   bool
	halted  bool
	ctx     context.Context
	entries map[int]*residentEntry
	starts  sync.WaitGroup
	// gate, when set, decides whether this host may run residents at all. A
	// proxy whose stream_nodes row is unknown has no instance-state scope to
	// keep overlay node keys under, so it runs none until the row resolves.
	gate    func(ctx context.Context) error
	gateErr string
	// hostIdentity, when set, names the identity residents run under on this
	// host; a proxy returns its node scope. Reconcile replaces every running
	// resident when it changes, since a process keeps the identity it was
	// started with (its overlay node key, its reported node id) in memory.
	hostIdentity func() string
}

// SetResidentGate installs a precondition every reconcile checks before it
// starts residents. While the gate returns an error no resident runs (running
// ones are stopped), the reason is logged once per distinct message, and the
// admin status reports it as the reason the host is unavailable. A nil gate
// clears it.
func (s *Service) SetResidentGate(gate func(ctx context.Context) error) {
	if s == nil || s.resident == nil {
		return
	}
	s.resident.mu.Lock()
	s.resident.gate = gate
	s.resident.mu.Unlock()
}

// SetResidentHostIdentity installs the identity residents run under on this
// host. When the returned value changes between reconciles, every running
// resident is stopped and started again so no process keeps serving under
// a previous identity; a proxy passes its node scope so a deleted and
// re-registered row (new stream_nodes id) restarts its providers.
func (s *Service) SetResidentHostIdentity(identity func() string) {
	if s == nil || s.resident == nil {
		return
	}
	s.resident.mu.Lock()
	s.resident.hostIdentity = identity
	s.resident.mu.Unlock()
}

// GateError returns why the host currently runs no residents, or "" when the
// gate (if any) is open.
func (r *ResidentSupervisor) GateError() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gateErr
}

func newResidentSupervisor(service *Service, opts ResidentOptions) *ResidentSupervisor {
	return &ResidentSupervisor{
		service: service,
		opts:    opts.withDefaults(),
		ctx:     context.Background(),
		entries: make(map[int]*residentEntry),
	}
}

// Enable arms the supervisor and runs the first reconcile. Reconcile is a
// no-op before this so the boot-time lifecycle hooks (preload, dispatcher
// backfill) do not launch residents before the listener they proxy to exists.
func (r *ResidentSupervisor) Enable(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.armed = true
	r.ctx = context.WithoutCancel(ctx)
	r.mu.Unlock()
	r.Reconcile(ctx)
}

// Reconcile aligns the supervised set with the enabled installations that
// declare a resident capability: new residents start, residents that lost
// enablement or the capability stop, and a running resident whose process
// was stopped elsewhere (config save, replace, auto-update) starts again. A
// failed entry stays failed until Restart or Reset.
func (r *ResidentSupervisor) Reconcile(ctx context.Context) {
	if r == nil {
		return
	}
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	r.mu.Lock()
	ready := r.armed && !r.halted
	r.mu.Unlock()
	if !ready {
		return
	}

	r.mu.Lock()
	gate := r.gate
	previousGateErr := r.gateErr
	identity := r.hostIdentity
	r.mu.Unlock()
	hostIdentity := ""
	if identity != nil {
		hostIdentity = identity()
	}

	var (
		desired map[int]*Installation
		gateErr string
	)
	if gate != nil {
		if err := gate(ctx); err != nil {
			gateErr = err.Error()
		}
	}
	if gateErr == "" {
		var err error
		desired, err = r.desiredResidents(ctx)
		if err != nil {
			r.opts.Logger.WarnContext(ctx, "resident plugin reconcile skipped", "component", "plugins", "error", err)
			return
		}
	}

	type relaunch struct {
		id  int
		gen uint64
	}
	var (
		stop    []int
		replace []relaunch
	)
	r.mu.Lock()
	if r.halted {
		r.mu.Unlock()
		return
	}
	r.gateErr = gateErr
	if gateErr != previousGateErr {
		if gateErr != "" {
			r.opts.Logger.WarnContext(ctx, "resident plugins are not started on this host", "component", "plugins", "reason", gateErr)
		} else if gate != nil {
			r.opts.Logger.InfoContext(ctx, "resident plugins may start on this host", "component", "plugins")
		}
	}
	for id, entry := range r.entries {
		if _, keep := desired[id]; keep {
			continue
		}
		entry.cancelTimer()
		entry.gen++
		delete(r.entries, id)
		stop = append(stop, id)
	}
	for id, installation := range desired {
		entry, ok := r.entries[id]
		if !ok {
			entry = &residentEntry{id: id, version: installation.Version, installPath: installation.InstallPath, hostIdentity: hostIdentity, runtimeGeneration: installation.RuntimeGeneration, state: ResidentStopped}
			r.entries[id] = entry
			r.startLocked(entry)
			continue
		}
		if entry.hostIdentity != hostIdentity {
			r.opts.Logger.InfoContext(ctx, "host identity changed; replacing resident plugin process", "component", "plugins",
				"installation_id", id, "previous_identity", entry.hostIdentity, "identity", hostIdentity)
		}
		if entry.version != installation.Version || entry.installPath != installation.InstallPath || entry.hostIdentity != hostIdentity || entry.runtimeGeneration != installation.RuntimeGeneration {
			// A replaced or auto-updated binary gets a fresh failure budget,
			// and the process built from the old binary is stopped before
			// the new one starts. The host that made the change already
			// stopped its own process; on any other host (a proxy node) it
			// is still alive, and ensureClient would keep it because the
			// row and the running manifest agree on nothing it checks.
			entry.version, entry.installPath, entry.hostIdentity, entry.runtimeGeneration = installation.Version, installation.InstallPath, hostIdentity, installation.RuntimeGeneration
			r.resetLocked(entry)
			entry.state = ResidentStarting
			entry.gen++
			// Registered under the lock, like Restart, so Halt always waits
			// for this launch.
			r.starts.Add(1)
			replace = append(replace, relaunch{id: id, gen: entry.gen})
			continue
		}
		switch entry.state {
		case ResidentStopped:
			r.startLocked(entry)
		case ResidentRunning:
			if r.service.host == nil {
				continue
			}
			if _, err := r.service.host.Client(id); errors.Is(err, pluginhost.ErrClientNotFound) {
				r.opts.Logger.InfoContext(ctx, "resident plugin was stopped; starting again", "component", "plugins", "installation_id", id)
				r.startLocked(entry)
			}
		}
	}
	r.mu.Unlock()

	for _, id := range stop {
		r.stopProcess(ctx, id)
	}
	for _, launch := range replace {
		r.opts.Logger.InfoContext(ctx, "resident plugin binary changed; replacing its process", "component", "plugins", "installation_id", launch.id)
		go func(launch relaunch) {
			defer r.starts.Done()
			r.stopProcess(ctx, launch.id)
			r.runStart(context.WithoutCancel(ctx), launch.id, launch.gen)
		}(launch)
	}
}

func (r *ResidentSupervisor) desiredResidents(ctx context.Context) (map[int]*Installation, error) {
	if r.service == nil || r.service.installations == nil {
		return nil, nil
	}
	installations, err := r.service.installations.ListEnabledWithCapabilityTypes(ctx, residentCapabilityTypes)
	if err != nil {
		return nil, fmt.Errorf("list enabled resident installations: %w", err)
	}
	desired := make(map[int]*Installation, len(installations))
	// A provider slug is owned by the lowest enabled installation declaring
	// it (ListNetworkAccessProviders). A later duplicate is not commanded,
	// reported, or listed anywhere, so it must not run either: a resident
	// nobody can disconnect would keep serving ingress unseen. Installations
	// arrive in id order, so the first holder of a slug is the owner.
	slugOwner := make(map[string]int, len(installations))
	for _, installation := range installations {
		if installation == nil || installation.IsBuiltin() {
			continue
		}
		manifest, err := r.service.ensureLoadedInstallation(ctx, installation)
		if err != nil {
			r.opts.Logger.WarnContext(ctx, "resident plugin manifest unavailable; not starting it", "component", "plugins",
				"installation_id", installation.ID, "plugin_id", installation.PluginID, "error", err)
			continue
		}
		if _, slug := pluginhost.NetworkAccessProviderCapability(manifest); slug != "" {
			if owner, dup := slugOwner[slug]; dup {
				r.opts.Logger.WarnContext(ctx, "network access provider slug is already owned by another installation; not starting the duplicate", "component", "plugins",
					"provider", slug, "installation_id", owner, "skipped_installation_id", installation.ID, "plugin_id", installation.PluginID)
				continue
			}
			slugOwner[slug] = installation.ID
		}
		desired[installation.ID] = installation
	}
	return desired, nil
}

// startLocked moves the entry to starting and launches the process on a
// goroutine so callers (admin requests, lifecycle hooks) do not wait on the
// plugin handshake.
func (r *ResidentSupervisor) startLocked(entry *residentEntry) {
	entry.cancelTimer()
	entry.state = ResidentStarting
	entry.nextRestartAt = time.Time{}
	entry.gen++
	gen := entry.gen
	id := entry.id
	ctx := r.ctx
	r.starts.Add(1)
	go func() {
		defer r.starts.Done()
		r.runStart(ctx, id, gen)
	}()
}

// runStart launches the plugin and records the outcome, unless the entry was
// reset, removed, or superseded while the launch was in flight. ensureClient
// reuses a process a lazy RPC already started instead of replacing it.
func (r *ResidentSupervisor) runStart(ctx context.Context, id int, gen uint64) {
	var err error
	accepted := false
	for attempt := 0; attempt < 3; attempt++ {
		floor := r.service.host.NextStartSeq()
		var client pluginClient
		client, err = r.service.ensureClient(ctx, id)
		if err != nil {
			break
		}
		// A client at or below the floor came from an older generation via the
		// singleflight launch join; stop it and let the next attempt relaunch.
		if c, ok := client.(interface{ StartSeq() uint64 }); ok && c.StartSeq() <= floor {
			_ = r.service.host.Stop(id)
			continue
		}
		accepted = true
		break
	}
	if err == nil && !accepted {
		err = fmt.Errorf("superseded launch adopted 3 times")
	}
	r.mu.Lock()
	entry := r.entries[id]
	if entry == nil || entry.gen != gen || r.halted {
		// A superseded launch's process is stopped only when nothing will
		// take it over: the entry is gone, halted, or parked (stopped or
		// failed). While a newer generation is starting, that generation
		// either adopts this process (start sequence above its floor) or
		// stops and relaunches it, so stopping here would kill a process
		// the newer generation may already have accepted.
		orphaned := err == nil && (entry == nil || r.halted || entry.state == ResidentStopped || entry.state == ResidentFailed)
		r.mu.Unlock()
		if orphaned {
			r.stopProcess(ctx, id)
		}
		return
	}
	if err == nil && r.service.host != nil {
		// The process may have died between the handshake and this point;
		// HandleExit ignored it because the entry was still starting.
		if _, clientErr := r.service.host.Client(id); clientErr != nil {
			err = clientErr
		}
	}
	if err != nil {
		r.failLocked(ctx, entry, fmt.Errorf("start: %w", err))
		r.mu.Unlock()
		return
	}
	entry.state = ResidentRunning
	entry.lastStartedAt = r.opts.now()
	entry.lastError = ""
	entry.nextRestartAt = time.Time{}
	r.mu.Unlock()
	r.opts.Logger.InfoContext(ctx, "resident plugin running", "component", "plugins", "installation_id", id)
}

// failLocked counts one failure, resetting the count first when the plugin
// had been running stably, then either schedules a restart with exponential
// backoff plus jitter or parks the entry as failed.
func (r *ResidentSupervisor) failLocked(ctx context.Context, entry *residentEntry, cause error) {
	now := r.opts.now()
	if entry.state == ResidentRunning && !entry.lastStartedAt.IsZero() && now.Sub(entry.lastStartedAt) >= r.opts.StableAfter {
		entry.failures = 0
		entry.restartCount = 0
	}
	entry.failures++
	entry.lastError = cause.Error()
	entry.cancelTimer()
	if entry.failures >= r.opts.MaxFailures {
		entry.state = ResidentFailed
		entry.nextRestartAt = time.Time{}
		entry.gen++
		r.opts.Logger.ErrorContext(ctx, "resident plugin failed; restart it from the admin API", "component", "plugins",
			"installation_id", entry.id, "consecutive_failures", entry.failures, "error", cause)
		return
	}
	delay := residentBackoff(entry.failures, r.opts.MinBackoff, r.opts.MaxBackoff)
	delay += r.opts.jitter(delay)
	entry.state = ResidentBackoff
	entry.restartCount++
	entry.nextRestartAt = now.Add(delay)
	entry.gen++
	gen := entry.gen
	id := entry.id
	entry.timer = time.AfterFunc(delay, func() { r.fireRestart(id, gen) })
	r.opts.Logger.WarnContext(ctx, "resident plugin stopped; restart scheduled", "component", "plugins",
		"installation_id", id, "consecutive_failures", entry.failures, "delay", delay, "error", cause)
}

func (r *ResidentSupervisor) fireRestart(id int, gen uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[id]
	if entry == nil || entry.gen != gen || entry.state != ResidentBackoff || r.halted {
		return
	}
	r.startLocked(entry)
}

// HandleExit is the plugin host's exit handler: a resident whose process
// went away on its own is treated as a failure and rescheduled. Exits of
// non-resident plugins and of entries not in running state (a failed start
// reports through runStart) are ignored.
func (r *ResidentSupervisor) HandleExit(installationID int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[installationID]
	if entry == nil || entry.state != ResidentRunning || r.halted {
		return
	}
	r.failLocked(r.ctx, entry, errors.New("plugin process exited"))
}

// resetLocked clears the failure budget and parks the entry as stopped so the
// next reconcile starts it. Anything in flight for the entry is invalidated.
func (r *ResidentSupervisor) resetLocked(entry *residentEntry) {
	entry.cancelTimer()
	entry.gen++
	entry.failures = 0
	entry.restartCount = 0
	entry.lastError = ""
	entry.nextRestartAt = time.Time{}
	entry.state = ResidentStopped
}

// Reset forgets an entry's failure history, for a reconfigure that should
// give a failed resident a fresh budget. The following Reconcile starts it.
func (r *ResidentSupervisor) Reset(installationID int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.entries[installationID]; entry != nil {
		r.resetLocked(entry)
	}
}

// Restart stops the resident's process, clears its failure budget and starts
// it again, waiting for the launch so the caller's next read sees running or
// the recorded failure. A launch failure is recorded in the entry (state
// backoff or failed, LastError set) rather than returned. ErrNotResident is
// returned for installations the supervisor does not own.
func (r *ResidentSupervisor) Restart(ctx context.Context, installationID int) error {
	if r == nil {
		return ErrNotResident
	}
	r.mu.Lock()
	entry := r.entries[installationID]
	if entry == nil || r.halted {
		r.mu.Unlock()
		return ErrNotResident
	}
	r.resetLocked(entry)
	entry.state = ResidentStarting
	entry.gen++
	gen := entry.gen
	// Registered under the lock so Halt, which flips halted under the same
	// lock before waiting, always sees this launch.
	r.starts.Add(1)
	r.mu.Unlock()
	defer r.starts.Done()

	r.stopProcess(ctx, installationID)
	r.runStart(context.WithoutCancel(ctx), installationID, gen)
	return nil
}

// setRuntimeGeneration records the installation's persisted runtime
// generation on its entry, for a host that is about to restart the process
// itself and must not treat the bump it just wrote as someone else's.
func (r *ResidentSupervisor) setRuntimeGeneration(installationID int, generation int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry := r.entries[installationID]; entry != nil {
		entry.runtimeGeneration = generation
	}
}

// Halt stops every resident and refuses further starts. It runs before the
// HTTP servers drain so overlay ingress goes away first, and waits (bounded
// by ctx) for in-flight launches to settle.
func (r *ResidentSupervisor) Halt(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.halted = true
	ids := make([]int, 0, len(r.entries))
	for id, entry := range r.entries {
		entry.cancelTimer()
		entry.gen++
		entry.state = ResidentStopped
		entry.nextRestartAt = time.Time{}
		ids = append(ids, id)
	}
	r.mu.Unlock()

	for _, id := range ids {
		r.stopProcess(ctx, id)
	}
	done := make(chan struct{})
	go func() {
		r.starts.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State reports the supervisor's view of one installation; ok is false when
// the installation is not resident.
func (r *ResidentSupervisor) State(installationID int) (RuntimeState, bool) {
	if r == nil {
		return RuntimeState{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := r.entries[installationID]
	if entry == nil {
		return RuntimeState{}, false
	}
	return entry.snapshot(), true
}

func (r *ResidentSupervisor) stopProcess(ctx context.Context, installationID int) {
	if r.service == nil || r.service.host == nil {
		return
	}
	if err := r.service.host.Stop(installationID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
		r.opts.Logger.WarnContext(ctx, "stop resident plugin", "component", "plugins", "installation_id", installationID, "error", err)
	}
}

// Residents returns the resident supervisor.
func (s *Service) Residents() *ResidentSupervisor {
	if s == nil {
		return nil
	}
	return s.resident
}

// StartResidents arms the supervisor once the API listener is bound.
func (s *Service) StartResidents(ctx context.Context) {
	if s == nil {
		return
	}
	s.resident.Enable(ctx)
}

// StopResidents halts the supervisor and stops every resident; call it
// before the HTTP servers drain.
func (s *Service) StopResidents(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.resident.Halt(ctx)
}

// HandleResidentExit is wired as the plugin host's exit handler.
func (s *Service) HandleResidentExit(installationID int) {
	if s == nil {
		return
	}
	s.resident.HandleExit(installationID)
}

// RuntimeState reports the process state of one installation for the admin
// API. A supervised resident answers from the supervisor; anything else is
// running or stopped as the host sees it, with Resident false. A caller that
// knows the installation's capabilities (a disabled resident has no
// supervisor entry) sets Resident from IsResidentCapabilityType itself.
func (s *Service) RuntimeState(installationID int) RuntimeState {
	state := RuntimeState{State: ResidentStopped}
	if s == nil {
		return state
	}
	if tracked, ok := s.resident.State(installationID); ok {
		return tracked
	}
	if s.host != nil {
		if _, err := s.host.Client(installationID); err == nil {
			state.State = ResidentRunning
		}
	}
	return state
}

// RestartInstallation stops the installation's process and, for a resident,
// starts it again immediately with a fresh failure budget. A non-resident
// plugin is only stopped; its next RPC launches it lazily.
func (s *Service) RestartInstallation(ctx context.Context, installationID int) error {
	if s == nil {
		return nil
	}
	if _, err := s.loadInstallation(ctx, installationID, true); err != nil {
		return err
	}
	// The restart is durable: advancing runtime_generation lets a host whose
	// lifecycle subscription missed the event (a proxy with Redis down)
	// replace its process on the next poll, and clears a failed entry's
	// budget there the same way the event would.
	durable := false
	if s.installations != nil {
		if err := s.installations.Update(ctx, installationID, UpdateInstallationInput{Restart: true}); err != nil {
			return fmt.Errorf("record plugin restart: %w", err)
		}
		s.invalidateInstallationCache()
		if installation, err := s.loadInstallation(ctx, installationID, true); err == nil {
			// This host restarts synchronously below; record the new
			// generation on its entry so the next reconcile does not
			// replace the fresh process a second time.
			s.resident.setRuntimeGeneration(installationID, installation.RuntimeGeneration)
			durable = true
		}
	}
	// Every other host running the resident (proxy nodes) replaces its
	// process too, so one admin action clears a failed instance everywhere.
	// With the generation persisted a plain event is enough: the follower's
	// reconcile sees the new generation and replaces its process once. A
	// restart event on top of that would make it restart, then reconcile and
	// replace the fresh process again for the generation it had not
	// recorded. Without a durable generation the event carries the restart.
	// Published whether or not this host runs the resident itself: the
	// restart is recorded regardless of what this host does with it.
	defer s.publishPluginsChanged(ctx, PluginsChangedEvent{InstallationID: installationID, Restart: !durable})
	err := s.resident.Restart(ctx, installationID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrNotResident) {
		return err
	}
	if s.host == nil {
		return nil
	}
	if err := s.host.Stop(installationID); err != nil && !errors.Is(err, pluginhost.ErrClientNotFound) {
		return fmt.Errorf("stop plugin installation %d: %w", installationID, err)
	}
	return nil
}
