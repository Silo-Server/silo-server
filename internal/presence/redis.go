package presence

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRegistry is a cluster-aware presence registry. Add increments a per-user
// counter key with a TTL; Connected reports key > 0. On any Redis error,
// Connected returns false so callers fail open (push proceeds rather than being
// suppressed by an outage).
type RedisRegistry struct {
	client *redis.Client
	ttl    time.Duration
}

func NewRedisRegistry(client *redis.Client, ttl time.Duration) *RedisRegistry {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &RedisRegistry{client: client, ttl: ttl}
}

func presenceKey(userID int) string { return fmt.Sprintf("push:presence:%d", userID) }

func (r *RedisRegistry) Add(ctx context.Context, userID int) func() {
	key := presenceKey(userID)
	pipe := r.client.TxPipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, r.ttl)
	_, _ = pipe.Exec(ctx) // best-effort; presence is advisory

	var once sync.Once
	return func() {
		once.Do(func() {
			bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if n, err := r.client.Decr(bg, key).Result(); err == nil && n <= 0 {
				r.client.Del(bg, key)
			}
		})
	}
}

func (r *RedisRegistry) Connected(ctx context.Context, userID int) bool {
	n, err := r.client.Get(ctx, presenceKey(userID)).Int()
	if err != nil {
		return false // fail open: unknown presence → allow push
	}
	return n > 0
}

// Refresh extends the TTL for a user's presence key; call on WS heartbeat.
func (r *RedisRegistry) Refresh(ctx context.Context, userID int) {
	r.client.Expire(ctx, presenceKey(userID), r.ttl)
}

var _ Registry = (*RedisRegistry)(nil)
