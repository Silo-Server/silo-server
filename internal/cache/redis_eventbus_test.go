package cache

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestRedisEventBusTakesDatabaseNumberFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"redis://localhost:6379", 0},
		{"redis://localhost:6379/7", 7},
		{"redis://localhost:6379?db=4", 4},
		{"redis://sentinel-1:26379/5?master_name=mymaster", 5},
		// Not a URL: the bus falls back to treating it as an address.
		{"localhost:6379", 0},
	} {
		bus := newRedisEventBus(config.RedisConfig{URL: tc.url})
		if bus.db != tc.want {
			t.Errorf("newRedisEventBus(%q) scopes channels to database %d, want %d", tc.url, bus.db, tc.want)
		}
		if err := bus.Close(); err != nil {
			t.Errorf("close bus for %q: %v", tc.url, err)
		}
	}
}

func TestRedisClientsTakeDatabaseNumberFromRedisDB(t *testing.T) {
	for _, cfg := range []config.RedisConfig{
		{URL: "redis://localhost:6379", DB: "5"},
		{URL: "redis://localhost:6379/7", DB: "5"},
		{URL: "redis://sentinel-1:26379/7?master_name=mymaster", DB: "5"},
	} {
		bus := newRedisEventBus(cfg)
		if bus.db != 5 {
			t.Errorf("newRedisEventBus(%+v) scopes channels to database %d, want redis.db's 5", cfg, bus.db)
		}
		if err := bus.Close(); err != nil {
			t.Errorf("close bus for %+v: %v", cfg, err)
		}

		client, err := NewRedisClientForRole(cfg, "test")
		if err != nil {
			t.Fatalf("NewRedisClientForRole(%+v): %v", cfg, err)
		}
		if got := client.Options().DB; got != 5 {
			t.Errorf("NewRedisClientForRole(%+v) selects database %d, want redis.db's 5", cfg, got)
		}
		if err := client.Close(); err != nil {
			t.Errorf("close client for %+v: %v", cfg, err)
		}
	}

	// The URL is fine here, so the error must not blame it.
	client, err := NewRedisClientForRole(config.RedisConfig{URL: "redis://localhost:6379/7", DB: "five"}, "test")
	if err == nil {
		_ = client.Close()
		t.Fatal("NewRedisClientForRole built a client for a redis.db that is not a number")
	}
	if got := err.Error(); !strings.Contains(got, config.RedisDBSettingKey) || strings.Contains(got, "redis URL") {
		t.Errorf("error for a redis.db that is not a number = %q, want one that names redis.db and not the URL", got)
	}
}

