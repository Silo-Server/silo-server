package cache

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/redis/go-redis/v9"
)

// respTestServer speaks enough RESP2 for a go-redis client to connect,
// authenticate, select a database and run a few commands. It records every
// command it receives, and each SUBSCRIBE and PUBLISH with the database its
// connection had selected. reply answers the commands only one kind of
// server knows.
type respTestServer struct {
	addr  string
	reply func(args []string) (string, bool)
	// stop closes the listener and every connection, as a server does that
	// went away. It also runs when the test ends.
	stop func()

	mu       sync.Mutex
	stopped  bool
	conns    []net.Conn
	commands []string
	// perConn holds the same commands, one slice per connection.
	perConn [][]string
	// scoped holds each SUBSCRIBE and PUBLISH with its connection's database.
	scoped []string
	// ended counts the connections that have closed.
	ended int
}

// startRESPTestServer listens on a loopback port, with TLS when serverTLS is
// set.
func startRESPTestServer(t *testing.T, serverTLS *tls.Config, reply func(args []string) (string, bool)) *respTestServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &respTestServer{addr: ln.Addr().String(), reply: reply}
	if serverTLS != nil {
		ln = tls.NewListener(ln, serverTLS)
	}
	var wg sync.WaitGroup
	s.stop = sync.OnceFunc(func() {
		s.mu.Lock()
		s.stopped = true
		for _, conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		_ = ln.Close()
		wg.Wait()
	})
	t.Cleanup(s.stop)
	wg.Go(func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				_ = conn.Close()
				continue
			}
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			wg.Go(func() { s.serve(conn) })
		}
	})
	return s
}

func (s *respTestServer) serve(conn net.Conn) {
	r := bufio.NewReader(conn)
	s.mu.Lock()
	id := len(s.perConn)
	s.perConn = append(s.perConn, nil)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.ended++
		s.mu.Unlock()
	}()
	db := "0"
	for {
		args, err := readRESPTestCommand(r)
		if err != nil || len(args) == 0 {
			return
		}
		// go-redis sends command names in lower case.
		args[0] = strings.ToUpper(args[0])
		s.mu.Lock()
		s.commands = append(s.commands, strings.Join(args, " "))
		s.perConn[id] = append(s.perConn[id], strings.Join(args, " "))
		switch {
		case args[0] == "SELECT" && len(args) == 2:
			db = args[1]
		case (args[0] == "SUBSCRIBE" || args[0] == "PUBLISH") && len(args) >= 2:
			s.scoped = append(s.scoped, fmt.Sprintf("%s %s on database %s", args[0], args[1], db))
		}
		s.mu.Unlock()

		// HELLO and CLIENT SETINFO: a Redis error keeps go-redis on RESP2.
		reply := "-ERR unknown command\r\n"
		if custom, ok := s.reply(args); ok {
			reply = custom
		} else {
			switch args[0] {
			case "AUTH", "SELECT", "SET":
				reply = "+OK\r\n"
			case "PUBLISH":
				reply = ":0\r\n"
			case "SUBSCRIBE":
				var b strings.Builder
				for i, channel := range args[1:] {
					fmt.Fprintf(&b, "*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:%d\r\n", len(channel), channel, i+1)
				}
				reply = b.String()
			}
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

func (s *respTestServer) received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.commands)
}

// pubsub returns each SUBSCRIBE and PUBLISH the server received, with the
// database its connection had selected.
func (s *respTestServer) pubsub() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.scoped)
}

// count returns how many times the server received command.
func (s *respTestServer) count(command string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, got := range s.commands {
		if got == command {
			n++
		}
	}
	return n
}

// endedConnections returns how many of the server's connections have closed.
func (s *respTestServer) endedConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// connectionThatReceived returns every command of the first connection that
// received command.
func (s *respTestServer) connectionThatReceived(command string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, commands := range s.perConn {
		if slices.Contains(commands, command) {
			return slices.Clone(commands)
		}
	}
	return nil
}

