package embeddedsub

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// indexLoadTimeout bounds a shared index read. A read that hangs, as on a
// stalled network mount, then fails the callers waiting on it, and the next
// caller starts another, instead of every later caller waiting on it.
var indexLoadTimeout = 30 * time.Second

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
		// The read is shared and runs on its own, so any caller, the first
		// included, can give up on it without failing the others.
		go c.load(context.WithoutCancel(ctx), key, load)
	}
	select {
	case <-load.done:
		return load.idx, load.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// load reads the index for key and publishes it to the callers waiting on
// load, within indexLoadTimeout. A panic fails them rather than leaving them
// waiting. A read still blocked at the deadline is left to finish on its own
// and its result dropped.
func (c *Cache) load(ctx context.Context, key cacheKey, load *cacheLoad) {
	ctx, cancel := context.WithTimeout(ctx, indexLoadTimeout)
	defer cancel()
	type result struct {
		idx *Index
		err error
	}
	read := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				read <- result{err: fmt.Errorf("embeddedsub: index read panicked: %v", r)}
			}
		}()
		idx, err := ReadIndex(ctx, key.path)
		read <- result{idx: idx, err: err}
	}()
	var res result
	select {
	case res = <-read:
	case <-ctx.Done():
		res.err = fmt.Errorf("embeddedsub: index read: %w", ctx.Err())
	}
	c.mu.Lock()
	delete(c.loading, key)
	if res.err == nil {
		c.add(cacheKey{path: key.path, size: res.idx.Size, mtime: res.idx.ModTime.UnixNano()}, res.idx)
	}
	c.mu.Unlock()
	load.idx, load.err = res.idx, res.err
	close(load.done)
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
