package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

type eventConsumerClient interface {
	HandleEvent(ctx context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error)
}

type eventConsumerResolver interface {
	EventConsumerClient(ctx context.Context, installationID int, capabilityID string) (eventConsumerClient, error)
}

// eventDeliveryStore is the outbox the dispatcher delivers from;
// EventDeliveryStore implements it on plugin_event_deliveries.
type eventDeliveryStore interface {
	Enqueue(ctx context.Context, deliveries []EventDelivery) error
	Claim(ctx context.Context, limit int, lease time.Duration) ([]EventDelivery, error)
	Delete(ctx context.Context, id int64, attempts int) error
	Retry(ctx context.Context, id int64, attempts int, delay time.Duration, lastError string) error
}

const (
	// eventIntakeSize bounds the events waiting to be written to the
	// outbox. A bus publisher never waits for room: when the queue is full,
	// its event is dropped and logged.
	eventIntakeSize = 1024
	// eventDeliveryPoll is how often every server looks for due deliveries
	// it was not woken for: retries, rows another server produced but could
	// not deliver yet, and rows whose claimer died.
	eventDeliveryPoll = 10 * time.Second
	// eventResolveTimeout bounds resolving the plugin client, which may
	// start the plugin. The HandleEvent call has the host's
	// pluginhost.DefaultEventTimeout.
	eventResolveTimeout = time.Minute
	// eventDeliveryLease is how long a claimed delivery stays hidden from
	// other servers. It outlasts eventResolveTimeout plus the call, so a slow
	// delivery is not claimed twice.
	eventDeliveryLease = 2 * time.Minute
	// eventOutcomeTimeout bounds recording a delivery's outcome, which
	// outlives a server that starts shutting down after the plugin answered.
	eventOutcomeTimeout = 10 * time.Second
	// eventDrainTimeout bounds writing one event, or all the events Stop
	// finds still queued, once shutdown may have begun.
	eventDrainTimeout = 5 * time.Second
)

// eventRetryDelays is the wait after each failed attempt; a delivery that
// fails once more after the last one is dropped. Together they cover about
// 20 minutes, longer than a crashing resident plugin's restart backoff.
var eventRetryDelays = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	3 * time.Minute,
	15 * time.Minute,
}