func readRESPTestCommand(r *bufio.Reader) ([]string, error) {
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

// respTestRedis answers only the commands every respTestServer knows.
func respTestRedis([]string) (string, bool) { return "", false }

// respTestMasterLookup is what a client sends a Sentinel each time it dials
// the master.
const respTestMasterLookup = "SENTINEL get-master-addr-by-name mymaster"

// respTestSentinel answers as a Sentinel that names the server at addr as the
// master.
func respTestSentinel(t *testing.T, addr string) func(args []string) (string, bool) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return func(args []string) (string, bool) {
		if len(args) < 2 || args[0] != "SENTINEL" {
			return "", false
		}
		switch strings.ToLower(args[1]) {
		case "get-master-addr-by-name":
			return fmt.Sprintf("*2\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(host), host, len(port), port), true
		case "sentinels":
			return "*0\r\n", true
		}
		return "", false
	}
}

// Runs without Redis: a fake Sentinel names a fake master. Both a key-value
// client and the event bus must ask Sentinel for the master, reach it on the
// database number from the URL, and give each server its own credentials.
func TestSentinelURLReachesTheMasterSentinelNames(t *testing.T) {
	master := startRESPTestServer(t, nil, respTestRedis)
	sentinel := startRESPTestServer(t, nil, respTestSentinel(t, master.addr))
	rawURL := "redis://watcher:sentinel-secret@" + sentinel.addr + "/4?master_name=mymaster&username=silo&password=redis-secret"

	client, err := NewRedisClientForRole(config.RedisConfig{URL: rawURL}, "checks")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = CloseRedisClient(client) })
	if err := client.Set(t.Context(), "key", "value", 0).Err(); err != nil {
		t.Fatalf("write through Sentinel: %v", err)
	}
	bus := NewEventBus(config.RedisConfig{URL: rawURL})
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err != nil {
		t.Fatalf("subscribe through Sentinel: %v", err)
	}

	atSentinel, atMaster := sentinel.received(), master.received()
	for _, want := range []string{"AUTH watcher sentinel-secret", "SENTINEL get-master-addr-by-name mymaster"} {
		if !slices.Contains(atSentinel, want) {
			t.Errorf("Sentinel did not receive %q; it received %q", want, atSentinel)
		}
	}
	subscribe := "SUBSCRIBE " + redisChannel(ChannelAdmin, 4)
	for _, want := range []string{"AUTH silo redis-secret", "SELECT 4", "SET key value", subscribe} {
		if !slices.Contains(atMaster, want) {
			t.Errorf("the master did not receive %q; it received %q", want, atMaster)
		}
	}
	// The key-value client alone would satisfy the checks above.
	subscriber := master.connectionThatReceived(subscribe)
	for _, want := range []string{"AUTH silo redis-secret", "SELECT 4"} {
		if !slices.Contains(subscriber, want) {
			t.Errorf("the event bus connection did not send %q; it sent %q", want, subscriber)
		}
	}
	for _, command := range atSentinel {
		if strings.Contains(command, "redis-secret") || strings.HasPrefix(command, "SET ") || strings.HasPrefix(command, "SUBSCRIBE "+ChannelAdmin) {
			t.Errorf("Sentinel received %q, which belongs to the master", command)
		}
	}
	for _, command := range atMaster {
		if strings.Contains(command, "sentinel-secret") {
			t.Errorf("the master received the Sentinel credentials in %q", command)
		}
	}
}

func TestNewRedisClientForRoleRejectsInvalidSentinelURL(t *testing.T) {
	if client, err := NewRedisClientForRole(config.RedisConfig{}, "checks"); client != nil || err != nil {
		t.Fatalf("no URL = (%v, %v), want no client and no error", client, err)
	}
	_, err := NewRedisClientForRole(config.RedisConfig{URL: "redis://sentinel-1:26379?master_name="}, "checks")
	if err == nil || !strings.Contains(err.Error(), "invalid redis URL") {
		t.Fatalf("Sentinel URL without a master name: err = %v, want an invalid redis URL error", err)
	}
}

// main logs the error a subscription returns, and go-redis quotes the dial
// address in it, so a URL that does not parse must not become the address.
func TestEventBusErrorDoesNotQuoteAnInvalidURL(t *testing.T) {
	bus := NewEventBus(config.RedisConfig{URL: "redis://watcher:sentinel-secret@127.0.0.1:26379?master_name=mymaster&password=redis-secret&route_randomly=true"})
	t.Cleanup(func() { _ = bus.Close() })
	err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {})
	if err == nil {
		t.Fatal("subscribing with an invalid URL succeeded")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the error quotes a password: %v", err)
	}
	if !strings.Contains(err.Error(), "route_randomly") {
		t.Fatalf("err = %v, want the reason the URL was rejected", err)
	}
}

