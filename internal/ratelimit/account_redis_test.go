package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/redis/go-redis/v9"
)

// Two independently constructed middleware/Redis clients model the shared counter
// boundary. This is not a deployed replica or failover test.
func TestAccountRedisSharingAcrossMiddlewareInstances(t *testing.T) {
	raw := os.Getenv("SILO_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("SILO_TEST_REDIS_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	clients := []*redis.Client{redis.NewClient(opts), redis.NewClient(opts)}
	for _, client := range clients {
		t.Cleanup(func() { _ = client.Close() })
		if err := client.Ping(t.Context()).Err(); err != nil {
			t.Fatal(err)
		}
	}
	var handlers []http.Handler
	for _, client := range clients {
		limiter := NewRedisLimiter(client)
		mw := NewMiddleware(limiter, limiter, nil, false)
		mw.cfg = DefaultConfig()
		mw.cfg.IPReqPerSecond, mw.cfg.IPReqPerMinute, mw.cfg.IPBurst = 1, 1, 1
		handlers = append(handlers, mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })))
	}
	for attempt := range 3 {
		account := int(time.Now().UnixNano())
		prefix := fmt.Sprintf("silo:ratelimit:account:%d:", account)
		t.Cleanup(func() {
			ctx := context.WithoutCancel(t.Context())
			keys, err := clients[0].Keys(ctx, prefix+"*").Result()
			if err == nil && len(keys) > 0 {
				_ = clients[0].Del(ctx, keys...).Err()
			}
		})
		minute := time.Now().Unix() / 60
		first, second := httptest.NewRecorder(), httptest.NewRecorder()
		// Empty source identities isolate the account counter from IP/key counters.
		handlers[0].ServeHTTP(first, accountRequest(account, auth.TokenTypeAccess, ""))
		handlers[1].ServeHTTP(second, accountRequest(account, auth.TokenTypeAccess, ""))
		if time.Now().Unix()/60 != minute {
			t.Logf("attempt %d crossed minute boundary", attempt)
			continue
		}
		if first.Code != 204 || second.Code != 429 {
			t.Fatalf("instance responses = %d, %d; want 204, 429", first.Code, second.Code)
		}
		return
	}
	t.Fatal("all attempts crossed minute boundaries")
}
