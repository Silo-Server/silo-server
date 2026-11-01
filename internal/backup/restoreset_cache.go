package backup

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sync/singleflight"
)

// A preview reads the restore set's documents and index, never its blobs or
// archives, but validating it means reading and hashing the whole archive.
// Previews of one backup therefore share one validated copy, keyed by the
// archive's SHA-256 from the manifest: the digest names the bytes, so a
// changed archive is a different key. Comparisons with this server run on
// every request. Applying a restore never uses the cache; the job reads and
// verifies the whole archive again.
const (
	maxCachedRestoreSets     = 4
	maxCachedRestoreSetBytes = 256 << 20
)

type restoreSetCache struct {
	mu      sync.Mutex
	entries map[string]*RestoreSet
	order   []string // least recently used first
	bytes   int64
	group   singleflight.Group
}

// get returns the validated restore set with digest, reading it with load
// once however many previews ask for it at the same time. The set it
// returns is shared and must not be modified.
func (c *restoreSetCache) get(ctx context.Context, digest string, load func(context.Context) (*RestoreSet, error)) (*RestoreSet, error) {
	if rs := c.lookup(digest); rs != nil {
		return rs, nil
	}
	v, err, _ := c.group.Do(digest, func() (any, error) {
		if rs := c.lookup(digest); rs != nil {
			return rs, nil
		}
		// One request's cancellation must not fail the others waiting on
		// the same read.
		rs, err := load(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		c.store(digest, rs)
		return rs, nil
	})
	if err != nil {
		return nil, err
	}
	rs, ok := v.(*RestoreSet)
	if !ok {
		return nil, errors.New("restore set cache: unexpected value")
	}
	return rs, nil
}

func (c *restoreSetCache) lookup(digest string) *RestoreSet {
	c.mu.Lock()
	defer c.mu.Unlock()
	rs, ok := c.entries[digest]
	if !ok {
		return nil
	}
	c.touch(digest)
	return rs
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

// store keeps rs unless it alone exceeds the byte bound, evicting the
// least recently used sets beyond the count and byte bounds.
func (c *restoreSetCache) store(digest string, rs *RestoreSet) {
	if rs.metadataBytes > maxCachedRestoreSetBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*RestoreSet{}
	}
	if _, ok := c.entries[digest]; ok {
		c.touch(digest)
		return
	}
	c.entries[digest] = rs
	c.bytes += rs.metadataBytes
	c.touch(digest)
	for len(c.order) > maxCachedRestoreSets || c.bytes > maxCachedRestoreSetBytes {
		oldest := c.order[0]
		c.order = c.order[1:]
		c.bytes -= c.entries[oldest].metadataBytes
		delete(c.entries, oldest)
	}
}