func TestEventBusStillAcceptsABareAddress(t *testing.T) {
	server := startRESPTestServer(t, nil, respTestRedis)
	bus := NewEventBus(config.RedisConfig{URL: server.addr})
	t.Cleanup(func() { _ = bus.Close() })
	if err := bus.Subscribe(t.Context(), ChannelAdmin, func(Event) {}); err != nil {
		t.Fatalf("subscribe with a bare host:port: %v", err)
	}
	if got := server.received(); !slices.Contains(got, "SUBSCRIBE "+ChannelAdmin) {
		t.Fatalf("the server received %q, want the subscription", got)
	}
}

// Only a host:port with a numeric port, or a socket path, may become the dial
// address. Any other value can hold a password.
func TestUnparsedRedisValueIsDialedOnlyWhenItIsABareAddress(t *testing.T) {
	parseErr := errors.New("not a URL")
	for _, address := range []string{
		"redis.example:6379", "redis_1.example-a:6379", "127.0.0.1:6379", "[::1]:6379", "[fe80::1%eth0]:6379", ":6379",
		"/run/redis/redis.sock",
	} {
		options := unparsedRedisOptions(config.RedisConfig{URL: address, DB: "5"}, parseErr)
		if options.Addr != address || options.Dialer != nil || options.DB != 5 {
			t.Errorf("unparsedRedisOptions(%q) dials %q on database %d, want the address itself on database 5", address, options.Addr, options.DB)
		}
	}
	refused := []string{
		"redis://silo:secret@redis.example:6379/x",
		"redis://:secret",
		"silo:secret@redis.example:6379",
		"redis.example:6379?password=secret",
		"redis.example:6379,password=secret,ssl=True",
		"redis.example:6379 secret",
		"secret:redis.example:6379",
		"redis.example:6379/0#secret",
		"redis:secret",
	}
	// A host name has none of these characters.
	for _, separator := range []string{"@", "/", "?", "#", " ", "=", ",", ";", "&"} {
		refused = append(refused, "secret"+separator+"redis.example:6379")
	}
	// Nor has the zone of an IPv6 address, or the path of a socket.
	for _, separator := range []string{"@", "/", "?", "#", " ", "=", ",", ";", "&"} {
		refused = append(refused, "[fe80::1%secret"+separator+"redis.example]:6379")
	}
	for _, separator := range []string{"@", "?", "#", " ", "=", ",", ";", "&", ":"} {
		refused = append(refused, "/run/redis/redis.sock"+separator+"secret")
	}
	// A zone belongs to an IP address.
	refused = append(refused, "secret%redis.example:6379")
	for _, value := range refused {
		options := unparsedRedisOptions(config.RedisConfig{URL: value}, parseErr)
		if strings.Contains(options.Addr, "secret") || options.Dialer == nil {
			t.Errorf("unparsedRedisOptions(%q) dials %q, want no dial at all", value, options.Addr)
			continue
		}
		if _, err := options.Dialer(t.Context(), "tcp", options.Addr); !errors.Is(err, parseErr) {
			t.Errorf("unparsedRedisOptions(%q) fails with %v, want the parse error", value, err)
		}
	}
}

// respTestPong is the reply Redis gives a PING on a subscribed connection.
const respTestPong = "*2\r\n$4\r\npong\r\n$0\r\n\r\n"

// respTestMessage is a published event as a subscriber receives it.
func respTestMessage(t *testing.T, channel string, event Event) string {
	t.Helper()
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("*3\r\n$7\r\nmessage\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n", len(channel), channel, len(payload), payload)
}

// respTestAnswering answers pings and nothing else, as an idle master does on
// a subscribed connection.
func respTestAnswering(args []string) (string, bool) {
	return respTestPong, args[0] == "PING"
}

// respTestSilent takes pings and never answers, as a server does that froze
// or dropped off the network without closing its connections.
func respTestSilent(args []string) (string, bool) {
	return "", args[0] == "PING"
}

// respTestPublishing answers pings, and publishes event to every connection
// that subscribes.
func respTestPublishing(t *testing.T, event Event) func(args []string) (string, bool) {
	t.Helper()
	return func(args []string) (string, bool) {
		switch args[0] {
		case "PING":
			return respTestPong, true
		case "SUBSCRIBE":
			return fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(args[1]), args[1]) +
				respTestMessage(t, args[1], event), true
		}
		return "", false
	}
}

