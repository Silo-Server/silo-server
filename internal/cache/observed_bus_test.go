package cache

import (
	"context"
	"errors"
	"testing"
)

type failingBus struct{ *NoopEventBus }

func (failingBus) Publish(context.Context, string, Event) error { return errors.New("redis down") }

func TestObservedEventBusObservesEveryPublish(t *testing.T) {
	ctx := context.Background()
	bus := NewObservedEventBus(failingBus{&NoopEventBus{}})
	if err := bus.Publish(ctx, ChannelCatalog, Event{Type: EventScanComplete}); err == nil {
		t.Fatal("Publish hid the bus's error")
	}

	var seen []string
	bus.ObservePublishes(func(channel string, event Event) { seen = append(seen, channel+" "+event.Type) })
	if err := bus.Publish(ctx, ChannelAdmin, Event{Type: EventSettingsChanged}); err == nil {
		t.Fatal("Publish hid the bus's error")
	}
	if len(seen) != 1 || seen[0] != ChannelAdmin+" "+EventSettingsChanged {
		t.Fatalf("observed %v, want the one event published after ObservePublishes, even though the bus failed", seen)
	}
}
