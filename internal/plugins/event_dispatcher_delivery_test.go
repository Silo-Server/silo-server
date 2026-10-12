package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
)

// apiServer is one API server in a test cluster: its own bus connection,
// hub, dispatcher and plugin process, over the cluster's shared bus and
// outbox.
type apiServer struct {
	bus        *cache.ObservedEventBus
	hub        *events.Hub
	dispatcher *EventDispatcher
}

func startAPIServer(t *testing.T, name string, shared cache.EventBus, deliveries eventDeliveryStore, client eventConsumerClient, subscriptions ...string) apiServer {
	t.Helper()
	ctx := t.Context()
	bus := cache.NewObservedEventBus(shared)
	hub := events.NewHub(name, bus)
	if err := hub.Start(ctx); err != nil {
		t.Fatalf("start %s hub: %v", name, err)
	}
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "silo.notify", Enabled: true}},
		capabilities:  map[int][]*Capability{1: {eventConsumer(1, "events", subscriptions...)}},
	}
	d := NewEventDispatcher(bus, hub, store, staticResolver{client: client}, deliveries, 4)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("start %s dispatcher: %v", name, err)
	}
	bus.ObservePublishes(d.ObservePublish)
	t.Cleanup(d.Stop)
	return apiServer{bus: bus, hub: hub, dispatcher: d}
}

type staticResolver struct{ client eventConsumerClient }

func (r staticResolver) EventConsumerClient(context.Context, int, string) (eventConsumerClient, error) {
	return r.client, nil
}

// TestClusterDeliversEachEventOnce runs two API servers on one bus and one
// outbox. Redis hands every bus event, and every hub envelope, to both
// servers; each event must still reach the installation once.
func TestClusterDeliversEachEventOnce(t *testing.T) {
	ctx := t.Context()
	shared := newFakeBus()
	deliveries := newMemoryDeliveryStore()
	client := &recordingClient{done: make(chan struct{}), want: 3}
	subscriptions := []string{cache.EventScanComplete, "plugin.silo.other.ping", "plugin.silo.other.pong"}
	a := startAPIServer(t, "api-a", shared, deliveries, client, subscriptions...)
	b := startAPIServer(t, "api-b", shared, deliveries, client, subscriptions...)

	if err := a.bus.Publish(ctx, cache.ChannelCatalog, cache.Event{Type: cache.EventScanComplete, Payload: "7"}); err != nil {
		t.Fatalf("publish bus event: %v", err)
	}
	if err := a.hub.Publish(ctx, events.Envelope{Channel: events.ChannelPlugins, Event: "plugin.silo.other.ping", Data: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatalf("publish envelope on api-a: %v", err)
	}
	if err := b.hub.Publish(ctx, events.Envelope{Channel: events.ChannelPlugins, Event: "plugin.silo.other.pong"}); err != nil {
		t.Fatalf("publish envelope on api-b: %v", err)
	}
	waitFor(t, client)
	a.dispatcher.deliverDue(ctx)

	ids := map[string]string{}
	for _, req := range client.get() {
		if _, seen := ids[req.GetEventName()]; seen {
			t.Fatalf("%s delivered more than once", req.GetEventName())
		}
		ids[req.GetEventName()] = req.GetEventId()
		if req.GetEventId() == "" {
			t.Errorf("%s delivered without an event_id", req.GetEventName())
		}
	}
	if len(ids) != 3 {
		t.Fatalf("delivered events = %v, want the three published", ids)
	}
	deliveries.mu.Lock()
	stored := deliveries.nextID
	deliveries.mu.Unlock()
	if stored != 3 {
		t.Fatalf("outbox rows written = %d, want 3 (one per event)", stored)
	}
}

// TestServerWithoutRedisDeliversBusEvents covers a single server without a
// Redis URL, whose bus delivers nothing to subscribers.
func TestServerWithoutRedisDeliversBusEvents(t *testing.T) {
	client := &recordingClient{done: make(chan struct{}), want: 1}
	server := startAPIServer(t, "api", &cache.NoopEventBus{}, newMemoryDeliveryStore(), client, cache.EventScanComplete)

	if err := server.bus.Publish(t.Context(), cache.ChannelCatalog, cache.Event{Type: cache.EventScanComplete, Payload: `{"libraryId":"lib-1"}`}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	waitFor(t, client)
	if got := client.get()[0].GetPayload().AsMap()["libraryId"]; got != "lib-1" {
		t.Fatalf("payload libraryId = %v, want lib-1", got)
	}
}

// flakyClient fails its first failures calls.
type flakyClient struct {
	mu       sync.Mutex
	failures int
	err      error
	calls    []*pluginv1.HandleEventRequest
}

func (c *flakyClient) HandleEvent(_ context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, req)
	if len(c.calls) <= c.failures {
		return nil, c.err
	}
	return &pluginv1.HandleEventResponse{}, nil
}

func (c *flakyClient) get() []*pluginv1.HandleEventRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pluginv1.HandleEventRequest(nil), c.calls...)
}