// A single-server URL keeps the receive path it had before Sentinel URLs.
func TestEventBusDeliversEventsFromASingleServer(t *testing.T) {
	want := Event{Type: EventSettingsChanged, Payload: "from a single server"}
	server := startRESPTestServer(t, nil, respTestPublishing(t, want))
	bus := newRedisEventBus(config.RedisConfig{URL: "redis://" + server.addr + "/2"})
	t.Cleanup(func() { _ = bus.Close() })
	events := make(chan Event, 64)
	if err := bus.Subscribe(t.Context(), ChannelAdmin, func(event Event) { events <- event }); err != nil {
		t.Fatalf("subscribe to a single server: %v", err)
	}
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("received %+v, want the event the server published", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("no event arrived; the server received %q", server.received())
	}
	if subscriber := server.connectionThatReceived("SUBSCRIBE " + redisChannel(ChannelAdmin, 2)); !slices.Contains(subscriber, "SELECT 2") {
		t.Fatalf("the subscription's connection sent %q, want it on database 2", subscriber)
	}
}

// Only a Sentinel URL gets the receive loop with a silence limit. A single
// server keeps the receive path it always had.
func TestOnlyASentinelURLGetsTheSilenceLimit(t *testing.T) {
	for _, single := range []string{"redis://127.0.0.1:6379/1", "127.0.0.1:6379"} {
		bus := newRedisEventBus(config.RedisConfig{URL: single})
		if bus.silenceLimit != 0 || bus.pingInterval != 0 {
			t.Errorf("newRedisEventBus(%q) has a silence limit of %s, want none", single, bus.silenceLimit)
		}
		_ = bus.Close()
	}
	bus := newRedisEventBus(config.RedisConfig{URL: "redis://127.0.0.1:26379/1?master_name=mymaster"})
	t.Cleanup(func() { _ = bus.Close() })
	if bus.silenceLimit != 10*time.Second || bus.pingInterval != 3*time.Second {
		t.Fatalf("a Sentinel URL gives silence limit %s and ping interval %s, want 10s and 3s", bus.silenceLimit, bus.pingInterval)
	}
}

// sentinelTestBus returns an event bus with intervals short enough for a
// test, and the fake Sentinel it asks. The Sentinel names the server master
// points at and takes lookupDelay to do so.
func sentinelTestBus(t *testing.T, master *atomic.Pointer[respTestServer], silenceLimit, lookupDelay time.Duration) (*RedisEventBus, *respTestServer) {
	t.Helper()
	sentinel := startRESPTestServer(t, nil, func(args []string) (string, bool) {
		if len(args) > 1 && strings.EqualFold(args[1], "get-master-addr-by-name") {
			time.Sleep(lookupDelay)
		}
		return respTestSentinel(t, master.Load().addr)(args)
	})
	bus := newRedisEventBus(config.RedisConfig{URL: "redis://" + sentinel.addr + "?master_name=mymaster"})
	bus.pingInterval, bus.silenceLimit = 20*time.Millisecond, silenceLimit
	t.Cleanup(func() { _ = bus.Close() })
	return bus, sentinel
}

// Runs without Redis. The first master accepts the subscription and then
// answers nothing, as a server does that froze or dropped off the network
// without closing its connections. Sentinel then names a second master. The
// bus has to notice the silence, ask Sentinel again and subscribe there.
func TestEventBusLeavesAMasterThatStoppedAnswering(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	silent := startRESPTestServer(t, nil, respTestSilent)
	want := Event{Type: EventSettingsChanged, Payload: "from the new master"}
	promoted := startRESPTestServer(t, nil, respTestPublishing(t, want))
	var master atomic.Pointer[respTestServer]
	master.Store(silent)
	bus, _ := sentinelTestBus(t, &master, time.Second, 0)
	events := make(chan Event, 64)
	// Callers subscribe with contexts that end long before the bus is closed,
	// and the subscription has to reconnect after that too.
	callerCtx, callerDone := context.WithCancel(ctx)
	err := bus.Subscribe(callerCtx, ChannelAdmin, func(event Event) { events <- event })
	callerDone()
	if err != nil {
		t.Fatalf("subscribe through Sentinel: %v", err)
	}
	if got := silent.received(); !slices.Contains(got, "SUBSCRIBE "+ChannelAdmin) {
		t.Fatalf("the first master received %q, want the subscription", got)
	}

	master.Store(promoted)
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("received %+v, want the event the new master published", got)
		}
	case <-ctx.Done():
		t.Fatalf("the subscription stayed on the silent master; the new master received %q", promoted.received())
	}
}

