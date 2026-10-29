package embeddedsub

import (
	"container/list"
	"context"
	"sync"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// Cache keeps recently read indexes in memory, keyed by path, size and
// modification time, so a file that changes gets a fresh index. It holds
// up to a byte budget, evicting the least recently used. Concurrent misses
// for one file share a single read. It is safe for concurrent use.
type Cache struct {
	maxBytes int64

	mu      sync.Mutex
	used    int64
	lru     *list.List // of cacheKey, most recent first
	entries map[cacheKey]*cacheEntry
	loading map[cacheKey]*cacheLoad
}

type cacheKey struct {
	path  string
	size  int64
	mtime int64
}

type cacheEntry struct {
	idx  *Index
	size int64
	elem *list.Element
}

type cacheLoad struct {
	done chan struct{}
	idx  *Index
	err  error
}

// NewCache returns a cache that keeps indexes up to maxBytes in total, by
// estimate. A typical file's index is a few kilobytes to a few hundred.
func NewCache(maxBytes int64) *Cache {
	return &Cache{
		maxBytes: maxBytes,
		lru:      list.New(),
		entries:  map[cacheKey]*cacheEntry{},
		loading:  map[cacheKey]*cacheLoad{},
	}
}

// Index returns the file's index, reading it on a miss (ReadIndex). It
// stats the file on every call. Errors aren't cached.
func (c *Cache) Index(ctx context.Context, path string) (*Index, error) {
	info, err := fspath.Stat(path)
	if err != nil {
		return nil, err
	}
	key := cacheKey{path: path, size: info.Size(), mtime: info.ModTime().UnixNano()}

	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		c.mu.Unlock()
		return e.idx, nil
	}
	load, loading := c.loading[key]
	if !loading {
		load = &cacheLoad{done: make(chan struct{})}
		c.loading[key] = load
	}
	c.mu.Unlock()

	if !loading {
		// The read is shared, so one caller giving up doesn't fail the
		// others; it is a handful of small reads.
		load.idx, load.err = ReadIndex(context.WithoutCancel(ctx), path)
		c.mu.Lock()
		delete(c.loading, key)
		if load.err == nil {
			c.add(cacheKey{path: path, size: load.idx.Size, mtime: load.idx.ModTime.UnixNano()}, load.idx)
		}
		c.mu.Unlock()
		close(load.done)
	}
	select {
	case <-load.done:
		return load.idx, load.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// add stores idx under key and evicts down to the budget. c.mu is held.
func (c *Cache) add(key cacheKey, idx *Index) {
	if e, ok := c.entries[key]; ok {
		c.lru.MoveToFront(e.elem)
		return
	}
	size := idx.memSize()
	if size > c.maxBytes {
		return
	}
	c.entries[key] = &cacheEntry{idx: idx, size: size, elem: c.lru.PushFront(key)}
	c.used += size
	for c.used > c.maxBytes {
		oldest, _ := c.lru.Remove(c.lru.Back()).(cacheKey)
		c.used -= c.entries[oldest].size
		delete(c.entries, oldest)
	}
}
