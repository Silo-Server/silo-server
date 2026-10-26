package cache

import (
	"context"
	"sync/atomic"
)

// PublishObserver is called with every event this process publishes.
type PublishObserver func(channel string, event Event)

// ObservedEventBus is an EventBus that also hands every event published
// through it to an observer, whether or not Redis carries it to other nodes.
// Subscribers on every node receive a published event, so a subscriber cannot
// tell which node produced it; the observer runs only on the producing node.
// It runs without Redis too, where the bus itself delivers nothing.
type ObservedEventBus struct {
	EventBus
	observer atomic.Pointer[PublishObserver]
}

// NewObservedEventBus wraps bus. The observer is set later with
// ObservePublishes; events published before then are not observed.
func NewObservedEventBus(bus EventBus) *ObservedEventBus {
	return &ObservedEventBus{EventBus: bus}
}

// ObservePublishes sets the observer, replacing any earlier one. The observer
// runs on the publisher's goroutine and must not block.
func (b *ObservedEventBus) ObservePublishes(observer PublishObserver) {
	if observer == nil {
		b.observer.Store(nil)
		return
	}
	b.observer.Store(&observer)
}

// Publish publishes event on the wrapped bus, then hands it to the observer.
// The observer sees the event even when the bus fails to publish it.
func (b *ObservedEventBus) Publish(ctx context.Context, channel string, event Event) error {
	err := b.EventBus.Publish(ctx, channel, event)
	if observer := b.observer.Load(); observer != nil {
		(*observer)(channel, event)
	}
	return err
}