// The silence limit is for a server that stopped answering. A new connection
// that takes longer than the limit to build, here because Sentinel is slow to
// name the master, must still be built and used.
func TestEventBusReconnectsThroughASentinelSlowerThanTheSilenceLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	silent := startRESPTestServer(t, nil, respTestSilent)
	want := Event{Type: EventSettingsChanged, Payload: "from the new master"}
	promoted := startRESPTestServer(t, nil, respTestPublishing(t, want))
	var master atomic.Pointer[respTestServer]
	master.Store(silent)
	bus, _ := sentinelTestBus(t, &master, 200*time.Millisecond, 400*time.Millisecond)
	events := make(chan Event, 64)
	if err := bus.Subscribe(ctx, ChannelAdmin, func(event Event) { events <- event }); err != nil {
		t.Fatalf("subscribe through Sentinel: %v", err)
	}

	master.Store(promoted)
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("received %+v, want the event the new master published", got)
		}
	case <-ctx.Done():
		t.Fatalf("no event arrived from the new master, which received %q", promoted.received())
	}
}

// Runs without Redis. A subscription with nothing to deliver must stay on a
// master that answers its pings.
func TestEventBusKeepsAnIdleSubscriptionOnAnAnsweringMaster(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	answering := startRESPTestServer(t, nil, respTestAnswering)
	var master atomic.Pointer[respTestServer]
	master.Store(answering)
	bus, _ := sentinelTestBus(t, &master, time.Second, 0)
	if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// Pings are at least one interval apart, so this many take more than
	// twice the silence limit.
	idlePings := int(2*bus.silenceLimit/bus.pingInterval) + 1
	waitFor(t, ctx, "the idle subscription to be pinged", func() error {
		if got := answering.count("PING"); got < idlePings {
			return fmt.Errorf("%d of %d pings", got, idlePings)
		}
		return nil
	})
	if got := answering.count("SUBSCRIBE " + ChannelAdmin); got != 1 {
		t.Fatalf("the bus subscribed %d times, want the one subscription to survive an idle period", got)
	}
}

// Pings fail while no master can be reached. They have to go on afterwards:
// without them the next master would look silent, and an idle subscription
// would reconnect to it again and again.
func TestEventBusKeepsPingingAfterNoMasterCouldBeReached(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	first := startRESPTestServer(t, nil, respTestAnswering)
	second := startRESPTestServer(t, nil, respTestAnswering)
	var master atomic.Pointer[respTestServer]
	master.Store(first)
	bus, sentinel := sentinelTestBus(t, &master, time.Second, 0)
	if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	// Sentinel goes on naming the first master after it is gone, so every
	// ping and every redial fails.
	first.stop()
	failed := sentinel.count(respTestMasterLookup) + 20
	waitFor(t, ctx, "pings and redials to fail", func() error {
		if got := sentinel.count(respTestMasterLookup); got < failed {
			return fmt.Errorf("Sentinel was asked %d times, want %d", got, failed)
		}
		return nil
	})
	master.Store(second)

	const idlePings = 10
	waitFor(t, ctx, "the subscription to be pinged on the second master", func() error {
		if got := second.count("PING"); got < idlePings {
			return fmt.Errorf("%d of %d pings", got, idlePings)
		}
		return nil
	})
	if got := second.count("SUBSCRIBE " + ChannelAdmin); got != 1 {
		t.Fatalf("the bus subscribed to the second master %d times, want once", got)
	}
}

