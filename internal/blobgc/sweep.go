package blobgc

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// The sweep is the backstop for what the triggers never queued: objects of
// files deleted before the triggers existed, and objects a write left after
// its row was already gone. It lists each namespace, groups keys into
// prefixes, asks the namespace which are live, and queues the dead ones for
// the Collector. It deletes nothing itself.
const (
	// sweepPageSize is one storage listing call.
	sweepPageSize = 1000

	// sweepMinAge keeps the sweep away from a prefix whose newest object is
	// recent: something may still be writing it, and its row may not be
	// visible yet.
	sweepMinAge = 24 * time.Hour

	// sweepAnomalyShare fails the sweep closed. Every way the liveness check
	// can go wrong (a key format the namespace misparses, a query that stops
	// matching) looks the same from here: most prefixes suddenly dead.
	// Queuing on that signal would erase a namespace, so when more than this
	// share of the prefixes seen are dead and not already queued, the sweep
	// queues nothing.
	sweepAnomalyShare = 0.5

	// sweepAnomalyFloor exempts small namespaces from the share check.
	sweepAnomalyFloor = 20

	// sweepBatchSize bounds the prefixes one liveness or queue query names.
	sweepBatchSize = 5000
)

// sweepAdvisoryLock serializes the sweep across servers ("SILOBGCS"). Two
// sweeps at once would only list twice, since queuing is idempotent.
const sweepAdvisoryLock int64 = 0x53494C4F42474353

// Sweeper queues dead prefixes that nothing queued.
type Sweeper struct {
	pool       *pgxpool.Pool
	store      SweepStore
	namespaces []Namespace
	now        func() time.Time
}

// NewSweeper returns nil when there is no database or storage to sweep.
func NewSweeper(pool *pgxpool.Pool, store SweepStore, namespaces ...Namespace) *Sweeper {
	if pool == nil || store == nil {
		return nil
	}
	return &Sweeper{pool: pool, store: store, namespaces: namespaces, now: time.Now}
}

// SweepStats counts what one sweep saw and queued.
type SweepStats struct {
	Namespaces []NamespaceSweep `json:"namespaces"`
	// Skipped means another server held the sweep lock.
	Skipped bool `json:"skipped"`
}

// NamespaceSweep is one namespace's part of a sweep.
type NamespaceSweep struct {
	Root         string `json:"root"`
	Objects      int    `json:"objects"`
	Unrecognized int    `json:"unrecognized"`
	Prefixes     int    `json:"prefixes"`
	Live         int    `json:"live"`
	TooNew       int    `json:"too_new"`
	Scheduled    int    `json:"scheduled"`
	Queued       int    `json:"queued"`
	// Truncated means maxPages ended the listing early.
	Truncated bool `json:"truncated"`
	// StoppedOnAnomaly means too many prefixes looked dead to trust, and
	// nothing was queued.
	StoppedOnAnomaly bool `json:"stopped_on_anomaly"`
}

// Sweep lists every namespace, up to maxPages listing pages each, and queues
// the dead prefixes it finds.
func (s *Sweeper) Sweep(ctx context.Context, maxPages int) (SweepStats, error) {
	lock, acquired, err := pglock.TryAcquire(ctx, s.pool, sweepAdvisoryLock)
	if err != nil {
		return SweepStats{}, fmt.Errorf("take the sweep lock: %w", err)
	}
	if !acquired {
		return SweepStats{Skipped: true}, nil
	}
	defer func() { _ = lock.Release(context.WithoutCancel(ctx)) }()

	conn := lock.Conn()
	checkpoint, err := s.loadCheckpoint(ctx, conn)
	if err != nil {
		return SweepStats{}, err
	}
	var stats SweepStats
	for _, ns := range s.namespaces {
		result, next, err := s.sweepNamespace(ctx, conn, ns, maxPages, checkpoint.Namespaces[ns.Root])
		stats.Namespaces = append(stats.Namespaces, result)
		if err != nil {
			return stats, fmt.Errorf("sweep %s: %w", ns.Root, err)
		}
		if !result.StoppedOnAnomaly {
			checkpoint.Namespaces[ns.Root] = next
			if err := s.saveCheckpoint(ctx, conn, checkpoint); err != nil {
				return stats, err
			}
		}
	}
	return stats, nil
}

