package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type eventConsumerClient interface {
	HandleEvent(ctx context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error)
}

type eventConsumerResolver interface {
	EventConsumerClient(ctx context.Context, installationID int, capabilityID string) (eventConsumerClient, error)
}

// EventDispatcher delivers host events to plugin event_consumer.v1 capabilities
// whose manifest "subscriptions" list matches the event name. It listens on
// two sources:
//
//   - The cache.EventBus channels (catalog/admin/playback) for events that
//     direct callers — host code in api/handlers and worker packages —
//     publish via the bus.
//   - The events.Hub for envelopes routed through the in-process hub. This is
//     the path plugin-published events take (RuntimeHostServer.PublishEvent
//     stamps `plugin.<id>.` and writes an envelope on ChannelPlugins). Without
//     this subscription, plugin↔plugin events never reach consumers.
//
// Dispatch is fan-out only: every subscriber to event_name receives the event.
// Plugins wanting to address a single peer should embed a target identifier
// in the payload and have subscribers filter on it.
type EventDispatcher struct {
	bus           cache.EventBus
	hub           *events.Hub
	installations taskInstallationStore
	resolver      eventConsumerResolver
	concurrency   int
	sem           chan struct{}

	hubUnsubscribe func()

	// index maps an event name to its subscribers so an event costs a map
	// lookup instead of 1+N store reads on every replica. It is
	// built from the store on first use and dropped by invalidateIndex after
	// every lifecycle change on this replica (Service.SetEventDispatcher
	// registers the hook) and on every cache.EventPluginsChanged, which is how
	// another replica's change arrives. An index older than indexMaxAge is
	// rebuilt, so a missed publish costs at most one interval.
	//
	// indexGen guards the rebuild like installationCacheGen guards the
	// installation cache: an index read before an invalidation serves the
	// event that built it but is never stored. rebuildMu lets one caller
	// rebuild while events that arrive meanwhile wait for its result.
	indexMu     sync.RWMutex
	index       map[string][]eventSubscriber
	indexBuilt  time.Time
	indexGen    uint64
	indexMaxAge time.Duration
	rebuildMu   sync.Mutex
	now         func() time.Time
}

// eventSubscriber is the event_consumer.v1 capability an installation
// receives one event name on.
type eventSubscriber struct {
	installationID int
	pluginID       string
	capabilityID   string
}

func NewEventDispatcher(
	bus cache.EventBus,
	hub *events.Hub,
	installations taskInstallationStore,
	resolver eventConsumerResolver,
	concurrency int,
) *EventDispatcher {
	if concurrency < 1 {
		concurrency = 4
	}
	return &EventDispatcher{
		bus:           bus,
		hub:           hub,
		installations: installations,
		resolver:      resolver,
		concurrency:   concurrency,
		sem:           make(chan struct{}, concurrency),
		indexMaxAge:   DefaultLifecyclePollInterval,
		now:           time.Now,
	}
}

func NewEventDispatcherWithTypedResolver(
	bus cache.EventBus,
	hub *events.Hub,
	installations taskInstallationStore,
	resolver interface {
		EventConsumerClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.EventConsumerClient, error)
	},
	concurrency int,
) *EventDispatcher {
	return NewEventDispatcher(bus, hub, installations, eventConsumerResolverFunc(func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (eventConsumerClient, error) {
		return resolver.EventConsumerClient(ctx, installationID, capabilityID)
	}), concurrency)
}

func (d *EventDispatcher) Start(ctx context.Context) error {
	for _, channel := range []string{cache.ChannelCatalog, cache.ChannelAdmin, cache.ChannelPlayback} {
		channel := channel
		if err := d.bus.Subscribe(ctx, channel, func(event cache.Event) {
			d.dispatchBusEvent(ctx, event)
		}); err != nil {
			return fmt.Errorf("subscribe plugin event dispatcher to %s: %w", channel, err)
		}
	}

	if d.hub != nil {
		envCh, unsub := d.hub.Subscribe()
		d.hubUnsubscribe = unsub
		go d.consumeHub(ctx, envCh)
	}
	return nil
}

