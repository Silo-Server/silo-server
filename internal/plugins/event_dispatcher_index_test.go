package plugins

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sync"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/cache"
)

// countingDispatchStore is a taskInstallationStore that counts every read the
// dispatcher makes, so tests can assert store reads per event.
type countingDispatchStore struct {
	mu               sync.Mutex
	installations    []*Installation
	capabilities     map[int][]*Capability
	listEnabled      int
	listCapabilities int
	// onListEnabled, when set, runs inside ListEnabled so a test can land an
	// invalidation while a rebuild is in flight.
	onListEnabled func()
	// capabilitiesErr fails ListCapabilities for the listed installations.
	capabilitiesErr map[int]error
}

func (s *countingDispatchStore) ListEnabled(context.Context) ([]*Installation, error) {
	s.mu.Lock()
	s.listEnabled++
	hook := s.onListEnabled
	out := append([]*Installation(nil), s.installations...)
	s.mu.Unlock()
	if hook != nil {
		hook()
	}
	return out, nil
}

func (s *countingDispatchStore) ListCapabilities(_ context.Context, id int) ([]*Capability, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listCapabilities++
	if err := s.capabilitiesErr[id]; err != nil {
		return nil, err
	}
	return s.capabilities[id], nil
}

func (s *countingDispatchStore) reads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listEnabled + s.listCapabilities
}

func (s *countingDispatchStore) resetReads() {
	s.mu.Lock()
	s.listEnabled, s.listCapabilities = 0, 0
	s.mu.Unlock()
}

// install adds an enabled installation, as another replica's admin action
// would by writing plugin_installations and plugin_capabilities.
func (s *countingDispatchStore) install(installation *Installation, capabilities ...*Capability) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installations = append(s.installations, installation)
	if s.capabilities == nil {
		s.capabilities = make(map[int][]*Capability)
	}
	s.capabilities[installation.ID] = capabilities
}

// disable drops an installation from the enabled set.
func (s *countingDispatchStore) disable(installationID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.installations[:0:0]
	for _, installation := range s.installations {
		if installation.ID != installationID {
			kept = append(kept, installation)
		}
	}
	s.installations = kept
}

// delivery is one HandleEvent call the dispatcher made.
type delivery struct {
	installationID int
	capabilityID   string
	eventName      string
}

// deliveryRecorder resolves every installation to a client that records the
// call. dispatchBusEvent and dispatchEnvelope wait for their deliveries, so
// they have finished recording by the time they return.
type deliveryRecorder struct {
	mu   sync.Mutex
	seen []delivery
}

func (r *deliveryRecorder) EventConsumerClient(_ context.Context, installationID int, capabilityID string) (eventConsumerClient, error) {
	return recordingConsumer{recorder: r, installationID: installationID, capabilityID: capabilityID}, nil
}

func (r *deliveryRecorder) take() []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

type recordingConsumer struct {
	recorder       *deliveryRecorder
	installationID int
	capabilityID   string
}

func (c recordingConsumer) HandleEvent(_ context.Context, req *pluginv1.HandleEventRequest) (*pluginv1.HandleEventResponse, error) {
	c.recorder.mu.Lock()
	c.recorder.seen = append(c.recorder.seen, delivery{
		installationID: c.installationID,
		capabilityID:   c.capabilityID,
		eventName:      req.GetEventName(),
	})
	c.recorder.mu.Unlock()
	return &pluginv1.HandleEventResponse{}, nil
}

func eventConsumer(installationID int, id string, subscriptions ...string) *Capability {
	values := make([]any, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		values = append(values, subscription)
	}
	return &Capability{
		InstallationID: installationID,
		Type:           "event_consumer.v1",
		ID:             id,
		Metadata:       map[string]any{"subscriptions": values},
	}
}

// newIndexFixture returns a store with one builtin and three plugin
// installations, of which only installation 1 subscribes to
// library.media_added and installation 3 subscribes to a plugin event.
func newIndexFixture() *countingDispatchStore {
	store := &countingDispatchStore{}
	store.install(&Installation{ID: 1, PluginID: "silo.alpha", Enabled: true},
		eventConsumer(1, "first", "library.media_added"),
		eventConsumer(1, "second", "library.media_added", cache.EventScanComplete),
	)
	store.install(&Installation{ID: 2, PluginID: "silo.metadata", Enabled: true},
		&Capability{InstallationID: 2, Type: "metadata_provider.v1", ID: "tmdb"},
	)
	store.install(&Installation{ID: 3, PluginID: "silo.gamma", Enabled: true},
		eventConsumer(3, "watcher", "plugin.silo.alpha.submitted"),
	)
	store.install(&Installation{ID: 4, PluginID: "silo.builtin", Kind: KindBuiltin, Enabled: true})
	return store
}

func mediaAdded() cache.Event {
	body, _ := json.Marshal(map[string]any{"libraryId": "lib-1"})
	return cache.Event{Type: "library.media_added", Payload: string(body)}
}

