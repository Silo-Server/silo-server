package cache

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

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
	EventMarkersUpdated              = "markers_updated"
	EventMarkerThumbnailReady        = "marker_thumbnail_ready"
	EventSubtitleTimingChanged       = "subtitle_timing_changed"
	EventSubtitleSyncUpdated         = "subtitle_sync_updated"
	EventUserDisabled                = "user_disabled"
	EventUserDeleted                 = "user_deleted"
	EventSettingsChanged             = "settings_changed"
	EventPolicyChanged               = "policy_changed"
	EventMarkerProviderConfigChanged = "marker_provider_config_changed"
	EventNodePoolChanged             = "node_pool_changed"
	EventOperationalLogAppended      = "operational_log_appended"
	EventAuditLogAppended            = "audit_log_appended"
	EventEventsNotification          = "events_notification"
	// EventPluginsChanged is published on ChannelAdmin by the API server after
	// every plugin lifecycle change (install, enable, disable, config save,
	// auto-update, uninstall) so proxy nodes running resident plugins from the
	// same installations reconcile at once instead of on their next poll, and
	// every API replica's plugin event dispatcher rebuilds its subscriber index.
	EventPluginsChanged = "plugins_changed"
	// EventCatalogItemChanged is published on ChannelCatalog after an admin
	// changes one catalog item's identity, fields or artwork outside a scan.
	// The payload is the content ID; every API node evicts the cached home
	// rails that list it.
	EventCatalogItemChanged = "catalog_item_changed"
)

// EventAuthProvidersChanged is published on ChannelAdmin after an auth
// binding write, so every API replica rebuilds its sign-in providers without
// a restart. Plugin install, config and removal changes arrive as
// EventPluginsChanged and trigger the same rebuild.
const EventAuthProvidersChanged = "auth_providers_changed"

// EventUserSessionsRevoked is published on ChannelAdmin with a user ID whose
// login sessions were revoked, so every API replica drops that account's
// in-memory Jellyfin-compatible sessions.
const EventUserSessionsRevoked = "user_sessions_revoked"

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

// NewEventBus returns an EventBus. When cfg has no URL a no-op
// implementation is returned that silently succeeds on every call and
// has zero external dependencies.
func NewEventBus(cfg config.RedisConfig) EventBus {
	if cfg.URL == "" {
		return &NoopEventBus{}
	}
	return newRedisEventBus(cfg)
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
	// db is the database number the client selects; see redisChannel.
	db int

	// Set for a Sentinel URL only. A subscription pings its server every
	// pingInterval and leaves one that has been silent for silenceLimit.
	pingInterval time.Duration
	silenceLimit time.Duration

	mu   sync.Mutex
	subs []subscription
	once sync.Once // ensures Close is idempotent
	done chan struct{}
}

// newRedisEventBus creates a RedisEventBus connected to the Redis cfg names.
func newRedisEventBus(cfg config.RedisConfig) *RedisEventBus {
	client, sentinel, err := newRedisClient(cfg, false)
	if err != nil {
		client = redis.NewClient(unparsedRedisOptions(cfg, err))
	}
	return newRedisEventBusFromClient(client, sentinel)
}

// newRedisEventBusFromClient creates a RedisEventBus on client. sentinel says
// whether the client follows the master of a Sentinel deployment.
func newRedisEventBusFromClient(client *redis.Client, sentinel bool) *RedisEventBus {
	bus := &RedisEventBus{
		client: instrumentRedis(client, "events"),
		db:     client.Options().DB,
		done:   make(chan struct{}),
	}
	if sentinel {
		bus.pingInterval, bus.silenceLimit = 3*time.Second, 10*time.Second
	}
	return bus
}

