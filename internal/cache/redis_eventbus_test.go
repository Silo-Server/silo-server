package cache

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestRedisChannelScopesByDatabaseNumber(t *testing.T) {
	channels := []string{ChannelCatalog, ChannelAdmin, ChannelPlayback, ChannelLogs, ChannelEvents}
	owners := map[string]string{}
	for _, channel := range channels {
		if got := redisChannel(channel, 0); got != channel {
			t.Errorf("redisChannel(%q, 0) = %q, want the bare name older nodes use", channel, got)
		}
		for _, db := range []int{0, 1, 2, 15} {
			name := redisChannel(channel, db)
			owner := fmt.Sprintf("%s on database %d", channel, db)
			if other, shared := owners[name]; shared {
				t.Errorf("%s and %s share Redis channel %q", other, owner, name)
			}
			owners[name] = owner
		}
	}
	if got, want := redisChannel(ChannelAdmin, 3), "silo:admin@db3"; got != want {
		t.Errorf("redisChannel(ChannelAdmin, 3) = %q, want %q", got, want)
	}
	if got := redisChannel(ChannelAdmin, -1); got != ChannelAdmin {
		t.Errorf("redisChannel(ChannelAdmin, -1) = %q, want the bare name", got)
	}
}

func TestRedisEventBusTakesDatabaseNumberFromURL(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want int
	}{
		{"redis://localhost:6379", 0},
		{"redis://localhost:6379/7", 7},
		{"redis://localhost:6379?db=4", 4},
		// Not a URL: the bus falls back to treating it as an address.
		{"localhost:6379", 0},
	} {
		bus := newRedisEventBus(tc.url)
		if bus.db != tc.want {
			t.Errorf("newRedisEventBus(%q) scopes channels to database %d, want %d", tc.url, bus.db, tc.want)
		}
		if err := bus.Close(); err != nil {
			t.Errorf("close bus for %q: %v", tc.url, err)
		}
	}
}

// fakeRedis speaks enough RESP2 for a go-redis client to select a database,
// subscribe and publish. It records each SUBSCRIBE and PUBLISH with the
// database its connection had selected.
type fakeRedis struct {
	addr string

	mu     sync.Mutex
	conns  []net.Conn
	pubsub []string
}

func startFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRedis{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, conn := range f.conns {
			_ = conn.Close()
		}
		f.mu.Unlock()
		wg.Wait()
	})
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			wg.Go(func() { f.serve(conn) })
		}
	})
	return f
}

func (f *fakeRedis) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	db := 0
	for {
		args, err := readRESPCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		name := strings.ToUpper(args[0])
		// HELLO and CLIENT SETINFO: a Redis error keeps go-redis on RESP2.
		reply := "-ERR unknown command\r\n"
		switch {
		case name == "SELECT" && len(args) == 2:
			if db, err = strconv.Atoi(args[1]); err != nil {
				return
			}
			reply = "+OK\r\n"
		case (name == "SUBSCRIBE" || name == "PUBLISH") && len(args) >= 2:
			f.mu.Lock()
			f.pubsub = append(f.pubsub, fmt.Sprintf("%s %s on database %d", name, args[1], db))
			f.mu.Unlock()
			reply = ":0\r\n"
			if name == "SUBSCRIBE" {
				reply = fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(args[1]), args[1])
			}
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (f *fakeRedis) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.pubsub)
}

func readRESPCommand(r *bufio.Reader) ([]string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("unexpected RESP line %q", line)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return nil, err
	}
	args := make([]string, 0, n)
	for range n {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "$")))
		if err != nil {
			return nil, err
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		args = append(args, string(buf[:size]))
	}
	return args, nil
}

// Runs without a Redis server: the name Redis receives must carry the database
// number that the same connection selected.
func TestRedisEventBusSendsScopedChannelToRedis(t *testing.T) {
	bare := []string{"SUBSCRIBE silo:admin on database 0", "PUBLISH silo:admin on database 0"}
	for _, tc := range []struct {
		name string
		path string
		want []string
	}{
		{"database 3", "/3", []string{"SUBSCRIBE silo:admin@db3 on database 3", "PUBLISH silo:admin@db3 on database 3"}},
		{"database 0", "/0", bare},
		{"no database number", "", bare},
		{"negative database number", "/-1", bare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := startFakeRedis(t)
			bus := newRedisEventBus("redis://" + server.addr + tc.path)
			t.Cleanup(func() { _ = bus.Close() })

			if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			if err := bus.Publish(t.Context(), ChannelAdmin, Event{Type: EventSettingsChanged}); err != nil {
				t.Fatalf("publish: %v", err)
			}
			if got := server.recorded(); !slices.Equal(got, tc.want) {
				t.Errorf("Redis received %q, want %q", got, tc.want)
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
	bus := newRedisEventBusFromOptions(testRedisOptions(t, db))
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
