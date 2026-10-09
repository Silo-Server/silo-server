package nodesessions

import (
	"context"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/redis/go-redis/v9"
)

func redisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("SILO_TEST_REDIS_URL")
	if url == "" {
		t.Skip("SILO_TEST_REDIS_URL required for the delivery record integration tests")
	}
	options, err := redis.ParseURL(url)
	if err != nil {
		t.Fatalf("parse SILO_TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(options)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("SILO_TEST_REDIS_URL does not answer: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// waitForDelivery polls until the API-side reader sees sessionID, since the
// node writes its record off the request path.
func waitForDelivery(t *testing.T, rdb *redis.Client, sessionID string) time.Time {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recent, err := RecentDeliveries(context.Background(), rdb, []string{sessionID})
		if err != nil {
			t.Fatalf("read deliveries: %v", err)
		}
		if at, ok := recent[sessionID]; ok {
			return at
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery for %s never became visible", sessionID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRecordDeliveryIsVisibleToTheAPIReader(t *testing.T) {
	rdb := redisTestClient(t)
	sessionID := "delivery-visible-" + t.Name()
	t.Cleanup(func() { rdb.Del(context.Background(), deliveryKeyPrefix+sessionID) })

	tracker := NewTracker(rdb, "http://proxy-a", "proxy-a", "proxy")
	before := time.Now()
	tracker.RecordDelivery(sessionID)

	at := waitForDelivery(t, rdb, sessionID)
	if at.Before(before.Add(-time.Second)) || at.After(time.Now().Add(time.Second)) {
		t.Fatalf("delivery time = %v, want close to %v", at, before)
	}

	recent, err := RecentDeliveries(context.Background(), rdb, []string{sessionID, "never-delivered-" + t.Name()})
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent deliveries = %v, want only the recorded session", recent)
	}
}

// The reader derives the delivery time from the record's remaining TTL, so an
// older record reports an older time without trusting the node's clock.
func TestRecentDeliveriesReportsRecordAge(t *testing.T) {
	rdb := redisTestClient(t)
	sessionID := "delivery-age-" + t.Name()
	ctx := context.Background()
	t.Cleanup(func() { rdb.Del(ctx, deliveryKeyPrefix+sessionID) })

	const age = 45 * time.Second
	if err := rdb.Set(ctx, deliveryKeyPrefix+sessionID, 1, DeliveryWindow-age).Err(); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	recent, err := RecentDeliveries(ctx, rdb, []string{sessionID})
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	got := time.Since(recent[sessionID])
	if got < age-2*time.Second || got > age+2*time.Second {
		t.Fatalf("record age = %v, want about %v", got, age)
	}
}

// One write per interval is enough to keep a record alive; HLS clients fetch a
// segment every few seconds and must not cost a Redis write each.
func TestRecordDeliveryThrottlesWritesPerSession(t *testing.T) {
	rdb := redisTestClient(t)
	sessionID := "delivery-throttle-" + t.Name()
	t.Cleanup(func() { rdb.Del(context.Background(), deliveryKeyPrefix+sessionID) })

	tracker := NewTracker(rdb, "http://proxy-a", "proxy-a", "proxy")
	tracker.RecordDelivery(sessionID)
	waitForDelivery(t, rdb, sessionID)

	tracker.mu.Lock()
	first := tracker.delivered[sessionID]
	tracker.mu.Unlock()

	tracker.RecordDelivery(sessionID)

	tracker.mu.Lock()
	second := tracker.delivered[sessionID]
	tracker.mu.Unlock()
	if !second.Equal(first) {
		t.Fatalf("second delivery inside the interval rewrote the record (%v -> %v)", first, second)
	}
}

func TestDeliveryWithoutRedisIsANoOp(t *testing.T) {
	tracker := NewTracker(nil, "http://proxy-a", "proxy-a", "proxy")
	tracker.RecordDelivery("session-1")
	var nilTracker *Tracker
	nilTracker.RecordDelivery("session-1")

	recent, err := RecentDeliveries(context.Background(), nil, []string{"session-1"})
	if err != nil || recent != nil {
		t.Fatalf("RecentDeliveries without Redis = %v, %v; want nil, nil", recent, err)
	}
}

func TestPruneDeliveredDropsExpiredThrottleEntries(t *testing.T) {
	tracker := NewTracker(nil, "http://proxy-a", "proxy-a", "proxy")
	now := time.Now()
	tracker.delivered["old"] = now.Add(-DeliveryWindow - time.Second)
	tracker.delivered["fresh"] = now
	tracker.mu.Lock()
	tracker.pruneDeliveredLocked(now)
	tracker.mu.Unlock()
	if _, ok := tracker.delivered["old"]; ok {
		t.Fatal("expired throttle entry was kept")
	}
	if _, ok := tracker.delivered["fresh"]; !ok {
		t.Fatal("fresh throttle entry was dropped")
	}
}

// Delivery writes must release their connections at the write deadline even
// when the node's Redis URL disables socket timeouts.
func TestRecordDeliveryReleasesAStalledRedisConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	closed := make(chan error, 1)
	wg.Go(func() {
		conn, err := ln.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer func() { _ = conn.Close() }()
		// Read every request without replying, as a stalled Redis would. EOF
		// means the client dropped the connection rather than abandoning it.
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	})
	rdb, err := cache.NewDeadlineRedisClientForRole(config.RedisConfig{
		URL: "redis://" + ln.Addr().String() + "?read_timeout=0&max_retries=-1&pool_size=1",
	}, "worker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.CloseRedisClient(rdb) })

	tracker := NewTracker(rdb, "http://proxy", "proxy", "proxy")
	tracker.RecordDelivery("session-1")
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("waiting for the delivery connection to close: %v", err)
		}
	case <-time.After(2 * deliveryWriteTimeout):
		t.Fatal("delivery write retained its connection past the write deadline")
	}
	if stats := rdb.PoolStats(); stats.TotalConns != 0 {
		t.Fatalf("delivery write retained %d pool connections", stats.TotalConns)
	}
}

// The API's idle sweep waits on this lookup, so with a client that honors
// context deadlines, a Redis that accepts the connection and never answers must
// not hold it past the caller's deadline, however long the read timeout is.
func TestRecentDeliveriesStopsAtTheDeadlineOfAStalledRedis(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	rdb := redis.NewClient(&redis.Options{Addr: ln.Addr().String(), ReadTimeout: time.Minute, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		_ = rdb.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	recent, err := RecentDeliveries(ctx, rdb, []string{"session-1"})
	if err == nil {
		t.Fatalf("lookup against a stalled Redis = %v, want an error", recent)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("lookup returned after %v, want about the 200ms deadline", elapsed)
	}
}
