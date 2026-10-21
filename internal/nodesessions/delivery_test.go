package nodesessions

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/cache"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/redis/go-redis/v9"
)

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