// unparsedRedisOptions treats a value that is not a URL as a bare address,
// applying the validated database override before the client is built.
// Anything else can carry passwords, and go-redis quotes the address in dial
// errors, so every command fails with the parse error instead.
func unparsedRedisOptions(cfg config.RedisConfig, parseErr error) *redis.Options {
	if isBareRedisAddress(cfg.URL) {
		db, err := config.NormalizeRedisDB(cfg.DB)
		if err == nil {
			options := &redis.Options{Addr: cfg.URL}
			if db != "" {
				// NormalizeRedisDB has already validated the integer.
				options.DB, _ = strconv.Atoi(db)
			}
			return options
		}
		parseErr = err
	}
	return &redis.Options{
		Addr: "invalid-redis-url",
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			return nil, parseErr
		},
	}
}

// isBareRedisAddress reports whether value is a host:port with a numeric
// port, or the path of a socket. A host name, the zone of an IPv6 address
// and the parts of a path are made of letters, digits, dots, hyphens and
// underscores.
func isBareRedisAddress(value string) bool {
	if strings.HasPrefix(value, "/") {
		return strings.Trim(value, redisHostNameCharacters+"/") == ""
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	address, zone, _ := strings.Cut(host, "%")
	if strings.Trim(zone, redisHostNameCharacters) != "" {
		return false
	}
	if net.ParseIP(address) == nil && (zone != "" || strings.Trim(address, redisHostNameCharacters) != "") {
		return false
	}
	_, err = strconv.ParseUint(port, 10, 16)
	return err == nil
}

const redisHostNameCharacters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-_"

// redisChannel returns the Redis channel that carries an event-bus channel
// for a client on the given database number. Redis delivers a published
// message to every subscriber of a channel whatever database either
// connection selected, so installs that share one Redis server on different
// database numbers would otherwise receive each other's events. Database 0
// keeps the bare name, which is the name every node used before channels were
// scoped. So does a negative number: go-redis accepts one in a URL and leaves
// the connection on database 0.
func redisChannel(channel string, db int) string {
	if db <= 0 {
		return channel
	}
	return channel + "@db" + strconv.Itoa(db)
}

// Publish serializes the event as JSON and publishes it on the given
// channel, scoped to the bus's database number.
func (r *RedisEventBus) Publish(ctx context.Context, channel string, event Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, redisChannel(channel, r.db), data).Err()
}

// Subscribe creates a Redis pub/sub subscription on the given channel,
// scoped to the bus's database number, and spawns a goroutine that delivers
// incoming messages to the handler.
func (r *RedisEventBus) Subscribe(ctx context.Context, channel string, handler EventHandler) error {
	subCtx, cancel := context.WithCancel(ctx)
	pubsub := r.client.Subscribe(subCtx, redisChannel(channel, r.db))

	// Wait for the subscription to be confirmed.
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		return err
	}

	// A Sentinel subscription pings and redials for as long as the bus is
	// open, which can be long after the caller's context has ended.
	listenCtx, stopListening := context.WithCancel(context.Background())
	sub := subscription{pubsub: pubsub, cancel: func() { cancel(); stopListening() }}

	r.mu.Lock()
	select {
	case <-r.done:
		// Close has taken the list of subscriptions and would never end
		// this one.
		r.mu.Unlock()
		sub.cancel()
		_ = pubsub.Close()
		return errors.New("redis event bus is closed")
	default:
	}
	r.subs = append(r.subs, sub)
	r.mu.Unlock()

	if r.silenceLimit > 0 {
		go r.keepAlive(listenCtx, pubsub)
		go r.listenToMaster(listenCtx, pubsub, handler)
	} else {
		go r.listen(pubsub, handler)
	}
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

// listenToMaster is listen for a Sentinel deployment, where the server behind
// a subscription can stop being the master.
//
// go-redis reconnects a subscription only when its connection breaks. A
// master that freezes or drops off the network leaves the connection open
// and silent, so after Sentinel promotes a replica the bus would publish to
// the new master and keep listening to the old one. Here each receive has a
// deadline, keepAlive makes a healthy server answer within it, and go-redis
// drops a connection whose deadline passes. The next connection goes to the
// master Sentinel names at that moment.
func (r *RedisEventBus) listenToMaster(ctx context.Context, pubsub *redis.PubSub, handler EventHandler) {
	for {
		receiveCtx, cancel := context.WithTimeout(ctx, r.silenceLimit)
		msg, err := pubsub.Receive(receiveCtx)
		cancel()
		if err != nil {
			if !redial(ctx, pubsub) {
				return
			}
			continue
		}
		message, ok := msg.(*redis.Message)
		if !ok {
			// A pong or a subscription confirmation.
			continue
		}
		var evt Event
		if err := json.Unmarshal([]byte(message.Payload), &evt); err != nil {
			// Skip malformed messages.
			continue
		}
		handler(evt)
	}
}

