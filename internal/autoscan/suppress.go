package autoscan

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Suppressor prevents re-enqueuing a scan for the same folder within a window.
type Suppressor interface {
	// ShouldScan atomically claims the folder for scanning: returns true and sets
	// a TTL key if no claim exists, false if a recent claim is still live.
	ShouldScan(ctx context.Context, folderID int, ttl time.Duration) (bool, error)
	// Release drops a suppression claim (used when the scan enqueue fails).
	Release(ctx context.Context, folderID int) error
}

type redisSuppressor struct{ client *redis.Client }

func NewRedisSuppressor(client *redis.Client) Suppressor { return &redisSuppressor{client: client} }

func (s *redisSuppressor) ShouldScan(ctx context.Context, folderID int, ttl time.Duration) (bool, error) {
	if s.client == nil || ttl <= 0 {
		return true, nil // no suppression configured -> always scan
	}
	key := fmt.Sprintf("autoscan:scanned:%d", folderID)
	ok, err := s.client.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return true, nil // fail open: a Redis hiccup should not block scanning
	}
	return ok, nil
}

func (s *redisSuppressor) Release(ctx context.Context, folderID int) error {
	if s.client == nil {
		return nil
	}
	return s.client.Del(ctx, fmt.Sprintf("autoscan:scanned:%d", folderID)).Err()
}
