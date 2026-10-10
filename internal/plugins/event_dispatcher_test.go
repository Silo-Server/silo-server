package plugins

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/events"
)

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

func newFixture(t *testing.T, subs []string, want int) (*fakeBus, *events.Hub, *recordingClient) {
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
	d := NewEventDispatcher(bus, hub, store, &fakeResolver{client: client}, 4)
	if err := d.Start(context.Background()); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(d.Stop)
	return bus, hub, client
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
