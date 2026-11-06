package events

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/oklog/ulid/v2"
)

type subscriber struct {
	ch chan Envelope
}

// Hub fans out multiplexed events locally and across nodes.
type Hub struct {
	mu          sync.RWMutex
	subscribers map[*subscriber]struct{}
	sourceID    string
	eventBus    cache.EventBus
	drops       map[EventChannel]uint64
	// pluginSubscribers reports whether a plugin event has subscribers. It
	// is nil until the plugin event dispatcher installs it.
	pluginSubscribers atomic.Pointer[func(event string) bool]
}

func NewHub(sourceID string, eventBus cache.EventBus) *Hub {
	return &Hub{
		subscribers: make(map[*subscriber]struct{}),
		sourceID:    sourceID,
		eventBus:    eventBus,
		drops:       make(map[EventChannel]uint64),
	}
}

func (h *Hub) Start(ctx context.Context) error {
	if h == nil || h.eventBus == nil || ctx == nil {
		return nil
	}
	return h.eventBus.Subscribe(ctx, cache.ChannelEvents, h.handleEventBusMessage)
}

// SourceID is the ID Publish stamps on envelopes from this node. Envelopes
// relayed from other nodes keep their own.
func (h *Hub) SourceID() string {
	return h.sourceID
}

// SetPluginSubscribers installs the plugin event dispatcher's check for
// whether an event could have subscribers.
func (h *Hub) SetPluginSubscribers(check func(event string) bool) {
	if h == nil {
		return
	}
	h.pluginSubscribers.Store(&check)
}

// PluginEventWanted reports whether a plugin event could have subscribers, so
// a publisher can skip building an envelope no plugin receives. It is true
// until the dispatcher installs its check.
func (h *Hub) PluginEventWanted(event string) bool {
	if h == nil {
		return false
	}
	check := h.pluginSubscribers.Load()
	return check == nil || (*check)(event)
}

func (h *Hub) Subscribe() (<-chan Envelope, func()) {
	sub := &subscriber{ch: make(chan Envelope, 64)}
	h.mu.Lock()
	h.subscribers[sub] = struct{}{}
	h.mu.Unlock()

	return sub.ch, func() {
		h.mu.Lock()
		if _, ok := h.subscribers[sub]; ok {
			delete(h.subscribers, sub)
			close(sub.ch)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(ctx context.Context, env Envelope) error {
	if h == nil {
		return nil
	}

	if env.Timestamp.IsZero() {
		env.Timestamp = time.Now().UTC()
	}
	if env.SourceID == "" {
		env.SourceID = h.sourceID
	}
	if env.EventID == "" {
		env.EventID = ulid.Make().String()
	}

	h.publishLocal(env)

	// Plugin envelopes stay on this server: no websocket client receives the
	// plugin channel, and each server's plugin event dispatcher stores only
	// the envelopes published on it.
	if h.eventBus == nil || env.Channel == ChannelPlugins {
		return nil
	}

	payload, err := json.Marshal(env)
	if err != nil {
		return err
	}

	return h.eventBus.Publish(ctx, cache.ChannelEvents, cache.Event{
		Type:    cache.EventEventsNotification,
		Payload: string(payload),
	})
}

func (h *Hub) PublishJSON(
	ctx context.Context,
	channel EventChannel,
	event string,
	data any,
	opts PublishOptions,
) error {
	var payload json.RawMessage
	if data != nil {
		encoded, err := json.Marshal(data)
		if err != nil {
			return err
		}
		payload = encoded
	}

	return h.Publish(ctx, Envelope{
		Channel:    channel,
		Event:      event,
		EventID:    opts.EventID,
		Data:       payload,
		UserID:     opts.UserID,
		ProfileID:  opts.ProfileID,
		AdminOnly:  opts.AdminOnly,
		AllViewers: opts.AllViewers,
		V2Only:     opts.V2Only,
	})
}

func (h *Hub) Dropped(channel EventChannel) uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.drops[channel]
}

func (h *Hub) publishLocal(env Envelope) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subscribers {
		select {
		case sub.ch <- env:
		default:
			h.drops[env.Channel]++
		}
	}
}

func (h *Hub) handleEventBusMessage(event cache.Event) {
	if event.Type != cache.EventEventsNotification || event.Payload == "" {
		return
	}

	var env Envelope
	if err := json.Unmarshal([]byte(event.Payload), &env); err != nil {
		return
	}
	if env.SourceID != "" && env.SourceID == h.sourceID {
		return
	}

	h.publishLocal(env)
}
