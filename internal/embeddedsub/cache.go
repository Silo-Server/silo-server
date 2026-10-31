package embeddedsub

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/fspath"
)

// indexLoadTimeout bounds how long callers wait for a shared index read.
// A timed-out read stays registered until its filesystem work finishes.
var indexLoadTimeout = 30 * time.Second

const maxIndexReads = 8

var errIndexReadBusy = errors.New("embeddedsub: index read limit reached")

// indexReads bounds outstanding cached reads, including ones still blocked
// in the kernel after their callers have received the load deadline.
var indexReads = make(chan struct{}, maxIndexReads)

// Cache keeps recently read indexes in memory, keyed by path, size and
// modification time, so a file that changes gets a fresh index. It holds
// up to a byte budget, evicting the least recently used. Concurrent misses
// for one file share a single read. It is safe for concurrent use.
type Cache struct {
	maxBytes  int64
	readIndex func(context.Context, string) (*Index, error)

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
		maxBytes:  maxBytes,
		readIndex: ReadIndex,
		lru:       list.New(),
		entries:   map[cacheKey]*cacheEntry{},
		loading:   map[cacheKey]*cacheLoad{},
	}
}

// Index returns the file's index, reading it on a miss (ReadIndex). It
// stats the file on every call, within ctx: a stat on a stalled mount
// doesn't hold a caller past its context. Failed loads can be retried after
// their filesystem work finishes.
func (c *Cache) Index(ctx context.Context, path string) (*Index, error) {
	info, err := fspath.StatContext(ctx, path)
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
		select {
		case indexReads <- struct{}{}:
		default:
			c.mu.Unlock()
			return nil, errIndexReadBusy
		}
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
// and its result dropped; its entry and slot remain held until it finishes.
func (c *Cache) load(ctx context.Context, key cacheKey, load *cacheLoad) {
	release := func() {
		c.mu.Lock()
		delete(c.loading, key)
		<-indexReads
		c.mu.Unlock()
	}
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
		idx, err := c.readIndex(ctx, key.path)
		read <- result{idx: idx, err: err}
	}()
	var res result
	finished := false
	select {
	case res = <-read:
		finished = true
	case <-ctx.Done():
		res.err = fmt.Errorf("embeddedsub: index read: %w", ctx.Err())
	}
	c.mu.Lock()
	if res.err == nil {
		c.add(cacheKey{path: key.path, size: res.idx.Size, mtime: res.idx.ModTime.UnixNano()}, res.idx)
	}
	c.mu.Unlock()
	load.idx, load.err = res.idx, res.err
	if finished {
		release()
	}
	close(load.done)
	if !finished {
		<-read
		release()
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
