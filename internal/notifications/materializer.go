package notifications

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

	evt "github.com/Silo-Server/silo-server/internal/events"
)

type matcherFunc func(ctx context.Context, env evt.Envelope) error

type namedMatcher struct {
	name string
	fn   matcherFunc
}

// Materializer subscribes to the events hub and turns events into
// notifications rows via registered matchers. Matcher failures are isolated:
// one matcher erroring or panicking never blocks the hub or other matchers.
type Materializer struct {
	hub       *evt.Hub
	svc       *Service
	content   ContentResolver // nil disables the content matcher
	matchers  []namedMatcher
	processed atomic.Int64
	unsub     func()
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewMaterializer(hub *evt.Hub, svc *Service, content ContentResolver) *Materializer {
	m := &Materializer{hub: hub, svc: svc, content: content}
	m.register("request", m.matchRequest)
	m.register("send", m.matchSend)
	if content != nil {
		m.register("content", m.matchContent)
	}
	m.register("admin", m.matchAdmin)
	m.register("system", m.matchSystem)
	return m
}

func (m *Materializer) register(name string, fn matcherFunc) {
	m.matchers = append(m.matchers, namedMatcher{name: name, fn: fn})
}

// Processed reports how many envelopes have been fully processed (test hook).
func (m *Materializer) Processed() int64 { return m.processed.Load() }

// Start subscribes to the hub and begins materializing events. It is
// single-shot and safe to call concurrently: only the first call subscribes;
// later calls are no-ops.
func (m *Materializer) Start(ctx context.Context) error {
	m.startOnce.Do(func() {
		ch, unsub := m.hub.Subscribe()
		m.unsub = unsub
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case env, ok := <-ch:
					if !ok {
						return
					}
					m.handle(ctx, env)
					m.processed.Add(1)
				}
			}
		}()
	})
	return nil
}

func (m *Materializer) Stop() {
	m.stopOnce.Do(func() {
		if m.unsub != nil {
			m.unsub()
		}
	})
}

func (m *Materializer) handle(ctx context.Context, env evt.Envelope) {
	if env.Channel == evt.ChannelNotifications {
		return // never react to our own output — the materializer consumes, it must not feed itself
	}
	for _, matcher := range m.matchers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.ErrorContext(ctx, "notifications: matcher panicked", "matcher", matcher.name, "event", env.Event, "panic", r)
				}
			}()
			if err := matcher.fn(ctx, env); err != nil {
				slog.WarnContext(ctx, "notifications: matcher failed", "matcher", matcher.name, "event", env.Event, "error", err)
			}
		}()
	}
}