func (d *EventDispatcher) Stop() {
	if d.hubUnsubscribe != nil {
		d.hubUnsubscribe()
		d.hubUnsubscribe = nil
	}
}

func (d *EventDispatcher) consumeHub(ctx context.Context, ch <-chan events.Envelope) {
	for {
		select {
		case <-ctx.Done():
			return
		case env, ok := <-ch:
			if !ok {
				return
			}
			d.dispatchEnvelope(ctx, env)
		}
	}
}

func (d *EventDispatcher) dispatchBusEvent(ctx context.Context, event cache.Event) {
	if event.Type == cache.EventPluginsChanged {
		d.invalidateIndex()
	}
	env := events.Envelope{Event: event.Type}
	if subscribers := d.subscribersFor(ctx, env); len(subscribers) > 0 {
		d.fanOut(ctx, env, subscribers, decodeStringPayload(event.Payload))
	}
}

func (d *EventDispatcher) dispatchEnvelope(ctx context.Context, env events.Envelope) {
	if subscribers := d.subscribersFor(ctx, env); len(subscribers) > 0 {
		d.fanOut(ctx, env, subscribers, decodeRawJSON(env.Data))
	}
}

// decodeStringPayload tries to JSON-decode a cache.Event.Payload string into a
// structpb. Plugin consumers expect top-level fields (e.g. p["libraryId"]), so
// when the payload parses we pass it through as-is. If it doesn't parse — the
// payload is an opaque id rather than JSON — we still hand the raw value over
// under "raw" so the consumer can ignore it without misreading nil fields.
func decodeStringPayload(raw string) *structpb.Struct {
	if raw == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err == nil {
		s, _ := structpb.NewStruct(m)
		return s
	}
	s, _ := structpb.NewStruct(map[string]any{"raw": raw})
	return s
}

func decodeRawJSON(raw json.RawMessage) *structpb.Struct {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	s, _ := structpb.NewStruct(m)
	return s
}

// subscribersFor returns the subscribers env is delivered to: the indexed
// subscribers of env.Event, narrowed to env.TargetPluginID when it is set.
func (d *EventDispatcher) subscribersFor(ctx context.Context, env events.Envelope) []eventSubscriber {
	subscribers, err := d.subscribers(ctx, env.Event)
	if err != nil {
		slog.WarnContext(ctx, "plugin event dispatcher: list installations failed", "component", "plugins", "error", err)
		return nil
	}
	if env.TargetPluginID == "" {
		return subscribers
	}
	var targeted []eventSubscriber
	for _, subscriber := range subscribers {
		if subscriber.pluginID == env.TargetPluginID {
			targeted = append(targeted, subscriber)
		}
	}
	return targeted
}

// subscribers returns the indexed subscribers of eventName, rebuilding the
// index from the store when it is missing or older than indexMaxAge. The
// returned slice is shared with the index and must not be modified.
func (d *EventDispatcher) subscribers(ctx context.Context, eventName string) ([]eventSubscriber, error) {
	if index, ok := d.currentIndex(); ok {
		return index[eventName], nil
	}
	d.rebuildMu.Lock()
	defer d.rebuildMu.Unlock()
	if index, ok := d.currentIndex(); ok {
		return index[eventName], nil
	}

	d.indexMu.RLock()
	gen := d.indexGen
	d.indexMu.RUnlock()
	builtAt := d.now()
	index, complete, err := d.buildIndex(ctx)
	if err != nil {
		return nil, err
	}
	// Store the index only if it read every installation and no invalidation
	// landed while it was read; otherwise it may predate a just-committed
	// lifecycle change. It still serves this event either way.
	d.indexMu.Lock()
	if complete && d.indexGen == gen {
		d.index = index
		d.indexBuilt = builtAt
	}
	d.indexMu.Unlock()
	return index[eventName], nil
}

