package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
)

// memoryDeliveryStore is plugin_event_deliveries in memory, shared by every
// dispatcher in a test the way the database is shared by every API server.
// Its clock only moves when a test advances it.
type memoryDeliveryStore struct {
	mu     sync.Mutex
	now    time.Time
	nextID int64
	rows   map[int64]*memoryDelivery
}

type memoryDelivery struct {
	EventDelivery
	nextAttemptAt time.Time
	// claimed is set from Claim until Retry.
	claimed   bool
	lastError string
	// delays are the waits Retry was asked for, in order.
	delays []time.Duration
}

func newMemoryDeliveryStore() *memoryDeliveryStore {
	return &memoryDeliveryStore{now: time.Unix(1_700_000_000, 0), rows: make(map[int64]*memoryDelivery)}
}

func (s *memoryDeliveryStore) Enqueue(_ context.Context, deliveries []EventDelivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, delivery := range deliveries {
		s.nextID++
		delivery.ID = s.nextID
		delivery.Attempts = 0
		s.rows[delivery.ID] = &memoryDelivery{EventDelivery: delivery, nextAttemptAt: s.now}
	}
	return nil
}

func (s *memoryDeliveryStore) Claim(_ context.Context, limit int, lease time.Duration) ([]EventDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []*memoryDelivery
	for _, row := range s.rows {
		if !row.nextAttemptAt.After(s.now) {
			due = append(due, row)
		}
	}
	slices.SortFunc(due, func(a, b *memoryDelivery) int { return int(a.ID - b.ID) })
	if len(due) > limit {
		due = due[:limit]
	}
	claimed := make([]EventDelivery, 0, len(due))
	for _, row := range due {
		row.Attempts++
		row.claimed = true
		row.nextAttemptAt = s.now.Add(lease)
		claimed = append(claimed, row.EventDelivery)
	}
	return claimed, nil
}

func (s *memoryDeliveryStore) Delete(_ context.Context, id int64, attempts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok && row.Attempts == attempts {
		delete(s.rows, id)
	}
	return nil
}

func (s *memoryDeliveryStore) Retry(_ context.Context, id int64, attempts int, delay time.Duration, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[id]; ok && row.Attempts == attempts {
		row.claimed = false
		row.nextAttemptAt = s.now.Add(delay)
		row.lastError = lastError
		row.delays = append(row.delays, delay)
	}
	return nil
}

func (s *memoryDeliveryStore) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *memoryDeliveryStore) pending() []memoryDelivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]memoryDelivery, 0, len(s.rows))
	for _, row := range s.rows {
		out = append(out, *row)
	}
	slices.SortFunc(out, func(a, b memoryDelivery) int { return int(a.ID - b.ID) })
	return out
}

// busy reports whether a delivery is due or claimed and not yet finished.
func (s *memoryDeliveryStore) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.claimed || !row.nextAttemptAt.After(s.now) {
			return true
		}
	}
	return false
}

// dispatchBusEvent runs event through d the way the API server that
// published it does, then delivers everything due and waits for it.
func (d *EventDispatcher) dispatchBusEvent(ctx context.Context, event cache.Event) {
	d.store(ctx, busEvent(event))
	d.deliverDue(ctx)
}

// dispatchEnvelope is dispatchBusEvent for an envelope published on d's hub.
func (d *EventDispatcher) dispatchEnvelope(ctx context.Context, env events.Envelope) {
	d.store(ctx, hubEvent(env))
	d.deliverDue(ctx)
}

// deliverDue delivers every due delivery in d's memoryDeliveryStore and waits
// until none is due or claimed by a delivery still running. A started
// dispatcher's own loop does the delivering.
func (d *EventDispatcher) deliverDue(ctx context.Context) {
	store := d.deliveries.(*memoryDeliveryStore)
	deadline := time.Now().Add(10 * time.Second)
	for store.busy() {
		if time.Now().After(deadline) {
			panic(fmt.Sprintf("deliverDue: outbox still busy after 10s: %+v", store.pending()))
		}
		if d.cancel == nil {
			d.claimAndDeliver(ctx)
			d.inflight.Wait()
		}
		time.Sleep(time.Millisecond)
	}
}

// fakeBus records publishes and replays them to subscribers in-process.
type fakeBus struct {
	mu       sync.Mutex
	handlers map[string][]cache.EventHandler
}

func newFakeBus() *fakeBus { return &fakeBus{handlers: make(map[string][]cache.EventHandler)} }

func (b *fakeBus) Publish(_ context.Context, channel string, ev cache.Event) error {
	b.mu.Lock()
	hs := append([]cache.EventHandler(nil), b.handlers[channel]...)
	b.mu.Unlock()
	for _, h := range hs {
		h(ev)
	}
	return nil
}

