package autoscan

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Suppressor prevents re-enqueuing a scan for the same (folder, path) target
// within a window. The key must match the scanqueue dedup granularity
// (folder ID + path) so two distinct subtrees under one library folder are
// not collapsed into a single suppression claim.
type Suppressor interface {
	// ShouldScan atomically claims the target for scanning: returns true and sets
	// a TTL key if no claim exists, false if a recent claim is still live.
	ShouldScan(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// Release drops a suppression claim (used when the scan enqueue fails).
	Release(ctx context.Context, key string) error
}

type redisSuppressor struct{ client *redis.Client }

func NewRedisSuppressor(client *redis.Client) Suppressor { return &redisSuppressor{client: client} }

func (s *redisSuppressor) ShouldScan(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if s.client == nil || ttl <= 0 {
		return true, nil // no suppression configured -> always scan
	}
	redisKey := "autoscan:scanned:" + key
	ok, err := s.client.SetNX(ctx, redisKey, "1", ttl).Result()
	if err != nil {
		return true, nil // fail open: a Redis hiccup should not block scanning
	}
	return ok, nil
}

func (s *redisSuppressor) Release(ctx context.Context, key string) error {
	if s.client == nil {
		return nil
	}
	return s.client.Del(ctx, "autoscan:scanned:"+key).Err()
}
