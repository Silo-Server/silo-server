package recommendations

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// titleCountTTL is how long Highly Rated reuses one access scope's title
// counts. The counts only set how the media types interleave, and counting
// reads every title the scope admits, which a large catalog makes too slow
// to repeat on every Discover read.
const titleCountTTL = 10 * time.Minute

// titleCountCacheMax bounds the scopes held; a full cache starts over.
const titleCountCacheMax = 1024

// titleCountTimeout bounds a shared count, which runs detached from the
// request that started it so the others waiting on it are not canceled with
// that request.
const titleCountTimeout = 30 * time.Second

// titleCountRetry is how long a scope whose count failed reuses its
// fallback before counting again, so a count that keeps failing does not
// hold up every read.
const titleCountRetry = time.Minute

// titleCountCache holds, per access scope, the title counts countTitlesByType
// last read. Each server keeps its own.
type titleCountCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]titleCountEntry
	// counting runs one count per scope at a time; reads that miss while it
	// runs wait for its result rather than each counting the catalog.
	counting singleflight.Group
}

type titleCountEntry struct {
	counts  []int64
	expires time.Time
}

func newTitleCountCache() *titleCountCache {
	return &titleCountCache{entries: map[[sha256.Size]byte]titleCountEntry{}}
}

// titleCounter counts the titles of each recommendableMediaTypes entry
// filter admits, in that order.
type titleCounter func(ctx context.Context, filter catalog.AccessFilter) ([]int64, error)

// get returns filter's title counts by type. The counts only set how
// Highly Rated interleaves the media types, so a read never fails or waits
// on them for long:
//   - fresh counts are returned as they are;
//   - expired counts are returned at once while a count refreshes them in
//     the background;
//   - with no counts yet, the read waits for a count, or until its context
//     ends;
//   - a count that fails or is not waited for gives the scope's last counts,
//     or equal weights when it has none, and a failed count is retried
//     after titleCountRetry.
//
// A nil cache counts on every read.
func (c *titleCountCache) get(ctx context.Context, filter catalog.AccessFilter, count titleCounter) []int64 {
	if c == nil {
		counts, err := count(ctx, filter)
		if err != nil {
			slog.WarnContext(ctx, "counting titles for Highly Rated failed; weighing media types equally", "component", "recommendations", "error", err)
			return equalTitleCounts()
		}
		return counts
	}
	key := titleCountKey(filter)
	counts, fresh, ok := c.lookup(key)
	if fresh {
		return counts
	}
	result := c.counting.DoChan(string(key[:]), func() (any, error) {
		// A count that finished between the lookup above and this call
		// already stored its result.
		if counts, fresh, _ := c.lookup(key); fresh {
			return counts, nil
		}
		countCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), titleCountTimeout)
		defer cancel()
		counts, err := count(countCtx, filter)
		if err != nil {
			slog.WarnContext(countCtx, "counting titles for Highly Rated failed; reusing the last counts", "component", "recommendations", "error", err)
			c.store(key, c.fallback(key), titleCountRetry)
			return nil, err
		}
		c.store(key, counts, titleCountTTL)
		return counts, nil
	})
	if ok {
		// Expired counts serve this read; the count above refreshes them.
		return counts
	}
	select {
	case res := <-result:
		if counts, isCounts := res.Val.([]int64); res.Err == nil && isCounts {
			return counts
		}
	case <-ctx.Done():
	}
	return c.fallback(key)
}

// lookup returns key's counts, whether they are fresh, and whether there
// are any.
func (c *titleCountCache) lookup(key [sha256.Size]byte) (counts []int64, fresh, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry.counts, ok && time.Now().Before(entry.expires), ok
}

// fallback returns key's last counts, or equal weights when it has none.
func (c *titleCountCache) fallback(key [sha256.Size]byte) []int64 {
	if counts, _, ok := c.lookup(key); ok {
		return counts
	}
	return equalTitleCounts()
}

// store keeps counts for key until ttl passes.
func (c *titleCountCache) store(key [sha256.Size]byte, counts []int64, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok && len(c.entries) >= titleCountCacheMax {
		clear(c.entries)
	}
	c.entries[key] = titleCountEntry{counts: counts, expires: time.Now().Add(ttl)}
}

// equalTitleCounts weighs every recommendableMediaTypes entry alike.
func equalTitleCounts() []int64 {
	counts := make([]int64, len(recommendableMediaTypes))
	for i := range counts {
		counts[i] = 1
	}
	return counts
}

// titleCountKey identifies the titles filter admits: the fields
// appendAccessConditions reads. AllowedContentIDs can be long, so the key is
// a digest.
func titleCountKey(filter catalog.AccessFilter) [sha256.Size]byte {
	return sha256.Sum256(fmt.Appendf(nil, "%v|%v|%t|%v|%+v",
		filter.AllowedLibraryIDs, filter.AllowedContentIDs, filter.AllowedContentIDs != nil,
		filter.DisabledLibraryIDs, filter.MaturityLimits))
}