// While no master can be reached, a subscription pauses between redials
// instead of spinning.
func TestEventBusPausesBetweenRedials(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	first := startRESPTestServer(t, nil, respTestAnswering)
	var master atomic.Pointer[respTestServer]
	master.Store(first)
	bus, sentinel := sentinelTestBus(t, &master, time.Second, 0)
	// No pings, so that only redials ask Sentinel for the master.
	bus.pingInterval = time.Hour
	if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err != nil {
		t.Fatal(err)
	}

	first.stop()
	// reach waits until Sentinel has been asked n times. It polls much
	// faster than the pause it measures.
	reach := func(n int) {
		for sentinel.count(respTestMasterLookup) < n {
			select {
			case <-ctx.Done():
				t.Fatalf("Sentinel was asked %d times, want %d", sentinel.count(respTestMasterLookup), n)
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	redialling := sentinel.count(respTestMasterLookup) + 2
	reach(redialling)
	start := time.Now()
	reach(redialling + 5)
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond {
		t.Fatalf("five redials took %s, want a pause between them", elapsed)
	}
}

// Callers subscribe with contexts that end before the bus is closed. The
// subscription has always outlived them.
func TestEventBusSubscriptionOutlivesTheCallersContext(t *testing.T) {
	want := Event{Type: EventSettingsChanged, Payload: "after the caller left"}
	var publish atomic.Bool
	answering := startRESPTestServer(t, nil, func(args []string) (string, bool) {
		if args[0] != "PING" {
			return "", false
		}
		if publish.Load() {
			return respTestPong + respTestMessage(t, ChannelAdmin, want), true
		}
		return respTestPong, true
	})
	var master atomic.Pointer[respTestServer]
	master.Store(answering)
	// The silence limit is longer than the wait below, so a redial cannot
	// bring the event. It has to come as the answer to a ping.
	bus, _ := sentinelTestBus(t, &master, time.Minute, 0)
	events := make(chan Event, 64)
	callerCtx, callerDone := context.WithCancel(t.Context())
	if err := bus.Subscribe(callerCtx, ChannelAdmin, func(event Event) { events <- event }); err != nil {
		t.Fatal(err)
	}

	callerDone()
	publish.Store(true)
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("received %+v, want the event published after the caller's context ended", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no event arrived after the caller's context ended")
	}
}

// Close marks the bus closed and then takes its list of subscriptions. A
// subscription confirmed after that is on no list, so Subscribe has to end it
// itself.
func TestEventBusRefusesASubscriptionOnceCloseHasBegun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	server := startRESPTestServer(t, nil, respTestAnswering)
	var master atomic.Pointer[respTestServer]
	master.Store(server)
	behindSentinel, _ := sentinelTestBus(t, &master, 200*time.Millisecond, 0)
	single := newRedisEventBus(config.RedisConfig{URL: "redis://" + server.addr})
	for name, bus := range map[string]*RedisEventBus{"Sentinel": behindSentinel, "single server": single} {
		// What Close does first. The client stays open, as it is while Close
		// is still closing the subscriptions it knows.
		bus.once.Do(func() { close(bus.done) })
		t.Cleanup(func() { _ = bus.client.Close() })

		ended := server.endedConnections()
		if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err == nil {
			t.Fatalf("%s: Subscribe succeeded on a bus that Close had begun to close", name)
		}
		waitFor(t, ctx, name+": the refused subscription's connection to close", func() error {
			if server.endedConnections() == ended {
				return errors.New("it is still open")
			}
			return nil
		})
	}
	requireNoSubscriptionGoroutines(t, ctx, "after the refused subscriptions")
}

// requireNoSubscriptionGoroutines waits until no goroutine of a Sentinel
// subscription is left.
func requireNoSubscriptionGoroutines(t *testing.T, ctx context.Context, when string) {
	t.Helper()
	waitFor(t, ctx, "the subscription goroutines to end "+when, func() error {
		stacks := make([]byte, 4<<20)
		stacks = stacks[:runtime.Stack(stacks, true)]
		for _, running := range []string{"RedisEventBus).keepAlive", "RedisEventBus).listenToMaster"} {
			if strings.Contains(string(stacks), running) {
				return fmt.Errorf("%s is still running", running)
			}
		}
		return nil
	})
}

// Close has to end both goroutines of a Sentinel subscription, whether the
// master answers or the subscription is reconnecting to one that went silent.
func TestEventBusCloseEndsASentinelSubscription(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	for name, reply := range map[string]func([]string) (string, bool){"answering": respTestAnswering, "silent": respTestSilent} {
		server := startRESPTestServer(t, nil, reply)
		var master atomic.Pointer[respTestServer]
		master.Store(server)
		bus, _ := sentinelTestBus(t, &master, 200*time.Millisecond, 0)
		if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err != nil {
			t.Fatal(err)
		}
		if name == "silent" {
			// go-redis closes a connection whose receive deadline passed.
			waitFor(t, ctx, "the subscription to leave the silent master", func() error {
				if server.endedConnections() == 0 {
					return errors.New("its first connection is still open")
				}
				return nil
			})
		}
		if err := bus.Close(); err != nil {
			t.Fatalf("close with the master %s: %v", name, err)
		}
		requireNoSubscriptionGoroutines(t, ctx, "with the master "+name)
	}
}

// With Sentinel gone, every subscription redials and holds its lock while it
// does. Close must not wait for those redials one after another.
func TestEventBusCloseDoesNotWaitForRedials(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	silent := startRESPTestServer(t, nil, respTestSilent)
	var master atomic.Pointer[respTestServer]
	master.Store(silent)
	bus, sentinel := sentinelTestBus(t, &master, 200*time.Millisecond, 0)
	const subscriptions = 4
	for range subscriptions {
		if err := bus.Subscribe(ctx, ChannelAdmin, func(Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	sentinel.stop()
	// go-redis closes a connection whose receive deadline passed, so every
	// subscription is redialling once the silent master has lost them all.
	waitFor(t, ctx, "every subscription to leave the silent master", func() error {
		if got := silent.endedConnections(); got < subscriptions {
			return fmt.Errorf("%d of %d connections have closed", got, subscriptions)
		}
		return nil
	})

	closed := make(chan error, 1)
	go func() { closed <- bus.Close() }()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("Close was still waiting for redials after 10s")
	}
	requireNoSubscriptionGoroutines(t, ctx, "with Sentinel gone")
}

// respTestCertificate returns a self-signed server certificate that is valid
// for one DNS name.
func respTestCertificate(t *testing.T, dnsName string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		DNSNames:              []string{dnsName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, certificate
}

// go-redis verifies every server behind a rediss:// Sentinel URL against the
// host name in the URL. Each master here is reached by IP address. The first
// presents a certificate for the URL host and the second a trusted
// certificate for another name.
func TestSentinelURLWithTLSVerifiesTheMasterAgainstTheURLHost(t *testing.T) {
	forURLHost, urlHostCertificate := respTestCertificate(t, "localhost")
	forOtherName, otherCertificate := respTestCertificate(t, "redis.example")
	roots := x509.NewCertPool()
	roots.AddCert(urlHostCertificate)
	roots.AddCert(otherCertificate)
	serverTLS := func(certificate tls.Certificate) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	}
	// set writes through a Sentinel that names master and presents a
	// certificate for the URL host.
	set := func(master *respTestServer, value string) error {
		sentinel := startRESPTestServer(t, serverTLS(forURLHost), respTestSentinel(t, master.addr))
		_, sentinelPort, err := net.SplitHostPort(sentinel.addr)
		if err != nil {
			t.Fatal(err)
		}
		_, failover, err := config.ParseRedisURL("rediss://localhost:" + sentinelPort + "/4?master_name=mymaster&max_retries=-1")
		if err != nil {
			t.Fatal(err)
		}
		// A URL cannot name a certificate authority; a deployment uses the
		// system roots.
		failover.TLSConfig.RootCAs = roots
		client := redis.NewFailoverClient(failover)
		t.Cleanup(func() { _ = client.Close() })
		return client.Set(t.Context(), "key", value, 0).Err()
	}

	master := startRESPTestServer(t, serverTLS(forURLHost), respTestRedis)
	if err := set(master, "value"); err != nil {
		t.Fatalf("write through Sentinel over TLS: %v", err)
	}
	if got := master.received(); !slices.Contains(got, "SELECT 4") || !slices.Contains(got, "SET key value") {
		t.Fatalf("the master received %q, want the write on database 4", got)
	}

	otherMaster := startRESPTestServer(t, serverTLS(forOtherName), respTestRedis)
	if err := set(otherMaster, "value"); err == nil {
		t.Fatal("a write succeeded on a master whose certificate does not cover the URL host")
	}
	if got := otherMaster.received(); len(got) != 0 {
		t.Fatalf("the master with the other certificate received %q, want nothing", got)
	}
}

// Fails the Sentinel-managed master over and requires both a key-value client
// and the event bus to keep working against the new master. It needs
// SILO_TEST_REDIS_SENTINEL_URL to name a disposable Sentinel deployment with
// at least one replica, because it triggers a real failover.
func TestSentinelURLFollowsFailover(t *testing.T) {
	rawURL := os.Getenv("SILO_TEST_REDIS_SENTINEL_URL")
	if rawURL == "" {
		t.Skip("SILO_TEST_REDIS_SENTINEL_URL not set")
	}
	_, failover, err := config.ParseRedisURL(rawURL)
	if err != nil || failover == nil {
		t.Fatalf("SILO_TEST_REDIS_SENTINEL_URL is not a Sentinel URL: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	client, _, err := newRedisClient(config.RedisConfig{URL: rawURL}, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	bus := newRedisEventBus(config.RedisConfig{URL: rawURL})
	t.Cleanup(func() { _ = bus.Close() })

	key := fmt.Sprintf("silo:test:sentinel:%d", time.Now().UnixNano())
	channel := key
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	events := make(chan Event, 64)
	if err := bus.Subscribe(ctx, channel, func(event Event) { events <- event }); err != nil {
		t.Fatalf("subscribe through Sentinel: %v", err)
	}
	if err := client.Set(ctx, key, "before", time.Minute).Err(); err != nil {
		t.Fatalf("write through Sentinel: %v", err)
	}
	if err := bus.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "before"}); err != nil {
		t.Fatalf("publish through Sentinel: %v", err)
	}
	select {
	case got := <-events:
		if got.Payload != "before" {
			t.Fatalf("received %+v before the failover, want the published event", got)
		}
	case <-ctx.Done():
		t.Fatal("the event published before the failover did not arrive")
	}

	sentinel := redis.NewSentinelClient(&redis.Options{
		Addr:      failover.SentinelAddrs[0],
		Username:  failover.SentinelUsername,
		Password:  failover.SentinelPassword,
		TLSConfig: failover.TLSConfig,
	})
	t.Cleanup(func() { _ = sentinel.Close() })
	oldMaster, err := sentinel.GetMasterAddrByName(ctx, failover.MasterName).Result()
	if err != nil {
		t.Fatalf("ask Sentinel for the master: %v", err)
	}
	old := redis.NewClient(&redis.Options{
		Addr:      net.JoinHostPort(oldMaster[0], oldMaster[1]),
		Username:  failover.Username,
		Password:  failover.Password,
		TLSConfig: failover.TLSConfig,
	})
	t.Cleanup(func() { _ = old.Close() })
	// Sentinel refuses a failover until a replica is in sync, so retry.
	waitFor(t, ctx, "Sentinel to accept a failover", func() error {
		return sentinel.Failover(ctx, failover.MasterName).Err()
	})
	// Sentinel names the new master before it demotes the old one, and until
	// then the old one still accepts writes. Once it is a replica, a master
	// role seen through the client can only be the promoted server.
	waitFor(t, ctx, "the old master to become a replica", func() error {
		return requireRedisRole(ctx, old, "slave")
	})
	waitFor(t, ctx, "the client to reach the new master", func() error {
		return requireRedisRole(ctx, client, "master")
	})

	if err := client.Set(ctx, key, "after", time.Minute).Err(); err != nil {
		t.Fatalf("write after the failover: %v", err)
	}
	if got, err := client.Get(ctx, key).Result(); err != nil || got != "after" {
		t.Fatalf("read after the failover = (%q, %v), want the value written to the new master", got, err)
	}
	// Sentinel closes the old master's subscriptions when it demotes it. The
	// event bus has to subscribe again on the new master by itself.
	scoped := redisChannel(channel, failover.DB)
	waitFor(t, ctx, "the subscription to reach the new master", func() error {
		subscribers, err := client.PubSubNumSub(ctx, scoped).Result()
		if err != nil {
			return err
		}
		if subscribers[scoped] < 1 {
			return fmt.Errorf("the new master has no subscriber for the channel")
		}
		return nil
	})
	if err := bus.Publish(ctx, channel, Event{Type: EventSettingsChanged, Payload: "after"}); err != nil {
		t.Fatalf("publish after the failover: %v", err)
	}
	select {
	case got := <-events:
		if got.Payload != "after" {
			t.Fatalf("received %+v after the failover, want the event published to the new master", got)
		}
	case <-ctx.Done():
		t.Fatal("the event published after the failover did not arrive")
	}
}

// requireRedisRole reports an error unless the server answering client has
// the given replication role.
func requireRedisRole(ctx context.Context, client *redis.Client, want string) error {
	reply, err := client.Do(ctx, "ROLE").Slice()
	if err != nil {
		return err
	}
	if len(reply) == 0 || reply[0] != want {
		return fmt.Errorf("the server's role is not %s", want)
	}
	return nil
}

// waitFor retries check until it succeeds or ctx ends.
func waitFor(t *testing.T, ctx context.Context, what string, check func() error) {
	t.Helper()
	for {
		err := check()
		if err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s: %v", what, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
