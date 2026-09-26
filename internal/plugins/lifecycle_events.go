package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

// PluginsChangedEvent is the payload of cache.EventPluginsChanged. An empty
// payload means "something about the installed set changed; reconcile".
// Restart remains accepted from older API replicas. Current senders advance
// plugin_installations.runtime_generation on admin config saves and restarts,
// then send a plain reconcile event; the poll can recover missed events.
type PluginsChangedEvent struct {
	InstallationID int  `json:"installation_id,omitempty"`
	Restart        bool `json:"restart,omitempty"`
}

// DefaultLifecyclePollInterval is how often a following host reconciles
// without an event, so a missed publish (Redis hiccup, restart) costs at most
// one interval.
const DefaultLifecyclePollInterval = time.Minute

// PublishLifecycleChanges makes this service announce every lifecycle change
// on cache.ChannelAdmin as cache.EventPluginsChanged. The API server calls
// it; proxy nodes follow with FollowLifecycleChanges. Publishing is
// best-effort: a failed publish is logged and the followers' poll catches up.
func (s *Service) PublishLifecycleChanges(bus cache.EventBus) {
	if s == nil || bus == nil {
		return
	}
	s.lifecycleBus = bus
	s.AddLifecycleHook(func(ctx context.Context) { s.publishPluginsChanged(ctx, PluginsChangedEvent{}) })
}

func (s *Service) publishPluginsChanged(ctx context.Context, event PluginsChangedEvent) {
	if s == nil || s.lifecycleBus == nil {
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
	if err := s.lifecycleBus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventPluginsChanged, Payload: payload}); err != nil {
		slog.WarnContext(ctx, "publish plugins changed event", "component", "plugins", "error", err)
	}
}

// FollowLifecycleChanges makes this service (a proxy node's) reconcile on
// every cache.EventPluginsChanged published on cache.ChannelAdmin and, as a
// backstop, every poll interval (DefaultLifecyclePollInterval when poll is
// zero). Both run OnLifecycleChange, which drops the installation cache and
// reconciles the resident set; an event naming an installation to restart
// replaces that process first. Events are handled by one worker off the bus
// goroutine, so a slow plugin handshake never delays other subscribers, and
// a burst of admin actions collapses into one reconcile (plus one restart
// per named installation) rather than a reconcile per event.
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
	go func() {
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				follower.enqueue(PluginsChangedEvent{})
			case <-follower.wake:
			}
			restarts := follower.drain()
			for _, installationID := range restarts {
				if err := s.resident.Restart(ctx, installationID); err != nil && !errors.Is(err, ErrNotResident) {
					slog.WarnContext(ctx, "restart resident plugin after remote change", "component", "plugins", "installation_id", installationID, "error", err)
				}
			}
			s.OnLifecycleChange(ctx)
		}
	}()
	return subscribeErr
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
	ids := make([]int, 0, len(f.restarts))
	for id := range f.restarts {
		ids = append(ids, id)
	}
	f.restarts = nil
	sort.Ints(ids)
	return ids
}
