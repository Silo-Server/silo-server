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
