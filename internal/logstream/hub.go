package logstream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

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

// Cross-node fan-out runs on the hub's own goroutine, so a slow or unreachable
// Redis never holds up the consumers that persist log entries.
const (
	// remoteQueueSize bounds the persisted rows waiting for the event bus.
	remoteQueueSize = 1024
	// remotePublishTimeout bounds one cross-node publish.
	remotePublishTimeout = 2 * time.Second
	// remoteCloseTimeout bounds how long Close waits for the queue to empty.
	remoteCloseTimeout = 2 * time.Second
)

// Reasons a persisted row did not reach other nodes' live tails.
const (
	tailDropQueueFull     = "queue_full"
	tailDropPublishFailed = "publish_failed"
)

var tailDropped = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "silo_log_tail_publish_dropped_total",
	Help: "Persisted log rows this node did not send to other nodes' admin live tails, by reason. The rows are in Postgres.",
}, []string{"stream", "reason"})

type remoteAppend struct {
	stream Stream
	event  cache.Event
}

type Hub struct {
	mu          sync.RWMutex
	subscribers map[*subscriber]struct{}
	sourceID    string
	eventBus    cache.EventBus

	// Cross-node publishing; nil without an event bus.
	remote     chan remoteAppend
	remoteStop chan struct{}
	remoteDone chan struct{}
	closeOnce  sync.Once
}

// NewHub returns a hub for this node. With an event bus it starts the
// goroutine that publishes this node's rows to other nodes; Close stops it.
func NewHub(sourceID string, eventBus cache.EventBus) *Hub {
	h := &Hub{
		subscribers: make(map[*subscriber]struct{}),
		sourceID:    sourceID,
		eventBus:    eventBus,
	}
	if eventBus != nil {
		h.remote = make(chan remoteAppend, remoteQueueSize)
		h.remoteStop = make(chan struct{})
		h.remoteDone = make(chan struct{})
		go h.runRemote()
	}
	return h
}

// Close sends the rows already queued for other nodes, waiting at most
// remoteCloseTimeout, and stops cross-node publishing. Call it after the last
// PublishAppends that should reach other nodes and before the event bus
// closes. Local fan-out keeps working.
func (h *Hub) Close() {
	if h == nil || h.remote == nil {
		return
	}
	h.closeOnce.Do(func() { close(h.remoteStop) })
	select {
	case <-h.remoteDone:
	case <-time.After(remoteCloseTimeout):
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

// PublishAppends fans out a batch of freshly persisted entries. Local
// subscribers receive them before it returns. Other nodes receive them through
// a bounded queue that the hub publishes from its own goroutine, so PublishAppends
// never waits on the event bus; a full queue drops rows and counts them in
// silo_log_tail_publish_dropped_total. It returns how many entries could not be
// encoded and the last such error.
func PublishAppends[T any](h *Hub, stream Stream, entries []T) (int, error) {
	failed := 0
	var lastErr error
	for i := range entries {
		if err := h.PublishAppend(stream, entries[i]); err != nil {
			failed++
			lastErr = err
		}
	}
	return failed, lastErr
}

// PublishAppend fans out one persisted entry; see PublishAppends.
func (h *Hub) PublishAppend(stream Stream, entry any) error {
	if h == nil {
		return nil
	}

	raw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log stream entry: %w", err)
	}

	h.publishLocal(Message{
		Type:   MessageTypeAppend,
		Stream: stream,
		Entry:  raw,
	})

	if h.remote == nil {
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

	select {
	case h.remote <- remoteAppend{stream: stream, event: cache.Event{Type: eventType, Payload: string(payload)}}:
	default:
		tailDropped.WithLabelValues(string(stream), tailDropQueueFull).Inc()
	}
	return nil
}

// runRemote publishes queued rows to other nodes until Close. It logs only
// when publishing starts failing and when it recovers: those records are
// persisted and published themselves, so logging every failure would keep the
// queue busy with records about its own failures.
func (h *Hub) runRemote() {
	defer close(h.remoteDone)
	healthy := true
	for {
		select {
		case msg := <-h.remote:
			healthy = h.publishRemote(msg, healthy)
		case <-h.remoteStop:
			// Send what is queued now. After the first failure, count the
			// rest instead of waiting on each, so Close stays bounded.
			for n := len(h.remote); n > 0; n-- {
				msg := <-h.remote
				if healthy {
					healthy = h.publishRemote(msg, healthy)
					continue
				}
				tailDropped.WithLabelValues(string(msg.stream), tailDropPublishFailed).Inc()
			}
			return
		}
	}
}

func (h *Hub) publishRemote(msg remoteAppend, wasHealthy bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), remotePublishTimeout)
	err := h.eventBus.Publish(ctx, cache.ChannelLogs, msg.event)
	cancel()
	if err != nil {
		tailDropped.WithLabelValues(string(msg.stream), tailDropPublishFailed).Inc()
		if wasHealthy {
			slog.Warn("log stream: cross-node publish failing; other nodes' live tails miss this node's rows until it recovers",
				"component", "logstream", "error", err)
		}
		return false
	}
	if !wasHealthy {
		slog.Info("log stream: cross-node publish recovered", "component", "logstream")
	}
	return true
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