// redial retries until the subscription has a connection again, and reports
// false when ctx ended first, which Close does. It runs outside the receive
// deadline, which is for a server that has gone silent and not for the time
// a new connection takes. The client's dial, read and write timeouts limit
// each attempt.
func redial(ctx context.Context, pubsub *redis.PubSub) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
		if pubsub.Ping(ctx) == nil {
			return true
		}
	}
}

// keepAlive pings the subscription's server, so that an idle subscription
// still hears from a healthy one.
func (r *RedisEventBus) keepAlive(ctx context.Context, pubsub *redis.PubSub) {
	ticker := time.NewTicker(r.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A failed ping needs no handling: listenToMaster sees the same
			// broken or silent connection.
			_ = pubsub.Ping(ctx)
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

		// End the subscriptions' contexts before closing them. A Sentinel
		// subscription that is redialling holds its lock until the dial
		// ends, and Close would wait for each of them in turn.
		for _, s := range subs {
			s.cancel()
		}
		for _, s := range subs {
			if err := s.pubsub.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}

		if err := CloseRedisClient(r.client); err != nil && firstErr == nil {
			firstErr = err
		}
	})
	return firstErr
}

// ---------------------------------------------------------------------------
// Redis client factory
// ---------------------------------------------------------------------------

// NewRedisClient creates a Redis client based on the config.
// Returns nil and no error if no Redis URL is provided.
// Returns an error if configuration is present but invalid.
func NewRedisClient(cfg config.RedisConfig) (*redis.Client, error) {
	return NewRedisClientForRole(cfg, "application")
}

// NewRedisClientForRole associates all standalone or Sentinel operations and
// pool pressure with a bounded operational role.
func NewRedisClientForRole(cfg config.RedisConfig, role string) (*redis.Client, error) {
	return newRedisClientForRole(cfg, role, false)
}

// NewDeadlineRedisClientForRole is NewRedisClientForRole for callers that must
// not wait past their context's deadline, such as a sweep other work waits on.
// Its socket reads and writes honor that deadline, so a Redis that stops
// answering frees the connection when the caller gives up instead of holding
// it for the URL's read timeout, which may be unlimited.
func NewDeadlineRedisClientForRole(cfg config.RedisConfig, role string) (*redis.Client, error) {
	return newRedisClientForRole(cfg, role, true)
}

func newRedisClientForRole(cfg config.RedisConfig, role string, honorContextDeadlines bool) (*redis.Client, error) {
	if cfg.URL == "" {
		return nil, nil
	}
	client, _, err := newRedisClient(cfg, honorContextDeadlines)
	if err != nil {
		return nil, err
	}
	return instrumentRedis(client, role), nil
}

// newRedisClient builds the client cfg names and reports whether its URL is
// a Sentinel URL. A Sentinel client asks Sentinel for the master each time it
// opens a connection, and closes its pooled connections when Sentinel
// announces a new master.
func newRedisClient(cfg config.RedisConfig, honorContextDeadlines bool) (*redis.Client, bool, error) {
	options, failover, err := cfg.Options()
	if err != nil {
		return nil, false, err
	}
	if failover != nil {
		failover.ContextTimeoutEnabled = failover.ContextTimeoutEnabled || honorContextDeadlines
		return redis.NewFailoverClient(failover), true, nil
	}
	options.ContextTimeoutEnabled = options.ContextTimeoutEnabled || honorContextDeadlines
	return redis.NewClient(options), false, nil
}
