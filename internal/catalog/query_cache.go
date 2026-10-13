package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// queryCache holds values derived from catalog queries in memory, keyed by a
// hash of the exact SQL and arguments that built them (queryCacheKey), so
// every access predicate (libraries, disabled libraries, maturity limits,
// personal sources) is part of the key and two viewers share a value only
// when their SQL is identical. Each node keeps its own cache. Entries expire
// after ttl; the cache evicts least recently used values once their summed
// weight exceeds budget. Concurrent misses for one key run one load, and at
// most cap(fills) loads run at once.
type queryCache[V any] struct {
	ttl         time.Duration
	budget      int
	loadTimeout time.Duration
	weigh       func(V) int
	now         func() time.Time
	flights     singleflight.Group
	// fills holds a slot for each value being built.
	fills chan struct{}

	mu      sync.Mutex
	entries map[string]*queryCacheItem[V]
	// lru is the sentinel of a ring of the entries: lru.next is the most
	// recently used, lru.prev the least.
	lru  queryCacheItem[V]
	used int
}

// queryCacheFlight carries a load's value through singleflight, whose results
// are untyped.
type queryCacheFlight[V any] struct{ value V }

type queryCacheItem[V any] struct {
	key        string
	value      V
	expires    time.Time
	weight     int
	prev, next *queryCacheItem[V]
}

func newQueryCache[V any](ttl time.Duration, budget, fillLimit int, loadTimeout time.Duration, weigh func(V) int) *queryCache[V] {
	c := &queryCache[V]{
		ttl:         ttl,
		budget:      budget,
		loadTimeout: loadTimeout,
		weigh:       weigh,
		now:         time.Now,
		fills:       make(chan struct{}, fillLimit),
		entries:     make(map[string]*queryCacheItem[V]),
	}
	c.lru.prev, c.lru.next = &c.lru, &c.lru
	return c
}

// get returns the cached value for key, or waits for the one load that
// builds it for every concurrent caller of the key. The load runs apart
// from the callers' contexts, bounded by loadTimeout, because the next
// request usually needs the same value: a typeahead keystroke or the next
// page of a jump. A caller whose context ends stops waiting; a failed load
// caches nothing and every waiter gets its error. A nil cache just loads.
func (c *queryCache[V]) get(ctx context.Context, key string, load func(context.Context) (V, error)) (V, error) {
	var zero V
	if c == nil {
		return load(ctx)
	}
	if v, ok := c.lookup(key); ok {
		return v, nil
	}
	flight := c.flights.DoChan(key, func() (any, error) {
		v, err := c.fill(context.WithoutCancel(ctx), key, load)
		return &queryCacheFlight[V]{value: v}, err
	})
	select {
	case res := <-flight:
		if res.Err != nil {
			return zero, res.Err
		}
		f, ok := res.Val.(*queryCacheFlight[V])
		if !ok {
			return zero, fmt.Errorf("query cache load returned %T", res.Val)
		}
		return f.value, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

// lookup returns key's value while it is fresh, dropping it once expired.
func (c *queryCache[V]) lookup(key string) (V, bool) {
	var zero V
	c.mu.Lock()
	defer c.mu.Unlock()
	item, ok := c.entries[key]
	if !ok {
		return zero, false
	}
	if !c.now().Before(item.expires) {
		c.removeLocked(item)
		return zero, false
	}
	c.unlinkLocked(item)
	c.pushFrontLocked(item)
	return item.value, true
}

// fill builds key's value in a fill slot and caches it when the load
// succeeds. A panicking load fails the flight instead of the process.
func (c *queryCache[V]) fill(ctx context.Context, key string, load func(context.Context) (V, error)) (v V, err error) {
	// A flight that starts just after another one stored the value finds it
	// here instead of building it again.
	if v, ok := c.lookup(key); ok {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.loadTimeout)
	defer cancel()
	select {
	case c.fills <- struct{}{}:
	case <-ctx.Done():
		return v, ctx.Err()
	}
	defer func() { <-c.fills }()
	defer func() {
		if r := recover(); r != nil {
			var zero V
			v, err = zero, fmt.Errorf("query cache load panicked: %v", r)
		}
	}()
	if v, err = load(ctx); err != nil {
		return v, err
	}
	c.mu.Lock()
	c.storeLocked(key, v)
	c.mu.Unlock()
	return v, nil
}

func (c *queryCache[V]) storeLocked(key string, v V) {
	weight := max(c.weigh(v), 1)
	if weight > c.budget {
		return
	}
	if old, ok := c.entries[key]; ok {
		c.removeLocked(old)
	}
	item := &queryCacheItem[V]{key: key, value: v, expires: c.now().Add(c.ttl), weight: weight}
	c.pushFrontLocked(item)
	c.entries[key] = item
	c.used += weight
	for c.used > c.budget {
		c.removeLocked(c.lru.prev)
	}
}

func (c *queryCache[V]) removeLocked(item *queryCacheItem[V]) {
	c.unlinkLocked(item)
	delete(c.entries, item.key)
	c.used -= item.weight
}

func (c *queryCache[V]) unlinkLocked(item *queryCacheItem[V]) {
	item.prev.next, item.next.prev = item.next, item.prev
}

func (c *queryCache[V]) pushFrontLocked(item *queryCacheItem[V]) {
	item.prev, item.next = &c.lru, c.lru.next
	c.lru.next.prev = item
	c.lru.next = item
}

// queryCacheKey hashes a query and its arguments. Arguments that do not
// encode to JSON make the query uncacheable rather than risk two scopes
// sharing a key.
func queryCacheKey(query string, args []any) (string, bool) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "", false
	}
	sum := sha256.New()
	sum.Write([]byte(query))
	sum.Write([]byte{0})
	sum.Write(encoded)
	return string(sum.Sum(nil)), true
}
