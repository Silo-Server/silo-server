package userdb

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	defaultMaxOpen     = 500
	defaultIdleTimeout = 12 * time.Hour
	hotThreshold       = 5 * time.Minute
	criticalPercent    = 0.95
)

// PoolConfig controls how many SQLite connections the pool keeps open
// and when idle connections become eligible for eviction.
type PoolConfig struct {
	MaxOpen     int           // max open SQLite connections (default 500)
	IdleTimeout time.Duration // how long before db considered cold (default 12h)
	DataDir     string        // directory where SQLite files are stored
}

// UserDBPool manages per-user SQLite database connections with LRU eviction.
// Connections are cached and reused for the same userID. Active playback
// connections can be pinned so they are never evicted.
type UserDBPool struct {
	config PoolConfig
	mu     sync.Mutex
	dbs    map[int]*poolEntry // userID -> entry
	pinned map[int]bool       // userID -> true if pinned (active playback)
}

type poolEntry struct {
	db         *UserDB
	lastAccess time.Time
}

// evictionTier classifies a pool entry for eviction priority.
// Lower values are evicted first.
type evictionTier int

const (
	tierCold   evictionTier = iota // past idle_timeout — first to evict
	tierWarm                       // within idle_timeout — standard LRU when pool full
	tierHot                        // activity within last 5 min — only when pool >95% full
	tierPinned                     // active playback — never evict
)

// NewUserDBPool creates a new pool with the given configuration.
// Zero-value fields in config are replaced with defaults.
func NewUserDBPool(config PoolConfig) *UserDBPool {
	if config.MaxOpen <= 0 {
		config.MaxOpen = defaultMaxOpen
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaultIdleTimeout
	}
	return &UserDBPool{
		config: config,
		dbs:    make(map[int]*poolEntry),
		pinned: make(map[int]bool),
	}
}

// Get returns the cached UserDB for the given userID, or creates a new
// SQLite database at {DataDir}/{userID}.db, initialises its schema, and
// adds it to the pool. The context is checked for cancellation before
// potentially expensive I/O.
func (p *UserDBPool) Get(ctx context.Context, userID int) (*UserDB, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Return cached entry and refresh its access time.
	if entry, ok := p.dbs[userID]; ok {
		entry.lastAccess = time.Now()
		return entry.db, nil
	}

	// Evict if we are at capacity before opening a new connection.
	if len(p.dbs) >= p.config.MaxOpen {
		p.evict()
	}

	// If still at capacity after eviction (e.g. everything is pinned),
	// we still proceed — the caller should not be blocked.

	dbPath := filepath.Join(p.config.DataDir, fmt.Sprintf("%d.db", userID))
	udb, err := NewUserDB(dbPath, userID)
	if err != nil {
		return nil, fmt.Errorf("userdb pool: creating db for user %d: %w", userID, err)
	}

	poolOpen.Inc()
	p.dbs[userID] = &poolEntry{
		db:         udb,
		lastAccess: time.Now(),
	}
	return udb, nil
}

// Pin marks a userID as having active playback. Pinned connections are
// never evicted.
func (p *UserDBPool) Pin(userID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pinned[userID] = true
}

// Unpin removes the active-playback mark from a userID, making it
// eligible for normal eviction again.
func (p *UserDBPool) Unpin(userID int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pinned, userID)
}

// Close closes every open database in the pool and resets internal state.
func (p *UserDBPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var firstErr error
	for uid, entry := range p.dbs {
		if err := entry.db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("closing db for user %d: %w", uid, err)
		}
		delete(p.dbs, uid)
		poolOpen.Dec()
	}
	// Clear pinned set.
	for uid := range p.pinned {
		delete(p.pinned, uid)
	}
	return firstErr
}

// tierFor returns the eviction tier of a pool entry.
func (p *UserDBPool) tierFor(userID int, entry *poolEntry, now time.Time) evictionTier {
	if p.pinned[userID] {
		return tierPinned
	}
	age := now.Sub(entry.lastAccess)
	if age > p.config.IdleTimeout {
		return tierCold
	}
	if age <= hotThreshold {
		return tierHot
	}
	return tierWarm
}

// evictionCandidate pairs a userID with its tier and last-access time
// for sorting during eviction.
type evictionCandidate struct {
	userID     int
	tier       evictionTier
	lastAccess time.Time
}

// evict removes entries from the pool until we are below MaxOpen.
// Eviction order (first evicted to last):
//   - Cold  (past idle_timeout)        — always eligible
//   - Warm  (within idle_timeout)      — standard LRU when pool full
//   - Hot   (within last 5 min)        — only when pool >95% full
//   - Pinned (active playback)         — never evicted
//
// Within a tier, the least-recently-accessed entry is evicted first.
// Must be called with p.mu held.
func (p *UserDBPool) evict() {
	now := time.Now()
	target := len(p.dbs) - p.config.MaxOpen + 1 // free at least 1 slot
	if target <= 0 {
		return
	}

	isCritical := float64(len(p.dbs)) >= criticalPercent*float64(p.config.MaxOpen)

	candidates := make([]evictionCandidate, 0, len(p.dbs))
	for uid, entry := range p.dbs {
		tier := p.tierFor(uid, entry, now)
		if tier == tierPinned {
			continue
		}
		// Hot entries only considered when critical.
		if tier == tierHot && !isCritical {
			continue
		}
		candidates = append(candidates, evictionCandidate{
			userID:     uid,
			tier:       tier,
			lastAccess: entry.lastAccess,
		})
	}

	// Sort: lower tier first (cold before warm before hot), then oldest
	// lastAccess first within the same tier.
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].tier != candidates[j].tier {
			return candidates[i].tier < candidates[j].tier
		}
		return candidates[i].lastAccess.Before(candidates[j].lastAccess)
	})

	evicted := 0
	for _, c := range candidates {
		if evicted >= target {
			break
		}
		entry := p.dbs[c.userID]
		entry.db.Close()
		delete(p.dbs, c.userID)
		poolOpen.Dec()
		poolEvictions.Inc()
		evicted++
	}
}

// Len returns the number of currently open connections. Useful for tests
// and metrics.
func (p *UserDBPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.dbs)
}
