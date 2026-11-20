package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

// PluginsChangedEvent is the payload of cache.EventPluginsChanged. An empty
// payload means "something about the installed set changed; reconcile".
// Restart remains accepted from older API replicas. Current senders advance
// plugin_installations.runtime_generation on enablement changes, admin config
// saves, and restarts, then send a plain event; the poll recovers missed events.
type PluginsChangedEvent struct {
	InstallationID int  `json:"installation_id,omitempty"`
	Restart        bool `json:"restart,omitempty"`
}

// DefaultLifecyclePollInterval is how often a following host reconciles
// without an event, so a missed publish (Redis hiccup, restart) costs at most
// one interval.
const DefaultLifecyclePollInterval = time.Minute

// PublishLifecycleChanges makes this service announce every lifecycle change
// on cache.ChannelAdmin as cache.EventPluginsChanged. Call it on hosts that
// make lifecycle changes. The followers' poll catches a missed announcement.
func (s *Service) PublishLifecycleChanges(bus cache.EventBus) {
	if s == nil || bus == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.lifecycleBus = bus
}

func (s *Service) publishPluginsChanged(ctx context.Context, event PluginsChangedEvent) {
	if s == nil {
		return
	}
	s.lifecycleMu.RLock()
	bus := s.lifecycleBus
	s.lifecycleMu.RUnlock()
	if bus == nil {
		return
	}
	payload := ""
	if event != (PluginsChangedEvent{}) {
		encoded, err := json.Marshal(event)
		if err != nil {
			slog.WarnContext(ctx, "encode plugins changed event", "component", "plugins", "error", err)
			return
		}
		payload = string(encoded)
	}
	if err := bus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventPluginsChanged, Payload: payload}); err != nil {
		slog.WarnContext(ctx, "publish plugins changed event", "component", "plugins", "error", err)
	}
}

// FollowLifecycleChanges reconciles committed state on events and on each
// poll, without publishing. Zero selects DefaultLifecyclePollInterval.
// One worker coalesces events and keeps plugin work off the bus goroutine.
//
// The poll is started even when the subscription fails (Redis down at boot,
// for example): the returned error then only says that changes arrive on
// the poll alone, and the caller logs it rather than giving up.
func (s *Service) FollowLifecycleChanges(ctx context.Context, bus cache.EventBus, poll time.Duration) error {
	if s == nil {
		return nil
	}
	if poll <= 0 {
		poll = DefaultLifecyclePollInterval
	}
	follower := &lifecycleFollower{wake: make(chan struct{}, 1)}
	var subscribeErr error
	if bus != nil {
		subscribeErr = bus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
			if event.Type != cache.EventPluginsChanged {
				return
			}
			var payload PluginsChangedEvent
			if event.Payload != "" {
				if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
					slog.WarnContext(ctx, "decode plugins changed event; reconciling anyway", "component", "plugins", "error", err)
					payload = PluginsChangedEvent{}
				}
			}
			follower.enqueue(payload)
		})
	}
	follower.enqueue(PluginsChangedEvent{})
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-follower.wake:
			}
			restarts := follower.drain()
			for _, installationID := range restarts {
				if err := s.resident.Restart(ctx, installationID); err != nil && !errors.Is(err, ErrNotResident) {
					slog.WarnContext(ctx, "restart resident plugin after remote change", "component", "plugins", "installation_id", installationID, "error", err)
				}
			}
			s.reconcileLifecycle(ctx, len(restarts) != 0)
		}
	}()
	return subscribeErr
}

type installationRuntime struct {
	pluginID    string
	version     string
	installPath string
	generation  int64
	enabled     bool
}

type runningInstallations struct {
	mu   sync.RWMutex
	byID map[int]installationRuntime
}

func (r *runningInstallations) load(id int) (installationRuntime, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runtime, ok := r.byID[id]
	return runtime, ok
}

func (r *runningInstallations) store(id int, runtime installationRuntime) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = make(map[int]installationRuntime)
	}
	r.byID[id] = runtime
}