// Runs without a Redis server: the name Redis receives must carry the database
// number that the same connection selected.
func TestRedisEventBusSendsScopedChannelToRedis(t *testing.T) {
	bare := []string{"SUBSCRIBE silo:admin on database 0", "PUBLISH silo:admin on database 0"}
	db5 := []string{"SUBSCRIBE silo:admin@db5 on database 5", "PUBLISH silo:admin@db5 on database 5"}
	for _, tc := range []struct {
		name string
		path string
		db   string
		want []string
		bare bool
	}{
		{"database 3", "/3", "", []string{"SUBSCRIBE silo:admin@db3 on database 3", "PUBLISH silo:admin@db3 on database 3"}, false},
		{"database 0", "/0", "", bare, false},
		{"no database number", "", "", bare, false},
		{"negative database number", "/-1", "", bare, false},
		{"redis.db replaces the number in the URL", "/3", "5", db5, false},
		{"redis.db on a URL without a number", "", "5", db5, false},
		{"redis.db 0 replaces the number in the URL", "/3", "0", bare, false},
		{"redis.db on a bare address", "", "5", db5, true},
		{"normalized redis.db on a bare address", "", " 05 ", db5, true},
		{"redis.db 0 on a bare address", "", "0", bare, true},
		{"no redis.db on a bare address", "", "", bare, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := startRESPTestServer(t, nil, respTestRedis)
			redisURL := "redis://" + server.addr + tc.path
			if tc.bare {
				redisURL = server.addr
			}
			bus := newRedisEventBus(config.RedisConfig{URL: redisURL, DB: tc.db})
			t.Cleanup(func() { _ = bus.Close() })

			if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if err := bus.Publish(t.Context(), ChannelAdmin, Event{Type: EventSettingsChanged}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if got := server.pubsub(); !slices.Equal(got, tc.want) {
				t.Errorf("Redis received %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedisEventBusRejectsInvalidDatabaseOnBareAddress(t *testing.T) {
	for _, db := range []string{"five", "-1", "999999999999999999999999999999"} {
		t.Run(db, func(t *testing.T) {
			server := startRESPTestServer(t, nil, respTestRedis)
			bus := newRedisEventBus(config.RedisConfig{URL: server.addr, DB: db})
			t.Cleanup(func() { _ = bus.Close() })

			if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err == nil || !strings.Contains(err.Error(), config.RedisDBSettingKey) {
				t.Errorf("subscribe with invalid redis.db: %v, want a redis.db error", err)
			}
			if err := bus.Publish(t.Context(), ChannelAdmin, Event{Type: EventSettingsChanged}); err == nil || !strings.Contains(err.Error(), config.RedisDBSettingKey) {
				t.Errorf("publish with invalid redis.db: %v, want a redis.db error", err)
			}
			if got := server.received(); len(got) != 0 {
				t.Errorf("Redis received %q for an invalid redis.db, want no commands", got)
			}
		})
	}
}

func testRedisOptions(t *testing.T, db int) *redis.Options {
	t.Helper()
	rawURL := os.Getenv("SILO_TEST_REDIS_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	options, err := redis.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	options.DB = db
	return options
}

func testEventBus(t *testing.T, db int) *RedisEventBus {
	t.Helper()
	bus := newRedisEventBusFromClient(redis.NewClient(testRedisOptions(t, db)), false)
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func collectEvents(t *testing.T, bus *RedisEventBus, channel string) <-chan Event {
	t.Helper()
	events := make(chan Event, 8)
	if err := bus.Subscribe(t.Context(), channel, func(event Event) { events <- event }); err != nil {
		t.Fatalf("subscribe to %s: %v", channel, err)
	}
	return events
}

func nextEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
		return Event{}
	}
}

// Two installs sharing one Redis server on different database numbers must
// not receive each other's events, while the nodes of one install still do.
func TestRedisEventBusKeepsDatabaseNumbersApart(t *testing.T) {
	node, peer, otherInstall := testEventBus(t, 1), testEventBus(t, 1), testEventBus(t, 2)
	channel := fmt.Sprintf("silo:test:%d", time.Now().UnixNano())
	peerEvents := collectEvents(t, peer, channel)
	otherEvents := collectEvents(t, otherInstall, channel)
	ctx := t.Context()

	if err := node.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "database 1"}); err != nil {
		t.Fatalf("publish on database 1: %v", err)
	}
	if got := nextEvent(t, peerEvents); got.Payload != "database 1" {
		t.Fatalf("peer on the same database received %+v, want the database 1 event", got)
	}

	// Redis queues messages to a subscriber in publish order, so an event
	// leaked from database 1 would arrive ahead of this one.
	if err := otherInstall.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "database 2"}); err != nil {
		t.Fatalf("publish on database 2: %v", err)
	}
	if got := nextEvent(t, otherEvents); got.Payload != "database 2" {
		t.Fatalf("bus on database 2 received %+v first, want only its own event", got)
	}

	if err := node.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "database 1 again"}); err != nil {
		t.Fatalf("publish on database 1: %v", err)
	}
	if got := nextEvent(t, peerEvents); got.Payload != "database 1 again" {
		t.Fatalf("peer on database 1 received %+v, want only database 1 events", got)
	}
}

// A node on database 0 must keep exchanging events with nodes built before
// channels were scoped, which publish and subscribe on the bare channel name.
func TestRedisEventBusOnDatabaseZeroUsesBareChannel(t *testing.T) {
	bus := testEventBus(t, 0)
	channel := fmt.Sprintf("silo:test:%d", time.Now().UnixNano())
	events := collectEvents(t, bus, channel)
	ctx := t.Context()

	older := redis.NewClient(testRedisOptions(t, 0))
	t.Cleanup(func() { _ = older.Close() })
	pubsub := older.Subscribe(ctx, channel)
	t.Cleanup(func() { _ = pubsub.Close() })
	if _, err := pubsub.Receive(ctx); err != nil {
		t.Fatalf("subscribe to the bare channel: %v", err)
	}

	if err := bus.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "from the bus"}); err != nil {
		t.Fatalf("publish from the bus: %v", err)
	}
	select {
	case msg := <-pubsub.Channel():
		var got Event
		if err := json.Unmarshal([]byte(msg.Payload), &got); err != nil || got.Payload != "from the bus" {
			t.Fatalf("bare channel carried %q (%v), want the bus event", msg.Payload, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the bare channel did not carry the bus event")
	}
	if got := nextEvent(t, events); got.Payload != "from the bus" {
		t.Fatalf("bus received %+v, want its own event", got)
	}

	data, err := json.Marshal(Event{Type: EventSettingsChanged, Payload: "from an older node"})
	if err != nil {
		t.Fatal(err)
	}
	if err := older.Publish(ctx, channel, data).Err(); err != nil {
		t.Fatalf("publish on the bare channel: %v", err)
	}
	if got := nextEvent(t, events); got.Payload != "from an older node" {
		t.Fatalf("bus received %+v, want the event published on the bare channel", got)
	}
}