// TestDispatcherFollowsPluginsChangedFromAnotherReplica simulates an admin
// enabling, then disabling, a plugin on another API replica: that replica
// writes the database and publishes cache.EventPluginsChanged, which reaches
// this replica's dispatcher over the event bus. Each notification drops the
// index, the next event rebuilds it (1+N store reads), and the events after
// it cost none.
func TestDispatcherFollowsPluginsChangedFromAnotherReplica(t *testing.T) {
	ctx := t.Context()
	bus := newFakeBus()
	store := newIndexFixture()
	recorder := &deliveryRecorder{}
	d := NewEventDispatcher(bus, nil, store, recorder, newMemoryDeliveryStore(), 4)
	if err := d.Start(ctx); err != nil {
		t.Fatalf("start dispatcher: %v", err)
	}
	t.Cleanup(d.Stop)

	publish := func() []int {
		t.Helper()
		d.dispatchBusEvent(ctx, mediaAdded())
		var ids []int
		for _, delivered := range recorder.take() {
			ids = append(ids, delivered.installationID)
		}
		return ids
	}
	pluginsChanged := func(payload string) {
		t.Helper()
		if err := bus.Publish(ctx, cache.ChannelAdmin, cache.Event{Type: cache.EventPluginsChanged, Payload: payload}); err != nil {
			t.Fatalf("publish plugins_changed: %v", err)
		}
	}
	wantReads := func(step string, want int) {
		t.Helper()
		got := store.reads()
		t.Logf("%s: %d store reads", step, got)
		if got != want {
			t.Fatalf("%s: store reads = %d, want %d", step, got, want)
		}
		store.resetReads()
	}

	if got := publish(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("initial deliveries = %v, want [1]", got)
	}
	store.resetReads()

	// The other replica commits the new installation. Until its notification
	// arrives this replica keeps dispatching from its index.
	store.install(&Installation{ID: 5, PluginID: "silo.delta", Enabled: true},
		eventConsumer(5, "watcher", "library.media_added"),
	)
	if got := publish(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("deliveries before plugins_changed = %v, want [1]", got)
	}
	wantReads("event before plugins_changed", 0)

	pluginsChanged("")
	wantReads("plugins_changed after enable elsewhere", 0)
	if got := sortedInts(publish()); !reflect.DeepEqual(got, []int{1, 5}) {
		t.Fatalf("deliveries after enable elsewhere = %v, want [1 5]", got)
	}
	// ListEnabled plus ListCapabilities for installations 1, 2, 3, and 5.
	wantReads("first event after enable elsewhere", 5)
	publish()
	wantReads("next event after enable elsewhere", 0)

	store.disable(1)
	pluginsChanged(`{"installation_id":1}`)
	wantReads("plugins_changed after disable elsewhere", 0)
	if got := publish(); !reflect.DeepEqual(got, []int{5}) {
		t.Fatalf("deliveries after disable elsewhere = %v, want [5]", got)
	}
	// ListEnabled plus ListCapabilities for installations 2, 3, and 5.
	wantReads("first event after disable elsewhere", 4)
	publish()
	wantReads("next event after disable elsewhere", 0)
}

// TestDispatcherDeliversOncePerInstallation keeps one delivery per
// installation: an event reaches the first event_consumer.v1 capability that
// lists it, even when a later capability lists it too.
func TestDispatcherDeliversOncePerInstallation(t *testing.T) {
	ctx := context.Background()
	recorder := &deliveryRecorder{}
	d := NewEventDispatcher(newFakeBus(), nil, newIndexFixture(), recorder, newMemoryDeliveryStore(), 4)

	d.dispatchBusEvent(ctx, mediaAdded())
	d.dispatchBusEvent(ctx, cache.Event{Type: cache.EventScanComplete, Payload: "7"})
	want := []delivery{
		{installationID: 1, capabilityID: "first", eventName: "library.media_added"},
		{installationID: 1, capabilityID: "second", eventName: cache.EventScanComplete},
	}
	if got := recorder.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deliveries = %+v, want %+v", got, want)
	}
}

// TestDispatcherIndexNotKeptAfterRacingInvalidation proves the generation
// guard: an index whose store reads straddle an invalidation serves the event
// that built it but is not kept, so the next event reads the store again.
func TestDispatcherIndexNotKeptAfterRacingInvalidation(t *testing.T) {
	ctx := context.Background()
	store := newIndexFixture()
	recorder := &deliveryRecorder{}
	d := NewEventDispatcher(newFakeBus(), nil, store, recorder, newMemoryDeliveryStore(), 4)

	store.onListEnabled = d.invalidateIndex
	d.dispatchBusEvent(ctx, mediaAdded())
	if got := recorder.take(); len(got) != 1 {
		t.Fatalf("racing rebuild deliveries = %+v, want one", got)
	}
	if got := store.reads(); got != 4 {
		t.Fatalf("racing rebuild store reads = %d, want 4", got)
	}

	store.onListEnabled = nil
	store.resetReads()
	d.dispatchBusEvent(ctx, mediaAdded())
	if got := store.reads(); got != 4 {
		t.Fatalf("store reads after racing rebuild = %d, want 4 (index was not kept)", got)
	}
	store.resetReads()
	d.dispatchBusEvent(ctx, mediaAdded())
	if got := store.reads(); got != 0 {
		t.Fatalf("store reads once rebuilt = %d, want 0", got)
	}
}

func sortedInts(values []int) []int {
	out := slices.Clone(values)
	slices.Sort(out)
	return out
}