func (r *runningInstallations) delete(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byID, id)
}

func (r *runningInstallations) snapshot() map[int]installationRuntime {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return maps.Clone(r.byID)
}

func runtimeOf(installation *Installation) installationRuntime {
	return installationRuntime{
		pluginID:    installation.PluginID,
		version:     installation.Version,
		installPath: installation.InstallPath,
		generation:  installation.RuntimeGeneration,
		enabled:     installation.Enabled,
	}
}

func (s *Service) reconcileLifecycle(ctx context.Context, force bool) {
	changed := s.reconcileInstallations(ctx)
	if !changed && !force {
		// The proxy host gate and identity can change independently of plugin
		// rows. Reconcile also retries a resident whose manifest was unavailable.
		s.resident.Reconcile(ctx)
	}
	s.runLifecycleHooks(ctx, changed || force)
}

func (s *Service) reconcileInstallations(ctx context.Context) bool {
	if s.installations == nil {
		return false
	}
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	rows, err := s.installations.List(ctx)
	if err != nil {
		slog.WarnContext(ctx, "read plugin lifecycle state", "component", "plugins", "error", err)
		return false
	}
	current := make(map[int]*Installation, len(rows))
	for _, row := range rows {
		if row != nil && !row.IsBuiltin() {
			current[row.ID] = row
		}
	}
	sameRuntime := func(a, b *Installation) bool {
		return a != nil && b != nil && runtimeOf(a) == runtimeOf(b)
	}
	changed := s.reconciled == nil || !maps.EqualFunc(s.reconciled, current, sameRuntime)
	if changed {
		s.InvalidateInstallationCache()
	}
	// Only lazy processes are stopped here. The resident supervisor owns
	// replacement, including its failure budget and host identity.
	for id, runtime := range s.running.snapshot() {
		if row := current[id]; row != nil && runtime == runtimeOf(row) {
			continue
		}
		if err := s.stopStaleLazyRuntime(ctx, id); err != nil {
			slog.WarnContext(ctx, "reconcile plugin process", "component", "plugins", "installation_id", id, "error", err)
		}
	}
	s.archiveCache.retryRetirements(ctx, current)
	if !changed {
		return false
	}
	for id, previous := range s.reconciled {
		if err := s.archiveCache.retire(previous, current[id]); err != nil {
			slog.WarnContext(ctx, "remove retired plugin release from cache", "component", "plugins", "installation_id", id, "error", err)
		}
	}
	s.reconciled = current
	return true
}

func (s *Service) stopStaleLazyRuntime(ctx context.Context, id int) error {
	if _, supervised := s.resident.State(id); supervised {
		return nil
	}
	lock := s.runtimeLock(id)
	if !lock.tryLock() {
		return nil // The next poll checks every tracked runtime again.
	}
	defer lock.Unlock()
	running, ok := s.running.load(id)
	if !ok {
		return nil
	}
	// A new start can have read a more recent commit than our list.
	err := s.checkStartRuntime(ctx, id, running)
	if errors.Is(err, ErrInstallationChanged) {
		err = s.stopRuntime(id)
	}
	if errors.Is(err, pluginhost.ErrClientNotFound) {
		return nil
	}
	return err
}

// lifecycleFollower coalesces pending lifecycle events for the worker in
// FollowLifecycleChanges: any number of plain events become one reconcile,
// and restart events are kept per installation.
type lifecycleFollower struct {
	mu       sync.Mutex
	restarts map[int]struct{}
	wake     chan struct{}
}

func (f *lifecycleFollower) enqueue(event PluginsChangedEvent) {
	f.mu.Lock()
	if event.Restart && event.InstallationID > 0 {
		if f.restarts == nil {
			f.restarts = make(map[int]struct{})
		}
		f.restarts[event.InstallationID] = struct{}{}
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// drain returns the installations to restart, in id order, and clears them.
func (f *lifecycleFollower) drain() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.restarts) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(f.restarts))
	f.restarts = nil
	return ids
}