// EventDispatcher delivers host events to plugin event_consumer.v1
// capabilities whose manifest "subscriptions" list matches the event name.
//
// Only the server that produced an event stores it: one row per subscribing
// installation in the plugin_event_deliveries outbox. Any server then claims
// due rows, calls HandleEvent on its own process for the installation, and
// deletes the row on success. A failed attempt is retried after
// eventRetryDelays with the same event_id, and a row whose claimer died is
// claimed again once its lease runs out, so a stored event reaches each
// subscribing installation at least once unless every attempt fails.
// Deliveries are not ordered. An event is lost before it is stored when its
// server dies first, when the intake is full, or when the write fails.
//
// Events come from two places on the producing server:
//
//   - ObservePublish, registered on the cache.ObservedEventBus, receives the
//     catalog, admin and playback events host code publishes on the bus.
//     Redis hands those to every server, so subscribing to them would store
//     each one once per server.
//   - The events.Hub, for envelopes published on this server. This is the
//     path plugin-published events take (RuntimeHostServer.PublishEvent
//     stamps `plugin.<id>.` and writes an envelope on ChannelPlugins).
//     Envelopes the hub relays from other servers are theirs to store.
//
// Dispatch is fan-out only: every subscriber to event_name receives the event.
// Plugins wanting to address a single peer should embed a target identifier
// in the payload and have subscribers filter on it.
type EventDispatcher struct {
	bus           cache.EventBus
	hub           *events.Hub
	installations taskInstallationStore
	resolver      eventConsumerResolver
	deliveries    eventDeliveryStore
	// sem holds one token per delivery in flight on this server.
	sem chan struct{}
	// inflight waits for those deliveries.
	inflight sync.WaitGroup
	// intake carries produced events to the goroutine that stores them.
	intake chan producedEvent
	// wake asks the delivery loop to claim now rather than at its next poll.
	wake chan struct{}

	hubUnsubscribe func()
	// cancel ends the goroutines Start runs, and loops waits for them.
	cancel context.CancelFunc
	loops  sync.WaitGroup

	// index maps an event name to its subscribers so an event costs a map
	// lookup instead of 1+N store reads. It is
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

// producedEvent is an event this server produced, on its way to the outbox.
type producedEvent struct {
	id   string
	name string
	// targetPluginID, when set, limits the event to that plugin's
	// installations.
	targetPluginID string
	// payload decodes the event's payload. It runs on the intake goroutine,
	// off the publisher's.
	payload func() *structpb.Struct
}

func NewEventDispatcher(
	bus cache.EventBus,
	hub *events.Hub,
	installations taskInstallationStore,
	resolver eventConsumerResolver,
	deliveries eventDeliveryStore,
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
		deliveries:    deliveries,
		sem:           make(chan struct{}, concurrency),
		intake:        make(chan producedEvent, eventIntakeSize),
		wake:          make(chan struct{}, 1),
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
	deliveries eventDeliveryStore,
	concurrency int,
) *EventDispatcher {
	return NewEventDispatcher(bus, hub, installations, eventConsumerResolverFunc(func(
		ctx context.Context,
		installationID int,
		capabilityID string,
	) (eventConsumerClient, error) {
		return resolver.EventConsumerClient(ctx, installationID, capabilityID)
	}), deliveries, concurrency)
}

// Start follows lifecycle changes from other servers, takes envelopes from
// the hub, and starts storing and delivering events until ctx ends or Stop
// is called. Bus events arrive through ObservePublish, which the caller
// registers.
func (d *EventDispatcher) Start(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)
	if err := d.bus.Subscribe(ctx, cache.ChannelAdmin, func(event cache.Event) {
		if event.Type == cache.EventPluginsChanged {
			d.invalidateIndex()
		}
	}); err != nil {
		return fmt.Errorf("subscribe plugin event dispatcher to %s: %w", cache.ChannelAdmin, err)
	}

	if d.hub != nil {
		envCh, unsub := d.hub.Subscribe()
		d.hubUnsubscribe = unsub
		d.loops.Go(func() { d.consumeHub(ctx, envCh) })
	}
	d.loops.Go(func() { d.storeEvents(ctx) })
	d.loops.Go(func() { d.deliverEvents(ctx) })
	return nil
}

// Stop ends what Start began and waits for the deliveries in flight, then
// stores the events still queued. Those include events published while the
// server drains after its context ended.
func (d *EventDispatcher) Stop() {
	if d.hubUnsubscribe != nil {
		d.hubUnsubscribe()
		d.hubUnsubscribe = nil
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.loops.Wait()
	d.inflight.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), eventDrainTimeout)
	defer cancel()
	for {
		select {
		case produced := <-d.intake:
			d.store(ctx, produced)
		default:
			return
		}
	}
}

// mayHaveSubscribers reports whether eventName could have subscribers,
// reading only the index: an event goes on to the intake when the index has
// subscribers for it, or is missing or stale.
func (d *EventDispatcher) mayHaveSubscribers(eventName string) bool {
	index, ok := d.currentIndex()
	return !ok || len(index[eventName]) > 0
}

// ObservePublish takes an event this server published on the bus. It is a
// cache.PublishObserver and never blocks the publisher.
func (d *EventDispatcher) ObservePublish(channel string, event cache.Event) {
	switch channel {
	case cache.ChannelCatalog, cache.ChannelAdmin, cache.ChannelPlayback:
	default:
		return
	}
	if !d.mayHaveSubscribers(event.Type) {
		return
	}
	d.offer(busEvent(event))
}

// offer queues an event without waiting, and logs it when the intake is full.
func (d *EventDispatcher) offer(produced producedEvent) {
	select {
	case d.intake <- produced:
	default:
		slog.Warn("plugin event dispatcher: intake full, event not delivered to plugins", "component", "plugins", "event_name", produced.name, "event_id", produced.id)
	}
}

func busEvent(event cache.Event) producedEvent {
	return producedEvent{
		id:      ulid.Make().String(),
		name:    event.Type,
		payload: func() *structpb.Struct { return decodeStringPayload(event.Payload) },
	}
}

func hubEvent(env events.Envelope) producedEvent {
	return producedEvent{
		id:             env.EventID,
		name:           env.Event,
		targetPluginID: env.TargetPluginID,
		payload:        func() *structpb.Struct { return decodeRawJSON(env.Data) },
	}
}

