package requests

import (
	"context"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// LogoLookupFunc resolves a TMDB entity ID to a logo path.
type LogoLookupFunc func(ctx context.Context, id int) (string, error)

type logoCacheEntry struct {
	path      string
	expiresAt time.Time
}

// logoCache caches TMDB company/network logo paths with a TTL. Concurrent
// misses for the same ID are deduplicated via singleflight so we do not
// hammer TMDB on first-load bursts.
type logoCache struct {
	lookup LogoLookupFunc
	ttl    time.Duration
	group  singleflight.Group

	mu      sync.RWMutex
	entries map[int]logoCacheEntry
}

func newLogoCache(lookup LogoLookupFunc, ttl time.Duration) *logoCache {
	return &logoCache{
		lookup:  lookup,
		ttl:     ttl,
		entries: map[int]logoCacheEntry{},
	}
}

// Get returns the cached logo path for id, fetching it via the lookup function
// on miss. Empty strings (TMDB returned no logo) are cached as misses; we
// still avoid refetching them within the TTL window. Errors are not cached.
func (c *logoCache) Get(ctx context.Context, id int) (string, error) {
	if cached, ok := c.read(id); ok {
		return cached, nil
	}

	value, err, _ := c.group.Do(keyFromID(id), func() (any, error) {
		if cached, ok := c.read(id); ok {
			return cached, nil
		}
		path, err := c.lookup(ctx, id)
		if err != nil {
			return "", err
		}
		c.write(id, path)
		return path, nil
	})
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (c *logoCache) read(id int) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[id]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.path, true
}

func (c *logoCache) write(id int, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[id] = logoCacheEntry{
		path:      path,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func keyFromID(id int) string {
	return "logo:" + strconv.Itoa(id)
}
