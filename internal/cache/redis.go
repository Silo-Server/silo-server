package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// Channel constants
// ---------------------------------------------------------------------------

const (
	// ChannelCatalog is the pub/sub channel for catalog events (scan_complete,
	// metadata_updated).
	ChannelCatalog = "silo:catalog"

	// ChannelAdmin is the pub/sub channel for admin events (user_disabled,
	// user_deleted, settings_changed).
	ChannelAdmin = "silo:admin"

	// ChannelPlayback is the pub/sub channel reserved for future playback
	// events.
	ChannelPlayback = "silo:playback"

	// ChannelLogs is the pub/sub channel for persisted operational/audit log
	// entries that should be fanned out to admin WebSocket subscribers.
	ChannelLogs = "silo:logs"

	// ChannelEvents is the pub/sub channel for passive websocket events.
	ChannelEvents = "silo:events"
)

// ---------------------------------------------------------------------------
// Event type constants
// ---------------------------------------------------------------------------

const (
	EventScanComplete                = "scan_complete"
	EventMetadataUpdated             = "metadata_updated"
	EventAdminStatsInvalidated       = "admin_stats_invalidated"
	EventPlaybackSessionsChanged     = "playback_sessions_changed"
	EventUserDisabled                = "user_disabled"
	EventUserDeleted                 = "user_deleted"
	EventSettingsChanged             = "settings_changed"
	EventPolicyChanged               = "policy_changed"
	EventMarkerProviderConfigChanged = "marker_provider_config_changed"
	EventNodePoolChanged             = "node_pool_changed"
	EventOperationalLogAppended      = "operational_log_appended"
	EventAuditLogAppended            = "audit_log_appended"
	EventEventsNotification          = "events_notification"
)

// ---------------------------------------------------------------------------
// Event
// ---------------------------------------------------------------------------

// Event represents a cache invalidation event transmitted over pub/sub.
type Event struct {
	Type    string `json:"type"`    // e.g. "user_disabled", "scan_complete"
	Payload string `json:"payload"` // e.g. user ID, library ID
}

// EventHandler is a callback invoked when an event is received on a
// subscribed channel.
type EventHandler func(event Event)

// ---------------------------------------------------------------------------
// EventBus interface
// ---------------------------------------------------------------------------

// EventBus is the interface for publishing and subscribing to cache
// invalidation events. When Redis is not configured the factory returns
// a no-op implementation so callers never need nil checks.
type EventBus interface {
	// Publish sends an event on the given channel.
	Publish(ctx context.Context, channel string, event Event) error

	// Subscribe registers a handler for events on the given channel.
	// The handler is called in a dedicated goroutine.
	Subscribe(ctx context.Context, channel string, handler EventHandler) error

	// Close tears down the bus and stops all subscriber goroutines.
	// It is safe to call Close multiple times.
	Close() error
}

// NewEventBus returns an EventBus. When redisURL is empty a no-op
// implementation is returned that silently succeeds on every call and
// has zero external dependencies.
func NewEventBus(redisURL string) EventBus {
	if redisURL == "" {
		return &NoopEventBus{}
	}
	return newRedisEventBus(redisURL)
}

// ---------------------------------------------------------------------------
// NoopEventBus
// ---------------------------------------------------------------------------

// NoopEventBus is a silent no-op implementation of EventBus used when
// Redis is not configured. All methods return nil without doing anything.
type NoopEventBus struct{}

// Publish is a no-op that always returns nil.
func (n *NoopEventBus) Publish(_ context.Context, _ string, _ Event) error { return nil }

// Subscribe is a no-op that always returns nil. The handler is never called.
func (n *NoopEventBus) Subscribe(_ context.Context, _ string, _ EventHandler) error { return nil }

// Close is a no-op that always returns nil. Safe to call multiple times.
func (n *NoopEventBus) Close() error { return nil }

// ---------------------------------------------------------------------------
// RedisEventBus
// ---------------------------------------------------------------------------

// subscription tracks a single Redis pub/sub subscription so it can be
// torn down on Close.
type subscription struct {
	pubsub *redis.PubSub
	cancel context.CancelFunc
}

// RedisEventBus implements EventBus using Redis pub/sub. Events are
// JSON-serialized before being published.
type RedisEventBus struct {
	client *redis.Client

	mu   sync.Mutex
	subs []subscription
	once sync.Once // ensures Close is idempotent
	done chan struct{}
}

// newRedisEventBus creates a RedisEventBus connected to the given Redis URL.
func newRedisEventBus(redisURL string) *RedisEventBus {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		// If the URL cannot be parsed, treat it as a simple address.
		opts = &redis.Options{Addr: redisURL}
	}
	return &RedisEventBus{
		client: redis.NewClient(opts),
		done:   make(chan struct{}),
	}
}

// Publish serializes the event as JSON and publishes it on the given
// Redis channel.
func (r *RedisEventBus) Publish(ctx context.Context, channel string, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, channel, data).Err()
}

// Subscribe creates a Redis pub/sub subscription on the given channel and
// spawns a goroutine that delivers incoming messages to the handler.
func (r *RedisEventBus) Subscribe(ctx context.Context, channel string, handler EventHandler) error {
	subCtx, cancel := context.WithCancel(ctx)
	pubsub := r.client.Subscribe(subCtx, channel)

	// Wait for the subscription to be confirmed.
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		return err
	}

	r.mu.Lock()
	r.subs = append(r.subs, subscription{pubsub: pubsub, cancel: cancel})
	r.mu.Unlock()

	go r.listen(pubsub, handler)
	return nil
}

// listen reads messages from the pub/sub channel and invokes the handler.
func (r *RedisEventBus) listen(pubsub *redis.PubSub, handler EventHandler) {
	ch := pubsub.Channel()
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var evt Event
			if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
				// Skip malformed messages.
				continue
			}
			handler(evt)
		case <-r.done:
			return
		}
	}
}

// Close stops all subscriber goroutines and closes the Redis client.
// It is safe to call Close multiple times; only the first call has any
// effect.
func (r *RedisEventBus) Close() error {
	var firstErr error
	r.once.Do(func() {
		close(r.done)

		r.mu.Lock()
		subs := r.subs
		r.subs = nil
		r.mu.Unlock()

		for _, s := range subs {
			if err := s.pubsub.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			s.cancel()
		}

		if err := r.client.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// ---------------------------------------------------------------------------
// Redis client factory
// ---------------------------------------------------------------------------

// NewRedisClient creates a Redis client based on the config.
// Returns nil and no error if no Redis URL or Sentinel config is provided.
// Returns an error if configuration is present but invalid.
func NewRedisClient(cfg config.RedisConfig) (*redis.Client, error) {
	if cfg.SentinelMaster != "" && len(cfg.SentinelAddresses) > 0 {
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:       cfg.SentinelMaster,
			SentinelAddrs:    cfg.SentinelAddresses,
			SentinelPassword: cfg.SentinelPassword,
			DB:               0,
		}), nil
	}
	if cfg.URL == "" {
		return nil, nil
	}
	opt, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	return redis.NewClient(opt), nil
}