func (b *fakeBus) Subscribe(_ context.Context, channel string, h cache.EventHandler) error {
	b.mu.Lock()
	b.handlers[channel] = append(b.handlers[channel], h)
	b.mu.Unlock()
	return nil
}

func (b *fakeBus) Close() error { return nil }

type fakeInstallationStore struct {
	installations []*Installation
	capabilities  map[int][]*Capability
}

func (s *fakeInstallationStore) ListEnabled(_ context.Context) ([]*Installation, error) {
	return s.installations, nil
}
func (s *fakeInstallationStore) ListCapabilities(_ context.Context, id int) ([]*Capability, error) {
	return s.capabilities[id], nil
}

type recordingClient struct {
	mu     sync.Mutex
	events []*pluginv1.HandleEventRequest
	done   chan struct{}
	want   int
}

func (c *recordingClient) HandleEvent(_ context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, req)
	if c.done != nil && len(c.events) == c.want {
		close(c.done)
		c.done = nil
	}
	return &pluginv1.HandleEventResponse{}, nil
}

func (c *recordingClient) get() []*pluginv1.HandleEventRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*pluginv1.HandleEventRequest, len(c.events))
	copy(out, c.events)
	return out
}

type fakeResolver struct{ client *recordingClient }

func (r *fakeResolver) EventConsumerClient(_ context.Context, _ int, _ string) (eventConsumerClient, error) {
	return r.client, nil
}

// newFixture starts a dispatcher with one installation subscribed to subs and
// returns the bus its server publishes on, its hub, and the plugin's client.
func newFixture(t *testing.T, subs []string, want int) (cache.EventBus, *events.Hub, *recordingClient) {
	t.Helper()
	bus := newFakeBus()
	hub := events.NewHub("test-node", bus)
	if err := hub.Start(context.Background()); err != nil {
		t.Fatalf("hub start: %v", err)
	}

	subsAny := make([]any, 0, len(subs))
	for _, s := range subs {
		subsAny = append(subsAny, s)
	}
	store := &fakeInstallationStore{
		installations: []*Installation{{ID: 1, PluginID: "test", Enabled: true}},
		capabilities: map[int][]*Capability{
			1: {{
				InstallationID: 1,
				Type:           "event_consumer.v1",
				ID:             "watcher",
				Metadata:       map[string]any{"subscriptions": subsAny},
			}},
		},
	}
	client := &recordingClient{done: make(chan struct{}), want: want}
	ctx, cancel := context.WithCancel(context.Background())
	d := NewEventDispatcher(bus, hub, store, &fakeResolver{client: client}, newMemoryDeliveryStore(), 4)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	observed := cache.NewObservedEventBus(bus)
	observed.ObservePublishes(d.ObservePublish)
	t.Cleanup(func() {
		cancel()
		d.Stop()
	})
	return observed, hub, client
}

func waitFor(t *testing.T, c *recordingClient) {
	t.Helper()
	c.mu.Lock()
	if len(c.events) >= c.want {
		c.mu.Unlock()
		return
	}
	done := c.done
	c.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for events; got %d, want %d", len(c.get()), c.want)
	}
}

func TestDispatcher_BusEvent_DecodesJSONPayloadIntoFields(t *testing.T) {
	bus, _, client := newFixture(t, []string{"library.media_added"}, 1)

	body, _ := json.Marshal(map[string]any{
		"libraryId": "lib-1",
		"mediaType": "movie",
		"mediaId":   "media-xyz",
		"tmdbId":    603,
	})
	_ = bus.Publish(context.Background(), cache.ChannelCatalog, cache.Event{
		Type:    "library.media_added",
		Payload: string(body),
	})

	waitFor(t, client)
	got := client.get()
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	p := got[0].GetPayload().AsMap()
	if p["libraryId"] != "lib-1" {
		t.Errorf("libraryId: got %v want lib-1; full payload=%v", p["libraryId"], p)
	}
	if _, wrapped := p["payload"]; wrapped {
		t.Error("payload should not be wrapped under \"payload\" key")
	}
}

func TestDispatcher_HubPluginEvent_ReachesConsumer(t *testing.T) {
	const eventName = "plugin.silo.arrproxy.submitted"
	_, hub, client := newFixture(t, []string{eventName}, 1)

	body, _ := json.Marshal(map[string]any{"requestId": "01J", "error": ""})
	if err := hub.Publish(context.Background(), events.Envelope{
		Channel: events.ChannelPlugins,
		Event:   eventName,
		Data:    body,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	waitFor(t, client)
	got := client.get()
	if len(got) != 1 {
		t.Fatalf("got %d events", len(got))
	}
	if got[0].GetEventName() != eventName {
		t.Errorf("event name: got %q want %q", got[0].GetEventName(), eventName)
	}
	p := got[0].GetPayload().AsMap()
	if p["requestId"] != "01J" {
		t.Errorf("requestId: got %v want 01J; full payload=%v", p["requestId"], p)
	}
}