func (s *Sweeper) sweepNamespace(ctx context.Context, conn *pgxpool.Conn, ns Namespace, maxPages int, saved namespaceCheckpoint) (NamespaceSweep, namespaceCheckpoint, error) {
	stats := NamespaceSweep{Root: ns.Root}
	// The newest object of each prefix: the listing is in key order, so a
	// prefix's objects are adjacent, but a prefix may span pages.
	newest := map[string]time.Time{}
	cursor := saved.Cursor
	lastPrefix := saved.Prefix
	if saved.Prefix != "" {
		newest[saved.Prefix] = saved.Newest
	}
	maxPages = max(maxPages, 1)
	var checkpoint namespaceCheckpoint
	for page := 0; ; page++ {
		if page == maxPages {
			stats.Truncated = true
			checkpoint = namespaceCheckpoint{Cursor: cursor, Prefix: lastPrefix, Newest: newest[lastPrefix]}
			// The last prefix can continue on the next page with newer objects.
			// Carry its age forward before deciding whether it is safe to queue.
			delete(newest, lastPrefix)
			break
		}
		objects, next, err := s.store.List(ctx, ns.Root, cursor, sweepPageSize)
		if err != nil {
			return stats, namespaceCheckpoint{}, fmt.Errorf("list: %w", err)
		}
		for _, object := range objects {
			stats.Objects++
			prefix, ok := ns.Group(object.Key)
			if !ok {
				stats.Unrecognized++
				continue
			}
			lastPrefix = prefix
			if object.ModTime.After(newest[prefix]) || newest[prefix].IsZero() {
				newest[prefix] = object.ModTime
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	prefixes := slices.Sorted(maps.Keys(newest))
	stats.Prefixes = len(prefixes)
	if len(prefixes) == 0 {
		return stats, checkpoint, nil
	}
	live, scheduled := map[string]bool{}, map[string]bool{}
	for batch := range slices.Chunk(prefixes, sweepBatchSize) {
		batchLive, err := ns.Live(ctx, conn, batch)
		if err != nil {
			return stats, namespaceCheckpoint{}, fmt.Errorf("check liveness: %w", err)
		}
		maps.Copy(live, batchLive)
		batchQueued, err := s.queued(ctx, conn, batch)
		if err != nil {
			return stats, namespaceCheckpoint{}, err
		}
		maps.Copy(scheduled, batchQueued)
	}
	cutoff := s.now().Add(-sweepMinAge)
	var dead []string
	for _, prefix := range prefixes {
		switch {
		case live[prefix]:
			stats.Live++
		case scheduled[prefix]:
			stats.Scheduled++
		case newest[prefix].After(cutoff):
			stats.TooNew++
		default:
			dead = append(dead, prefix)
		}
	}
	if len(prefixes) >= sweepAnomalyFloor && float64(len(dead)) > sweepAnomalyShare*float64(len(prefixes)) {
		stats.StoppedOnAnomaly = true
		return stats, checkpoint, nil
	}
	if len(dead) == 0 {
		return stats, checkpoint, nil
	}
	tag, err := conn.Exec(ctx, `
		INSERT INTO public.blob_gc_queue (prefix, not_before)
		SELECT unnest($1::text[]), now()
		ON CONFLICT (prefix) DO NOTHING`, dead)
	if err != nil {
		return stats, namespaceCheckpoint{}, fmt.Errorf("queue dead prefixes: %w", err)
	}
	stats.Queued = int(tag.RowsAffected())
	return stats, checkpoint, nil
}

// queued reports which of prefixes are already in the queue.
func (s *Sweeper) queued(ctx context.Context, conn *pgxpool.Conn, prefixes []string) (map[string]bool, error) {
	rows, err := conn.Query(ctx, `SELECT prefix FROM public.blob_gc_queue WHERE prefix = ANY($1::text[])`, prefixes)
	if err != nil {
		return nil, fmt.Errorf("read the queue: %w", err)
	}
	defer rows.Close()
	queued := map[string]bool{}
	for rows.Next() {
		var prefix string
		if err := rows.Scan(&prefix); err != nil {
			return nil, err
		}
		queued[prefix] = true
	}
	return queued, rows.Err()
}
