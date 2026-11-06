package backup

import (
	"context"
	"slices"
	"sync"
)

// A preview reads the restore set's documents and index, never its blobs or
// archives, but validating it means reading and hashing the whole archive.
// Previews of one backup therefore share one validated copy, keyed by the
// archive's SHA-256 from the manifest: the digest names the bytes, so a
// changed archive is a different key. Comparisons with this server run on
// every request. A restore job uses the cached set only to size the files
// it extracts; it reads and verifies the whole archive again.
//
// A cached set owns a directory with its account documents. Each get takes
// a reference its caller releases; a set leaving the cache removes its
// directory once the last reference is released, so an eviction never
// pulls a document from under a preview that is reading it.
const (
	maxCachedRestoreSets     = 4
	maxCachedRestoreSetBytes = 256 << 20
)

type restoreSetCache struct {
	mu      sync.Mutex
	entries map[string]*RestoreSet
	order   []string // least recently used first
	bytes   int64
	// reading holds the read in flight for each digest.
	reading map[string]*restoreSetRead
}

// restoreSetRead is one read in flight that previews wait on.
type restoreSetRead struct {
	done    chan struct{}
	rs      *RestoreSet
	err     error
	waiters int
	// finished is set, under the cache's lock, once each waiter left holds
	// a reference to rs.
	finished bool
	cancel   context.CancelFunc
}

// cacheRefs counts a set's holders; the cache's lock guards it.
type cacheRefs struct {
	refs int
	// evicted marks a set no longer cached: the last release removes its
	// directory.
	evicted bool
}

// get returns the validated restore set with digest, reading it with load
// once however many previews ask for it at the same time, and the release
// its caller runs when done with it. The set it returns is shared and must
// not be modified.
//
// One preview's cancellation does not fail the others waiting on the same
// read; the read itself stops once every preview waiting on it is gone.
func (c *restoreSetCache) get(ctx context.Context, digest string, load func(context.Context) (*RestoreSet, error)) (*RestoreSet, func(), error) {
	c.mu.Lock()
	if rs, ok := c.entries[digest]; ok {
		c.touch(digest)
		rs.cache.refs++
		c.mu.Unlock()
		return rs, c.releaser(rs), nil
	}
	if c.reading == nil {
		c.reading = map[string]*restoreSetRead{}
	}
	r, ok := c.reading[digest]
	if !ok {
		readCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		r = &restoreSetRead{done: make(chan struct{}), cancel: cancel}
		c.reading[digest] = r
		go c.read(readCtx, digest, r, load)
	}
	r.waiters++
	c.mu.Unlock()

	select {
	case <-r.done:
		if r.err != nil {
			return nil, nil, r.err
		}
		return r.rs, c.releaser(r.rs), nil
	case <-ctx.Done():
		c.mu.Lock()
		finished := r.finished
		if !finished {
			if r.waiters--; r.waiters == 0 {
				// A preview that asks now starts a read of its own.
				r.cancel()
				c.forgetLocked(digest, r)
			}
		}
		c.mu.Unlock()
		if finished && r.err == nil {
			// The read handed this preview a reference as it finished.
			c.releaser(r.rs)()
		}
		return nil, nil, ctx.Err()
	}
}

// releaser returns the release for one reference to rs, which runs once.
func (c *restoreSetCache) releaser(rs *RestoreSet) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			rs.cache.refs--
			remove := rs.cache.evicted && rs.cache.refs == 0
			c.mu.Unlock()
			if remove {
				rs.Cleanup()
			}
		})
	}
}

// forgetLocked drops r as digest's read in flight, if it still is. c.mu is
// held.
func (c *restoreSetCache) forgetLocked(digest string, r *restoreSetRead) {
	if c.reading[digest] == r {
		delete(c.reading, digest)
	}
}

// read runs load for the previews waiting on r and keeps what it read.
func (c *restoreSetCache) read(ctx context.Context, digest string, r *restoreSetRead, load func(context.Context) (*RestoreSet, error)) {
	defer r.cancel()
	rs, err := load(ctx)
	c.mu.Lock()
	c.forgetLocked(digest, r)
	remove := false
	if err == nil {
		if !c.storeLocked(digest, rs) {
			rs.cache.evicted = true
		}
		rs.cache.refs += r.waiters
		remove = rs.cache.evicted && rs.cache.refs == 0
	}
	r.rs, r.err, r.finished = rs, err, true
	c.mu.Unlock()
	if remove {
		rs.Cleanup()
	}
	close(r.done)
}

func (c *restoreSetCache) touch(digest string) {
	for i, d := range c.order {
		if d == digest {
			c.order = append(append(c.order[:i:i], c.order[i+1:]...), digest)
			return
		}
	}
	c.order = append(c.order, digest)
}

// store hands rs, which no one else holds, to the cache, removing its
// directory when the cache does not keep it.
func (c *restoreSetCache) store(digest string, rs *RestoreSet) {
	c.mu.Lock()
	stored := c.storeLocked(digest, rs)
	c.mu.Unlock()
	if !stored {
		rs.Cleanup()
	}
}

// storeLocked keeps rs unless it alone exceeds the byte bound or the digest
// is already cached, evicting the least recently used sets beyond the count
// and byte bounds, and reports whether it kept rs. c.mu is held.
func (c *restoreSetCache) storeLocked(digest string, rs *RestoreSet) bool {
	if rs.metadataBytes > maxCachedRestoreSetBytes {
		return false
	}
	if c.entries == nil {
		c.entries = map[string]*RestoreSet{}
	}
	if _, ok := c.entries[digest]; ok {
		c.touch(digest)
		return false
	}
	if rs.fromUnverified {
		// One set of a backup this server cannot verify at a time: anyone
		// who can write a destination could fill the cache otherwise.
		for d, other := range c.entries {
			if other.fromUnverified {
				c.evictLocked(d)
			}
		}
	}
	c.entries[digest] = rs
	c.bytes += rs.metadataBytes
	c.touch(digest)
	for len(c.order) > maxCachedRestoreSets || c.bytes > maxCachedRestoreSetBytes {
		c.evictLocked(c.order[0])
	}
	return true
}

// evictLocked drops the set with digest, removing its directory unless a
// caller still holds it (releaser). c.mu is held.
func (c *restoreSetCache) evictLocked(digest string) {
	rs := c.entries[digest]
	c.order = slices.DeleteFunc(c.order, func(d string) bool { return d == digest })
	c.bytes -= rs.metadataBytes
	delete(c.entries, digest)
	rs.cache.evicted = true
	if rs.cache.refs == 0 {
		rs.Cleanup()
	}
}