func newDeliveryFixture(client eventConsumerClient) (*EventDispatcher, *memoryDeliveryStore) {
	deliveries := newMemoryDeliveryStore()
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "silo.notify", Enabled: true}},
		capabilities:  map[int][]*Capability{1: {eventConsumer(1, "events", cache.EventScanComplete)}},
	}
	return NewEventDispatcher(newFakeBus(), nil, store, staticResolver{client: client}, deliveries, 4), deliveries
}

func TestFailedDeliveryIsRetriedWithTheSameEventID(t *testing.T) {
	ctx := t.Context()
	client := &flakyClient{failures: 2, err: errors.New("plugin restarting")}
	d, deliveries := newDeliveryFixture(client)

	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete, Payload: "7"})
	for _, delay := range eventRetryDelays[:2] {
		pending := deliveries.pending()
		if len(pending) != 1 || pending[0].lastError != "plugin restarting" {
			t.Fatalf("after a failed attempt the outbox holds %+v, want the delivery with its error", pending)
		}
		if got := pending[0].delays[len(pending[0].delays)-1]; got != delay {
			t.Fatalf("retry delay = %v, want %v", got, delay)
		}
		// Not due before the delay has passed.
		d.deliverDue(ctx)
		if got := len(client.get()); got != len(pending[0].delays) {
			t.Fatalf("attempts before the retry is due = %d, want %d", got, len(pending[0].delays))
		}
		deliveries.advance(delay)
		d.deliverDue(ctx)
	}

	calls := client.get()
	if len(calls) != 3 {
		t.Fatalf("attempts = %d, want 3", len(calls))
	}
	for _, call := range calls[1:] {
		if call.GetEventId() != calls[0].GetEventId() {
			t.Fatalf("retry event_id = %q, want %q", call.GetEventId(), calls[0].GetEventId())
		}
	}
	if pending := deliveries.pending(); len(pending) != 0 {
		t.Fatalf("outbox after success = %+v, want empty", pending)
	}
}

func TestDeliveryGivesUpAfterTheLastRetry(t *testing.T) {
	ctx := t.Context()
	client := &flakyClient{failures: 1 << 30, err: errors.New("broken")}
	d, deliveries := newDeliveryFixture(client)

	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete})
	for _, delay := range eventRetryDelays {
		deliveries.advance(delay)
		d.deliverDue(ctx)
	}
	if got, want := len(client.get()), len(eventRetryDelays)+1; got != want {
		t.Fatalf("attempts = %d, want %d", got, want)
	}
	if pending := deliveries.pending(); len(pending) != 0 {
		t.Fatalf("outbox after the last retry = %+v, want empty", pending)
	}
}

