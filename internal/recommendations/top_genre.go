package recommendations

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// topGenreTTL is how long a server reuses the top genre. It follows the
// catalog and the server's viewing, which change slowly, and reading it
// scans the whole catalog.
const topGenreTTL = 10 * time.Minute

// topGenreTimeout bounds a shared read of the top genre, which runs
// detached from the request that started it.
const topGenreTimeout = 30 * time.Second

// topGenreCache holds the server's top genre. Its zero value is ready to
// use. The genre is the same for every viewer, so one read serves them all:
// an expired genre is served while a background read refreshes it, and
// reads that find none wait on one shared read, or until their request
// ends.
type topGenreCache struct {
	mu      sync.Mutex
	genre   string
	loaded  bool
	expires time.Time
	loading singleflight.Group
}

func (c *topGenreCache) get(ctx context.Context, load func(context.Context) (string, error)) (string, error) {
	c.mu.Lock()
	genre, loaded, fresh := c.genre, c.loaded, time.Now().Before(c.expires)
	c.mu.Unlock()
	if fresh {
		return genre, nil
	}
	result := c.loading.DoChan("", func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), topGenreTimeout)
		defer cancel()
		genre, err := load(loadCtx)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.genre, c.loaded, c.expires = genre, true, time.Now().Add(topGenreTTL)
		c.mu.Unlock()
		return genre, nil
	})
	if loaded {
		return genre, nil
	}
	select {
	case res := <-result:
		genre, _ := res.Val.(string)
		return genre, res.Err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