// consumeHub reads until Stop unsubscribes, so envelopes published while the
// server drains after its context ended still reach the intake for Stop to
// store. Until then it waits for room in the intake, leaving envelopes in
// the hub's buffer.
func (d *EventDispatcher) consumeHub(ctx context.Context, ch <-chan events.Envelope) {
	for env := range ch {
		if env.SourceID != d.hub.SourceID() || !d.mayHaveSubscribers(env.Event) {
			continue
		}
		produced := hubEvent(env)
		select {
		case d.intake <- produced:
		case <-ctx.Done():
			d.offer(produced)
		}
	}
}

// storeEvents stores queued events until ctx ends. An event it has taken
// from the intake is stored with a context of its own, so a shutdown that
// begins meanwhile does not lose it; Stop waits for it before draining.
func (d *EventDispatcher) storeEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case produced := <-d.intake:
			storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), eventDrainTimeout)
			d.store(storeCtx, produced)
			cancel()
		}
	}
}

// store writes one outbox row per installation subscribed to the event and
// wakes the delivery loop.
func (d *EventDispatcher) store(ctx context.Context, produced producedEvent) {
	subscribers := d.subscribersFor(ctx, produced.name, produced.targetPluginID)
	if len(subscribers) == 0 {
		return
	}
	var payload json.RawMessage
	if decoded := produced.payload(); decoded != nil {
		encoded, err := decoded.MarshalJSON()
		if err != nil {
			slog.WarnContext(ctx, "plugin event dispatcher: encode payload failed, event not delivered to plugins", "component", "plugins", "event_name", produced.name, "event_id", produced.id, "error", err)
			return
		}
		payload = encoded
	}
	deliveries := make([]EventDelivery, 0, len(subscribers))
	for _, subscriber := range subscribers {
		deliveries = append(deliveries, EventDelivery{
			EventID:        produced.id,
			EventName:      produced.name,
			Payload:        payload,
			InstallationID: subscriber.installationID,
			CapabilityID:   subscriber.capabilityID,
		})
	}
	if err := d.deliveries.Enqueue(ctx, deliveries); err != nil {
		slog.WarnContext(ctx, "plugin event dispatcher: store event failed, event not delivered to plugins", "component", "plugins", "event_name", produced.name, "event_id", produced.id, "error", err)
		return
	}
	d.wakeDeliveries()
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

// subscribersFor returns the subscribers an event is delivered to: the
// indexed subscribers of eventName, narrowed to targetPluginID when it is set.
func (d *EventDispatcher) subscribersFor(ctx context.Context, eventName, targetPluginID string) []eventSubscriber {
	subscribers, err := d.subscribers(ctx, eventName)
	if err != nil {
		slog.WarnContext(ctx, "plugin event dispatcher: list installations failed", "component", "plugins", "error", err)
		return nil
	}
	if targetPluginID == "" {
		return subscribers
	}
	var targeted []eventSubscriber
	for _, subscriber := range subscribers {
		if subscriber.pluginID == targetPluginID {
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

func (d *EventDispatcher) wakeDeliveries() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// deliverEvents claims and delivers due events until ctx ends: when woken by
// a stored event or a finished delivery, and every eventDeliveryPoll.
func (d *EventDispatcher) deliverEvents(ctx context.Context) {
	ticker := time.NewTicker(eventDeliveryPoll)
	defer ticker.Stop()
	for {
		d.claimAndDeliver(ctx)
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
	}
}

// claimAndDeliver claims as many due deliveries as this server has free
// slots and starts each one. A finished delivery frees its slot and wakes
// the loop to claim again.
func (d *EventDispatcher) claimAndDeliver(ctx context.Context) {
	for ctx.Err() == nil {
		// Only this loop takes slots, so free can only grow before it does.
		free := cap(d.sem) - len(d.sem)
		if free == 0 {
			return
		}
		claimed, err := d.deliveries.Claim(ctx, free, eventDeliveryLease)
		if err != nil {
			if ctx.Err() == nil {
				slog.WarnContext(ctx, "plugin event dispatcher: claim deliveries failed", "component", "plugins", "error", err)
			}
			return
		}
		for _, delivery := range claimed {
			d.sem <- struct{}{}
			d.inflight.Add(1)
			go func() {
				defer d.inflight.Done()
				defer func() {
					<-d.sem
					d.wakeDeliveries()
				}()
				d.deliver(ctx, delivery)
			}()
		}
		if len(claimed) < free {
			return
		}
	}
}

// deliver makes one attempt at a claimed delivery and records the outcome.
func (d *EventDispatcher) deliver(ctx context.Context, delivery EventDelivery) {
	err := d.handleEvent(ctx, delivery)
	if err != nil && ctx.Err() != nil {
		// Shutting down. The lease runs out and a server claims it again.
		return
	}
	// A plugin that answered just before shutdown has handled the event, so
	// its outcome is recorded with a context of its own.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), eventOutcomeTimeout)
	defer cancel()
	logArgs := []any{
		"component", "plugins",
		"installation_id", delivery.InstallationID,
		"capability_id", delivery.CapabilityID,
		"event_name", delivery.EventName,
		"event_id", delivery.EventID,
		"attempt", delivery.Attempts,
	}
	// A plugin's error text can carry credentials it was given.
	var message string
	if err != nil {
		message = logredact.SanitizeText(err.Error())
	}
	var recordErr error
	switch {
	case err == nil:
		recordErr = d.deliveries.Delete(ctx, delivery.ID, delivery.Attempts)
	case errors.Is(err, errUndeliverable):
		slog.WarnContext(ctx, "plugin event dispatcher: delivery dropped", append(logArgs, "error", message)...)
		recordErr = d.deliveries.Delete(ctx, delivery.ID, delivery.Attempts)
	case delivery.Attempts > len(eventRetryDelays):
		slog.WarnContext(ctx, "plugin event dispatcher: delivery failed, giving up", append(logArgs, "error", message)...)
		recordErr = d.deliveries.Delete(ctx, delivery.ID, delivery.Attempts)
	default:
		delay := eventRetryDelays[delivery.Attempts-1]
		level := slog.LevelWarn
		if errors.Is(err, ErrInstallationDisabled) || errors.Is(err, ErrInstallationNotFound) {
			// Usually a plugin disabled or removed after the event was
			// stored, but this server's installation cache can lag a
			// change made on another server, so it is retried too.
			level = slog.LevelDebug
		}
		slog.Log(ctx, level, "plugin event dispatcher: delivery failed, will retry", append(logArgs, "retry_in", delay, "error", message)...)
		recordErr = d.deliveries.Retry(ctx, delivery.ID, delivery.Attempts, delay, message)
	}
	if recordErr != nil {
		// The lease runs out and the delivery is attempted again.
		slog.WarnContext(ctx, "plugin event dispatcher: record delivery outcome failed", append(logArgs, "error", recordErr)...)
	}
}

// errUndeliverable marks a delivery no retry can make succeed.
var errUndeliverable = errors.New("undeliverable")

func (d *EventDispatcher) handleEvent(ctx context.Context, delivery EventDelivery) error {
	var payload *structpb.Struct
	if len(delivery.Payload) > 0 {
		payload = &structpb.Struct{}
		if err := payload.UnmarshalJSON(delivery.Payload); err != nil {
			return fmt.Errorf("%w: decode payload: %w", errUndeliverable, err)
		}
	}
	resolveCtx, cancel := context.WithTimeout(ctx, eventResolveTimeout)
	client, err := d.resolveClient(resolveCtx, delivery)
	cancel()
	if err != nil {
		return fmt.Errorf("resolve client: %w", err)
	}
	// The client gives the call the host's event timeout.
	_, err = client.HandleEvent(ctx, &pluginv1.HandleEventRequest{
		EventId:   delivery.EventID,
		EventName: delivery.EventName,
		Payload:   payload,
	})
	return err
}

// resolveClient resolves the installation's client within ctx. Starting a
// plugin is shared by its callers and ignores their deadlines, and a
// delivery still waiting once its lease ran out could be made by a second
// server meanwhile.
func (d *EventDispatcher) resolveClient(ctx context.Context, delivery EventDelivery) (eventConsumerClient, error) {
	type resolved struct {
		client eventConsumerClient
		err    error
	}
	done := make(chan resolved, 1)
	go func() {
		client, err := d.resolver.EventConsumerClient(ctx, delivery.InstallationID, delivery.CapabilityID)
		done <- resolved{client, err}
	}()
	select {
	case r := <-done:
		return r.client, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
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