func (d *EventDispatcher) currentIndex() (map[string][]eventSubscriber, bool) {
	d.indexMu.RLock()
	defer d.indexMu.RUnlock()
	if d.index == nil || d.now().Sub(d.indexBuilt) >= d.indexMaxAge {
		return nil, false
	}
	return d.index, true
}

// invalidateIndex drops the subscriber index so the next event rebuilds it
// from the store.
func (d *EventDispatcher) invalidateIndex() {
	d.indexMu.Lock()
	d.index = nil
	d.indexGen++
	d.indexMu.Unlock()
}

// buildIndex maps every event name an enabled installation subscribes to onto
// that installation's first event_consumer.v1 capability listing it, which
// preserves one delivery per installation. Subscribers stay in installation
// order. complete is false when an installation's capabilities could not be
// read; that installation is left out and the caller must not keep the index.
func (d *EventDispatcher) buildIndex(ctx context.Context) (index map[string][]eventSubscriber, complete bool, err error) {
	installations, err := d.installations.ListEnabled(ctx)
	if err != nil {
		return nil, false, err
	}

	index = make(map[string][]eventSubscriber)
	complete = true
	for _, installation := range installations {
		// Builtin installations are metadata-only and never launchable;
		// defense-in-depth alongside the capability-type filter below.
		if installation.IsBuiltin() {
			continue
		}

		capabilities, err := d.installations.ListCapabilities(ctx, installation.ID)
		if err != nil {
			slog.WarnContext(ctx, "plugin event dispatcher: list capabilities failed", "component", "plugins", "installation_id", installation.ID, "error", err)
			complete = false
			continue
		}

		indexed := make(map[string]struct{})
		for _, capability := range capabilities {
			if capability == nil || capability.Type != "event_consumer.v1" {
				continue
			}
			for _, eventName := range subscriptionsOf(capability) {
				if _, ok := indexed[eventName]; ok {
					continue
				}
				indexed[eventName] = struct{}{}
				index[eventName] = append(index[eventName], eventSubscriber{
					installationID: installation.ID,
					pluginID:       installation.PluginID,
					capabilityID:   capability.ID,
				})
			}
		}
	}
	return index, complete, nil
}

// fanOut delivers env to each subscriber and waits for every delivery.
func (d *EventDispatcher) fanOut(ctx context.Context, env events.Envelope, subscribers []eventSubscriber, payload *structpb.Struct) {
	var wg sync.WaitGroup
	for _, subscriber := range subscribers {
		wg.Add(1)
		d.sem <- struct{}{}
		go func(installationID int, capabilityID string) {
			defer wg.Done()
			defer func() { <-d.sem }()

			client, err := d.resolver.EventConsumerClient(ctx, installationID, capabilityID)
			if err != nil {
				slog.WarnContext(ctx, "plugin event dispatcher: resolve client failed", "component", "plugins", "installation_id", installationID, "capability_id", capabilityID, "error", err)
				return
			}

			if _, err := client.HandleEvent(ctx, &pluginv1.HandleEventRequest{
				EventName: env.Event,
				Payload:   payload,
			}); err != nil {
				slog.WarnContext(ctx, "plugin event dispatcher: delivery failed", "component", "plugins", "installation_id", installationID, "capability_id", capabilityID, "error", err)
			}
		}(subscriber.installationID, subscriber.capabilityID)
	}
	wg.Wait()
}

// subscriptionsOf returns the event names in the capability's manifest
// "subscriptions" list, or nil when the list is missing or malformed.
func subscriptionsOf(capability *Capability) []string {
	subscriptions, ok := capability.Metadata["subscriptions"]
	if !ok {
		return nil
	}
	values, err := toStringSlice(subscriptions)
	if err != nil {
		return nil
	}
	return values
}

type eventConsumerResolverFunc func(ctx context.Context, installationID int, capabilityID string) (eventConsumerClient, error)

func (f eventConsumerResolverFunc) EventConsumerClient(
	ctx context.Context,
	installationID int,
	capabilityID string,
) (eventConsumerClient, error) {
	return f(ctx, installationID, capabilityID)
}
