package cache

import (
	"context"
	"sync"
	"sync/atomic"
)

// PublishObserver is called with every event this process publishes.
type PublishObserver func(channel string, event Event)

// ObservedEventBus is an EventBus that also hands every event published
// through it to its observers, whether or not Redis carries it to other nodes.
// Subscribers on every node receive a published event, so a subscriber cannot
// tell which node produced it; an observer runs only on the producing node.
// It runs without Redis too, where the bus itself delivers nothing.
type ObservedEventBus struct {
	EventBus
	mu        sync.Mutex
	observers atomic.Pointer[[]PublishObserver]
}

// NewObservedEventBus wraps bus. Observers are added later with
// ObservePublishes; events published before then are not observed.
func NewObservedEventBus(bus EventBus) *ObservedEventBus {
	return &ObservedEventBus{EventBus: bus}
}

// ObservePublishes adds an observer. Observers run on the publisher's
// goroutine, in the order they were added, and must not block.
func (b *ObservedEventBus) ObservePublishes(observer PublishObserver) {
	if observer == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var observers []PublishObserver
	if current := b.observers.Load(); current != nil {
		observers = append(observers, *current...)
	}
	observers = append(observers, observer)
	b.observers.Store(&observers)
}

// Publish publishes event on the wrapped bus, then hands it to the observers.
// They see the event even when the bus fails to publish it.
func (b *ObservedEventBus) Publish(ctx context.Context, channel string, event Event) error {
	err := b.EventBus.Publish(ctx, channel, event)
	if observers := b.observers.Load(); observers != nil {
		for _, observer := range *observers {
			observer(channel, event)
		}
	}
	return err
}
