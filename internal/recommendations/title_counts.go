package recommendations

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// titleCountTTL is how long Highly Rated reuses one access scope's title
// counts. The counts only set how the media types interleave, and counting
// reads every title the scope admits, which a large catalog makes too slow
// to repeat on every Discover read.
const titleCountTTL = 10 * time.Minute

// titleCountCacheMax bounds the scopes held; a full cache starts over.
const titleCountCacheMax = 1024

// titleCountCache holds, per access scope, the title counts countTitlesByType
// last read. Each server keeps its own.
type titleCountCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]titleCountEntry
}

type titleCountEntry struct {
	counts  []int64
	expires time.Time
}

func newTitleCountCache() *titleCountCache {
	return &titleCountCache{entries: map[[sha256.Size]byte]titleCountEntry{}}
}

// get returns filter's title counts by type, counting them with repo when
// the scope's counts are missing or expired. A nil cache always counts.
func (c *titleCountCache) get(ctx context.Context, repo *Repo, filter catalog.AccessFilter) ([]int64, error) {
	if c == nil {
		return repo.countTitlesByType(ctx, filter)
	}
	key := titleCountKey(filter)
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && time.Now().Before(entry.expires) {
		return entry.counts, nil
	}
	counts, err := repo.countTitlesByType(ctx, filter)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= titleCountCacheMax {
		clear(c.entries)
	}
	c.entries[key] = titleCountEntry{counts: counts, expires: time.Now().Add(titleCountTTL)}
	return counts, nil
}

// titleCountKey identifies the titles filter admits: the fields
// appendAccessConditions reads. AllowedContentIDs can be long, so the key is
// a digest.
func titleCountKey(filter catalog.AccessFilter) [sha256.Size]byte {
	return sha256.Sum256(fmt.Appendf(nil, "%v|%v|%t|%v|%+v",
		filter.AllowedLibraryIDs, filter.AllowedContentIDs, filter.AllowedContentIDs != nil,
		filter.DisabledLibraryIDs, filter.MaturityLimits))
}
