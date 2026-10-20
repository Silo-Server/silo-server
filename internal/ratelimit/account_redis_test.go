package ratelimit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/auth"
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
		// Alternate instances; empty source identities isolate the account
		// counter from IP/key counters.
		var codes []int
		for i := range accountBudgetMultiplier + 1 {
			rec := httptest.NewRecorder()
			handlers[i%2].ServeHTTP(rec, accountRequest(account, auth.TokenTypeAccess, ""))
			codes = append(codes, rec.Code)
		}
		if time.Now().Unix()/60 != minute {
			t.Logf("attempt %d crossed minute boundary", attempt)
			continue
		}
		for i, code := range codes {
			want := 204
			if i == accountBudgetMultiplier {
				want = 429
			}
			if code != want {
				t.Fatalf("instance responses = %v; want %d admitted, then 429", codes, accountBudgetMultiplier)
			}
		}
		return
	}
	t.Fatal("all attempts crossed minute boundaries")
}
