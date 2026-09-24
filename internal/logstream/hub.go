package logstream

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
)

type Stream string

const (
	StreamApp   Stream = "app"
	StreamAudit Stream = "audit"
)

const (
	MessageTypeSnapshot = "snapshot"
	MessageTypeAppend   = "append"
	MessageTypeError    = "error"
)

type Message struct {
	Type       string          `json:"type"`
	Stream     Stream          `json:"stream"`
	Entry      json.RawMessage `json:"entry,omitempty"`
	Entries    json.RawMessage `json:"entries,omitempty"`
	NextCursor string          `json:"next_cursor,omitempty"`
	Code       string          `json:"code,omitempty"`
	Message    string          `json:"message,omitempty"`
}

type appendEnvelope struct {
	Source string          `json:"source"`
	Stream Stream          `json:"stream"`
	Entry  json.RawMessage `json:"entry"`
}

type Filter func(Message) bool

type subscriber struct {
	ch     chan Message
	filter Filter
}

type Hub struct {
	mu          sync.RWMutex
	subscribers map[*subscriber]struct{}
	sourceID    string
	eventBus    cache.EventBus
}

func NewHub(sourceID string, eventBus cache.EventBus) *Hub {
	return &Hub{
		subscribers: make(map[*subscriber]struct{}),
		sourceID:    sourceID,
		eventBus:    eventBus,
	}
}

func (h *Hub) Start(ctx context.Context) error {
	if h == nil || h.eventBus == nil || ctx == nil {
		return nil
	}
	return h.eventBus.Subscribe(ctx, cache.ChannelLogs, h.handleEventBusMessage)
}

func (h *Hub) Subscribe(filter Filter) (<-chan Message, func()) {
	sub := &subscriber{
		ch:     make(chan Message, 64),
		filter: filter,
	}
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

// publishBudget bounds the cross-node fan-out of one persisted batch.
var publishBudget = 2 * time.Second

// PublishAppends fans out a batch of freshly persisted entries. Local
// subscribers receive every entry. The cross-node event bus shares one
// deadline for the batch, so an unreachable Redis costs the consumer at most
// publishBudget per batch instead of stalling the entries queued behind it.
// It returns how many entries failed to publish and the last error.
func PublishAppends[T any](ctx context.Context, h *Hub, stream Stream, entries []T) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, publishBudget)
	defer cancel()
	failed := 0
	var lastErr error
	for _, entry := range entries {
		if err := h.PublishAppend(ctx, stream, entry); err != nil {
			failed++
			lastErr = err
		}
	}
	return failed, lastErr
}

func (h *Hub) PublishAppend(ctx context.Context, stream Stream, entry any) error {
	if h == nil {
		return nil
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log stream entry: %w", err)
	}

	msg := Message{
		Type:   MessageTypeAppend,
		Stream: stream,
		Entry:  raw,
	}
	h.publishLocal(msg)

	if h.eventBus == nil {
		return nil
	}

	eventType := cache.EventOperationalLogAppended
	if stream == StreamAudit {
		eventType = cache.EventAuditLogAppended
	}

	payload, err := json.Marshal(appendEnvelope{
		Source: h.sourceID,
		Stream: stream,
		Entry:  raw,
	})
	if err != nil {
		return fmt.Errorf("marshal log stream payload: %w", err)
	}

	if err := h.eventBus.Publish(ctx, cache.ChannelLogs, cache.Event{
		Type:    eventType,
		Payload: string(payload),
	}); err != nil {
		return fmt.Errorf("publish log stream payload: %w", err)
	}
	return nil
}

func (h *Hub) publishLocal(msg Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subscribers {
		if sub.filter != nil && !sub.filter(msg) {
			continue
		}
		select {
		case sub.ch <- msg:
		default:
		}
	}
}

func (h *Hub) handleEventBusMessage(event cache.Event) {
	if event.Type != cache.EventOperationalLogAppended && event.Type != cache.EventAuditLogAppended {
		return
	}

	var envelope appendEnvelope
	if err := json.Unmarshal([]byte(event.Payload), &envelope); err != nil {
		return
	}
	if envelope.Source != "" && envelope.Source == h.sourceID {
		return
	}
	if len(envelope.Entry) == 0 {
		return
	}

	h.publishLocal(Message{
		Type:   MessageTypeAppend,
		Stream: envelope.Stream,
		Entry:  envelope.Entry,
	})
}
