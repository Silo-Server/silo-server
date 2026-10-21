package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A collection list reads, for every listed collection, its visible member
// count and, without an uploaded or imported poster, the first members with a
// poster for its collage. Both reads scan a collection's members, so on every
// read their cost grows with the members of every listed collection. A
// personal collection's result is remembered per viewer access and
// membership revision (user_collection_revisions): a change to the members,
// their order or the collection's definition, or to the viewer's or owner's
// access, is read at once. Catalog changes that leave membership alone, such
// as a title removed from a library, a rating that crosses the viewer's
// ceiling or a poster that arrives after import, reach a remembered result
// within personalCollectionReadMemoTTL.
const (
	personalCollectionReadMemoTTL = 5 * time.Minute
	// personalCollectionReadMemoMaxEntries bounds the memory one memo holds.
	personalCollectionReadMemoMaxEntries = 50_000
)

type personalCollectionReadKey struct {
	userID       int
	collectionID string
	accessKey    string
}

type personalCollectionReadEntry[V any] struct {
	revision int64
	expires  time.Time
	value    V
}

// personalCollectionReadMemo remembers one kind of per-viewer read of
// personal collections. Its zero value is ready to use.
type personalCollectionReadMemo[V any] struct {
	mu      sync.Mutex
	entries map[personalCollectionReadKey]personalCollectionReadEntry[V]
}

// get returns the value remembered for key at revision, unless it expired.
func (m *personalCollectionReadMemo[V]) get(key personalCollectionReadKey, revision int64, now time.Time) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || e.revision != revision || !now.Before(e.expires) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// put remembers value for key at revision. Callers read revision before the
// value, so a value is never remembered for a revision newer than the
// membership it was read from.
func (m *personalCollectionReadMemo[V]) put(key personalCollectionReadKey, revision int64, value V, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[personalCollectionReadKey]personalCollectionReadEntry[V])
	}
	if _, ok := m.entries[key]; !ok && len(m.entries) >= personalCollectionReadMemoMaxEntries {
		m.makeRoomLocked(now)
	}
	m.entries[key] = personalCollectionReadEntry[V]{revision: revision, expires: now.Add(personalCollectionReadMemoTTL), value: value}
}

// makeRoomLocked drops expired entries and, if too few expired, arbitrary
// live entries (map order is random) down to nine tenths of the capacity
// rather than grow. Most entries stay, and the next full sweep is at least a
// tenth of the capacity away. The caller holds mu.
func (m *personalCollectionReadMemo[V]) makeRoomLocked(now time.Time) {
	for k, e := range m.entries {
		if !now.Before(e.expires) {
			delete(m.entries, k)
		}
	}
	for k := range m.entries {
		if len(m.entries) < personalCollectionReadMemoMaxEntries*9/10 {
			break
		}
		delete(m.entries, k)
	}
}

// personalCollectionRevisions returns the membership revision of each of
// account userID's collections in the Postgres user store. A collection
// without one is absent.
func personalCollectionRevisions(ctx context.Context, pool *pgxpool.Pool, userID int, collectionIDs []string) (map[string]int64, error) {
	revisions := make(map[string]int64, len(collectionIDs))
	rows, err := pool.Query(ctx, `
		SELECT collection_id, revision
		FROM user_collection_revisions
		WHERE user_id = $1 AND collection_id = ANY($2)
	`, userID, collectionIDs)
	if err != nil {
		return nil, fmt.Errorf("loading personal collection revisions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var revision int64
		if err := rows.Scan(&id, &revision); err != nil {
			return nil, fmt.Errorf("scanning personal collection revision: %w", err)
		}
		revisions[id] = revision
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating personal collection revisions: %w", err)
	}
	return revisions, nil
}

// PersonalCollectionMemberCounts counts the members of hand-picked and
// imported personal collections a viewer can see
// (ItemRepository.CountVisiblePersonalCollectionMembers) and remembers each
// count for the collection's membership revision and the viewer's access.
type PersonalCollectionMemberCounts struct {
	pool *pgxpool.Pool
	memo personalCollectionReadMemo[int]
}

// NewPersonalCollectionMemberCounts counts members in pool's user store.
func NewPersonalCollectionMemberCounts(pool *pgxpool.Pool) *PersonalCollectionMemberCounts {
	return &PersonalCollectionMemberCounts{pool: pool}
}

// Count returns, for each of account userID's collections, how many of its
// members the viewer described by access can see; a collection with none
// counts zero. Pass only collections without a smart definition or display
// filter, which CountPersonalCollections counts. When the revisions can't be
// read, every collection is counted afresh and nothing is remembered.
func (c *PersonalCollectionMemberCounts) Count(ctx context.Context, userID int, collectionIDs []string, access AccessFilter) (map[string]int, error) {
	counts := make(map[string]int, len(collectionIDs))
	if len(collectionIDs) == 0 {
		return counts, nil
	}
	revisions, err := personalCollectionRevisions(ctx, c.pool, userID, collectionIDs)
	if err != nil {
		slog.WarnContext(ctx, "collections: failed to load personal collection revisions", "component", "catalog", "error", err)
		return NewItemRepository(c.pool).CountVisiblePersonalCollectionMembers(ctx, userID, collectionIDs, access)
	}
	accessKey := collageAccessKey(access)
	now := time.Now()
	var misses []string
	for _, id := range collectionIDs {
		revision, ok := revisions[id]
		if !ok {
			misses = append(misses, id)
			continue
		}
		if n, ok := c.memo.get(personalCollectionReadKey{userID, id, accessKey}, revision, now); ok {
			counts[id] = n
			continue
		}
		misses = append(misses, id)
	}
	if len(misses) == 0 {
		return counts, nil
	}
	visible, err := NewItemRepository(c.pool).CountVisiblePersonalCollectionMembers(ctx, userID, misses, access)
	if err != nil {
		return nil, err
	}
	for _, id := range misses {
		counts[id] = visible[id]
		if revision, ok := revisions[id]; ok {
			c.memo.put(personalCollectionReadKey{userID, id, accessKey}, revision, visible[id], now)
		}
	}
	return counts, nil
}