// TestDeliveryToDisabledInstallationIsRetried: the delivering server's
// installation cache can lag an enable made on another server, so a disabled
// installation is retried like any failure rather than dropped at once.
func TestDeliveryToDisabledInstallationIsRetried(t *testing.T) {
	ctx := t.Context()
	deliveries := newMemoryDeliveryStore()
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "silo.notify", Enabled: true}},
		capabilities:  map[int][]*Capability{1: {eventConsumer(1, "events", cache.EventScanComplete)}},
	}
	client := &flakyClient{}
	disabled := true
	resolver := eventConsumerResolverFunc(func(context.Context, int, string) (eventConsumerClient, error) {
		if disabled {
			return nil, ErrInstallationDisabled
		}
		return client, nil
	})
	d := NewEventDispatcher(newFakeBus(), nil, store, resolver, deliveries, 4)

	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete})
	if pending := deliveries.pending(); len(pending) != 1 || len(pending[0].delays) != 1 {
		t.Fatalf("outbox after delivering to a disabled installation = %+v, want the delivery waiting to retry", pending)
	}
	disabled = false
	deliveries.advance(eventRetryDelays[0])
	d.deliverDue(ctx)
	if got := len(client.get()); got != 1 {
		t.Fatalf("deliveries once the installation is enabled = %d, want 1", got)
	}
	if pending := deliveries.pending(); len(pending) != 0 {
		t.Fatalf("outbox = %+v, want empty", pending)
	}
}

func TestUndeliverablePayloadIsDropped(t *testing.T) {
	ctx := t.Context()
	client := &flakyClient{}
	d, deliveries := newDeliveryFixture(client)
	if err := deliveries.Enqueue(ctx, []EventDelivery{{EventID: "e", EventName: cache.EventScanComplete, Payload: []byte(`[1]`), InstallationID: 1, CapabilityID: "events"}}); err != nil {
		t.Fatal(err)
	}
	d.deliverDue(ctx)
	if got := len(client.get()); got != 0 {
		t.Fatalf("deliveries of a payload that is not an object = %d, want 0", got)
	}
	if pending := deliveries.pending(); len(pending) != 0 {
		t.Fatalf("outbox = %+v, want empty", pending)
	}
}

// TestUnsubscribedEventsAreNotQueued: once the index is built, events no
// installation subscribes to never take a place in the intake.
func TestUnsubscribedEventsAreNotQueued(t *testing.T) {
	ctx := t.Context()
	d, _ := newDeliveryFixture(&flakyClient{})
	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete})

	d.ObservePublish(cache.ChannelPlayback, cache.Event{Type: cache.EventPlaybackSessionsChanged})
	if got := len(d.intake); got != 0 {
		t.Fatalf("intake after an unsubscribed event = %d, want 0", got)
	}
	d.ObservePublish(cache.ChannelCatalog, cache.Event{Type: cache.EventScanComplete})
	if got := len(d.intake); got != 1 {
		t.Fatalf("intake after a subscribed event = %d, want 1", got)
	}
}

// TestStopStoresEventsPublishedWhileDraining: the server's context ends
// before it drains its requests, and the bus events and hub envelopes those
// requests publish must still reach the outbox.
func TestStopStoresEventsPublishedWhileDraining(t *testing.T) {
	deliveries := newMemoryDeliveryStore()
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "silo.notify", Enabled: true}},
		capabilities:  map[int][]*Capability{1: {eventConsumer(1, "events", cache.EventScanComplete, "plugin.silo.other.ping")}},
	}
	bus := newFakeBus()
	hub := events.NewHub("api", bus)
	d := NewEventDispatcher(bus, hub, store, staticResolver{client: &flakyClient{}}, deliveries, 4)
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()

	d.ObservePublish(cache.ChannelCatalog, cache.Event{Type: cache.EventScanComplete, Payload: "7"})
	if err := hub.Publish(context.Background(), events.Envelope{Channel: events.ChannelPlugins, Event: "plugin.silo.other.ping"}); err != nil {
		t.Fatal(err)
	}
	d.Stop()
	var names []string
	for _, pending := range deliveries.pending() {
		names = append(names, pending.EventName)
	}
	slices.Sort(names)
	if want := []string{"plugin.silo.other.ping", cache.EventScanComplete}; !slices.Equal(names, want) {
		t.Fatalf("outbox after Stop = %v, want %v", names, want)
	}
}

// cancelAwareStore is a memoryDeliveryStore whose Enqueue waits for the test
// and then honors its context, as a database write would.
type cancelAwareStore struct {
	*memoryDeliveryStore
	entered chan struct{}
	release chan struct{}
}

func (s *cancelAwareStore) Enqueue(ctx context.Context, deliveries []EventDelivery) error {
	close(s.entered)
	<-s.release
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.memoryDeliveryStore.Enqueue(ctx, deliveries)
}

// TestShutdownDuringStoreKeepsTheEvent: the server's context ends while an
// event it already took from the intake is being written.
func TestShutdownDuringStoreKeepsTheEvent(t *testing.T) {
	deliveries := &cancelAwareStore{memoryDeliveryStore: newMemoryDeliveryStore(), entered: make(chan struct{}), release: make(chan struct{})}
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "silo.notify", Enabled: true}},
		capabilities:  map[int][]*Capability{1: {eventConsumer(1, "events", cache.EventScanComplete)}},
	}
	d := NewEventDispatcher(newFakeBus(), nil, store, staticResolver{client: &flakyClient{}}, deliveries, 4)
	ctx, cancel := context.WithCancel(context.Background())
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}

	d.ObservePublish(cache.ChannelCatalog, cache.Event{Type: cache.EventScanComplete})
	<-deliveries.entered
	cancel()
	close(deliveries.release)
	d.Stop()
	if pending := deliveries.pending(); len(pending) != 1 {
		t.Fatalf("outbox after shutdown during the write = %+v, want the event", pending)
	}
}

// TestDeliveryClaimedByDeadServerIsDeliveredByAnother: a server claims a
// delivery and dies before it records the outcome. Once the lease runs out
// another server delivers it, and the dead server's claim can no longer
// remove the row.
func TestDeliveryClaimedByDeadServerIsDeliveredByAnother(t *testing.T) {
	ctx := t.Context()
	client := &flakyClient{}
	d, deliveries := newDeliveryFixture(client)
	d.store(ctx, busEvent(cache.Event{Type: cache.EventScanComplete}))

	dead, err := deliveries.Claim(ctx, 10, eventDeliveryLease)
	if err != nil || len(dead) != 1 {
		t.Fatalf("dead server claim = %+v, %v; want one delivery", dead, err)
	}
	d.claimAndDeliver(ctx)
	d.inflight.Wait()
	if got := len(client.get()); got != 0 {
		t.Fatalf("deliveries while the lease holds = %d, want 0", got)
	}

	deliveries.advance(eventDeliveryLease)
	d.claimAndDeliver(ctx)
	d.inflight.Wait()
	if got := len(client.get()); got != 1 {
		t.Fatalf("deliveries after the lease ran out = %d, want 1", got)
	}
	if err := deliveries.Delete(ctx, dead[0].ID, dead[0].Attempts); err != nil {
		t.Fatal(err)
	}
	if pending := deliveries.pending(); len(pending) != 0 {
		t.Fatalf("outbox = %+v, want empty", pending)
	}
}

// TestDeliveryErrorIsSanitized: a plugin's error text can carry credentials,
// and the outbox keeps the last one.
func TestDeliveryErrorIsSanitized(t *testing.T) {
	ctx := t.Context()
	client := &flakyClient{failures: 1, err: errors.New("rpc error: desc = api_key=SECRET rejected")}
	d, deliveries := newDeliveryFixture(client)

	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete})
	pending := deliveries.pending()
	if len(pending) != 1 || strings.Contains(pending[0].lastError, "SECRET") || !strings.Contains(pending[0].lastError, "api_key=") {
		t.Fatalf("stored error = %+v, want the message with the key's value masked", pending)
	}
}
