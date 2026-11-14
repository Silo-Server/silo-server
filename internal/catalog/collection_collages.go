package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/collage"
	"github.com/Silo-Server/silo-server/internal/models"
)

// A collection without an uploaded, template or imported poster shows a
// collage of its titles' posters. Server collections
// (library_collection_posters.go) and personal collections
// (personal_collection_collages.go) share this machinery; only where their
// collages are stored and how their sources are chosen differ. The titles a
// collage shows depend on the viewer: each viewer sees the first titles it can
// access, so a restricted profile never sees the poster of a title it can't
// open (#1618).
//
// Each distinct set of source posters is one stored collage, shared by every
// viewer that selects the same posters. Its key is a hash of those posters, so
// a change to the titles, their order, or their artwork selects a new collage
// rather than serving a stale one. Reads never compose: a read serves the
// stored collage and builds a missing one in the background.
const (
	// CollectionCollageSourceLimit is the most member posters one collage shows.
	CollectionCollageSourceLimit = 4

	// collectionCollageLayout versions how a collage is composed from its
	// sources. Changing the composition must change this value, so collages
	// already stored are rebuilt instead of reused.
	collectionCollageLayout = "collage-v1"

	// A collage in use is touched at most this often, so reads rarely write.
	collectionCollageTouchInterval = 24 * time.Hour
	// A collage nobody has read for this long is retired the next time a
	// collage of the same collection is built.
	collectionCollageUnusedAfter = 7 * 24 * time.Hour

	collageBuildConcurrency = 2
	collageBuildMaxPending  = 256
	collageBuildTimeout     = 2 * time.Minute
	// After a failed build, reads don't retry the same collage for this long.
	collageBuildRetryAfter = 10 * time.Minute
)

// ErrCollectionCollageBeingCollected reports that the artwork revision
// collector is deleting a collage's objects, so it can't be rebuilt yet.
var ErrCollectionCollageBeingCollected = errors.New("collection collage is being collected")

// CollectionPoster is the poster one viewer sees for a collection.
type CollectionPoster struct {
	Path      string
	Thumbhash string
	// CollageKey names the viewer's collage. It is empty for an uploaded or
	// template poster, which every viewer shares.
	CollageKey string
}

// CollectionCollageRef names one stored collage of a collection.
type CollectionCollageRef struct {
	CollectionID string
	Key          string
}

// CollectionCollage is one stored collage.
type CollectionCollage struct {
	CollectionCollageRef
	Path       string
	Thumbhash  string
	LastUsedAt time.Time
}

// CollectionCollageKey returns the key of the collage composed from sources,
// the member poster paths in the order the collage shows them.
func CollectionCollageKey(sources []string) string {
	sum := sha256.New()
	sum.Write([]byte(collectionCollageLayout))
	for _, source := range sources {
		sum.Write([]byte{0})
		sum.Write([]byte(source))
	}
	return hex.EncodeToString(sum.Sum(nil)[:8])
}

// collageStore keeps one kind of collection's stored collages. Deleting a
// stored row hands its objects to the artwork revision collector in the same
// transaction (a table trigger), so no store deletes objects itself.
type collageStore interface {
	GetCollectionCollages(ctx context.Context, refs []CollectionCollageRef) (map[CollectionCollageRef]CollectionCollage, error)
	TouchCollectionCollages(ctx context.Context, refs []CollectionCollageRef) error
	// SaveCollectionCollage stores a built collage and releases its path's
	// reservation, or fails when the collection is gone.
	SaveCollectionCollage(ctx context.Context, c CollectionCollage) error
	RetireUnusedCollectionCollages(ctx context.Context, collectionID string, cutoff time.Time) (int, error)
	ReserveCollectionCollagePath(ctx context.Context, path string) (held bool, err error)
}

// collageSet serves, builds and retires the collages kept in one store.
type collageSet struct {
	store collageStore
	gen   CollageGenerator
	queue *collageBuildQueue
}

// serve returns the stored collage of each collection in ids whose sources
// are listed, keyed by collection ID. A collage not built yet is left out and
// built in the background, so a later read finds it.
func (c collageSet) serve(ctx context.Context, ids []string, sources map[string][]string) map[string]CollectionPoster {
	posters := make(map[string]CollectionPoster, len(sources))
	refs := make([]CollectionCollageRef, 0, len(sources))
	for _, id := range ids {
		if src := sources[id]; len(src) > 0 {
			refs = append(refs, CollectionCollageRef{CollectionID: id, Key: CollectionCollageKey(src)})
		}
	}
	if len(refs) == 0 {
		return posters
	}
	stored, err := c.store.GetCollectionCollages(ctx, refs)
	if err != nil {
		slog.WarnContext(ctx, "collage: failed to load collection collages", "component", "catalog", "error", err)
		return posters
	}

	var touch []CollectionCollageRef
	now := time.Now()
	for _, ref := range refs {
		stored, ok := stored[ref]
		if !ok || stored.Path == "" {
			c.buildLater(ref, sources[ref.CollectionID])
			continue
		}
		posters[ref.CollectionID] = CollectionPoster{Path: stored.Path, Thumbhash: stored.Thumbhash, CollageKey: ref.Key}
		if now.Sub(stored.LastUsedAt) > collectionCollageTouchInterval {
			touch = append(touch, ref)
		}
	}
	if err := c.store.TouchCollectionCollages(ctx, touch); err != nil {
		slog.DebugContext(ctx, "collage: failed to touch collection collages", "component", "catalog", "error", err)
	}
	return posters
}

// prepare builds the collage composed from sources unless it is already
// stored. It returns collage.ErrNotEnoughImages when sources is empty.
func (c collageSet) prepare(ctx context.Context, collectionID string, sources []string) error {
	if len(sources) == 0 {
		return collage.ErrNotEnoughImages
	}
	ref := CollectionCollageRef{CollectionID: collectionID, Key: CollectionCollageKey(sources)}
	stored, err := c.store.GetCollectionCollages(ctx, []CollectionCollageRef{ref})
	if err != nil {
		return err
	}
	if s, ok := stored[ref]; ok && s.Path != "" {
		return nil
	}
	return c.build(ctx, ref, sources)
}

// build composes and stores one collage, then retires the collection's
// collages that went unused. Nodes that build the same collage at once write
// the same objects, so the result doesn't depend on which one wins. The path
// stays reserved in the artwork revision collector until the row is saved, so
// a failed build leaves nothing behind.
func (c collageSet) build(ctx context.Context, ref CollectionCollageRef, sources []string) error {
	held, err := c.store.ReserveCollectionCollagePath(ctx, c.gen.CollectionCollagePath(ref.CollectionID, ref.Key))
	if err != nil {
		return err
	}
	if held {
		return ErrCollectionCollageBeingCollected
	}
	path, thumbhash, err := c.gen.ComposeCollectionCollage(ctx, ref.CollectionID, ref.Key, sources)
	if err != nil {
		return err
	}
	if err := c.store.SaveCollectionCollage(ctx, CollectionCollage{CollectionCollageRef: ref, Path: path, Thumbhash: thumbhash}); err != nil {
		return err
	}
	if _, err := c.store.RetireUnusedCollectionCollages(ctx, ref.CollectionID, time.Now().Add(-collectionCollageUnusedAfter)); err != nil {
		slog.WarnContext(ctx, "collage: failed to retire unused collages", "component", "catalog", "collection_id", ref.CollectionID, "error", err)
	}
	return nil
}

// buildLater builds a collage a read found missing, off the request.
func (c collageSet) buildLater(ref CollectionCollageRef, sources []string) {
	c.queue.runLater(ref, func(ctx context.Context) error { return c.build(ctx, ref, sources) })
}

// collageBuildQueue runs the collage builds reads ask for in the background.
// It skips a collage already being built, bounds how many builds wait and
// run, and holds off retrying a collage whose build just failed. A build that
// dies with its node is retried by the next read on any node.
type collageBuildQueue struct {
	mu sync.Mutex
	// pending maps a collage to the time a read may ask for it again: zero
	// while a build runs, the retry time after one failed.
	pending map[CollectionCollageRef]time.Time
	slots   chan struct{}
}

func newCollageBuildQueue() *collageBuildQueue {
	return &collageBuildQueue{
		pending: make(map[CollectionCollageRef]time.Time),
		slots:   make(chan struct{}, collageBuildConcurrency),
	}
}

// claim reports whether a build of ref may start now and, if so, marks it
// running.
func (q *collageBuildQueue) claim(ref CollectionCollageRef, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if retryAt, ok := q.pending[ref]; ok && (retryAt.IsZero() || now.Before(retryAt)) {
		return false
	}
	if len(q.pending) >= collageBuildMaxPending {
		for pendingRef, retryAt := range q.pending {
			if !retryAt.IsZero() && !now.Before(retryAt) {
				delete(q.pending, pendingRef)
			}
		}
		if len(q.pending) >= collageBuildMaxPending {
			return false
		}
	}
	q.pending[ref] = time.Time{}
	return true
}

// runLater runs work for ref off the request, unless work for ref is already
// running or recently failed.
func (q *collageBuildQueue) runLater(ref CollectionCollageRef, work func(ctx context.Context) error) {
	if !q.claim(ref, time.Now()) {
		return
	}
	go func() {
		failed := true
		defer func() {
			if r := recover(); r != nil {
				slog.Error("collage: build panic", "component", "catalog", "collection_id", ref.CollectionID, "panic", r, "stack", string(debug.Stack()))
			}
			q.finish(ref, failed, time.Now())
		}()
		q.slots <- struct{}{}
		defer func() { <-q.slots }()

		ctx, cancel := context.WithTimeout(context.Background(), collageBuildTimeout)
		defer cancel()
		err := work(ctx)
		switch {
		case err == nil:
			failed = false
		case errors.Is(err, collage.ErrNotEnoughImages):
			slog.DebugContext(ctx, "collage: no usable source images", "component", "catalog", "collection_id", ref.CollectionID)
		default:
			slog.WarnContext(ctx, "collage: background build failed", "component", "catalog", "collection_id", ref.CollectionID, "error", err)
		}
	}()
}

func (q *collageBuildQueue) finish(ref CollectionCollageRef, failed bool, now time.Time) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if failed {
		q.pending[ref] = now.Add(collageBuildRetryAfter)
		return
	}
	delete(q.pending, ref)
}

// reserveCollectionCollagePath queues a collage path in the artwork revision
// collector before a collage is built under it, pushing any earlier entry past
// the build. If the build fails or its node dies before the collage is saved,
// the collector deletes whatever it uploaded; a saved row releases the
// reservation. It reports held when a collector worker is processing the
// path, since it may be deleting objects the build would upload; the build
// must then wait for a later read.
func reserveCollectionCollagePath(ctx context.Context, pool *pgxpool.Pool, path string) (held bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning collection collage reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	err = tx.QueryRow(ctx, `
		SELECT locked_at IS NOT NULL
		FROM artwork_revision_gc_candidates
		WHERE original_path = $1
		FOR UPDATE
	`, path).Scan(&held)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("checking collection collage reservation: %w", err)
	case held:
		return true, nil
	}
	if _, err := tx.Exec(ctx, `SELECT public.queue_collection_poster_objects($1)`, path); err != nil {
		return false, fmt.Errorf("reserving collection collage path: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing collection collage reservation: %w", err)
	}
	return false, nil
}

// releaseCollectionCollageReservation drops the collector's queue entry for a
// collage path inside the transaction that saves its row: the row now
// protects the objects.
func releaseCollectionCollageReservation(ctx context.Context, tx pgx.Tx, path string) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM artwork_revision_gc_candidates
		WHERE original_path = $1 AND locked_at IS NULL
	`, path); err != nil {
		return fmt.Errorf("releasing collection collage reservation: %w", err)
	}
	return nil
}

func splitCollectionCollageRefs(refs []CollectionCollageRef) ([]string, []string) {
	ids := make([]string, len(refs))
	keys := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = ref.CollectionID
		keys[i] = ref.Key
	}
	return ids, keys
}

// A smart collection's collage comes from its query's first matches, which
// are too costly to read on every list read. A background refresh reads them
// and records which stored collage each viewer's access sees. A list read
// serves that record without running the query, and asks for a refresh when
// there is none for the collection's current definition or it is older than
// the refresh interval, so a new match reaches the collage within that time.
// Personal and server smart collections share this; only where the records
// live and how the matches are read differ.

// smartCollageStore keeps one kind of collection's stored collages and the
// record of which one each viewer access sees for a smart collection.
type smartCollageStore interface {
	collageStore
	// getSmartCollages returns the collages recorded for the viewer access
	// accessKey names, keyed by collection ID.
	getSmartCollages(ctx context.Context, collectionIDs []string, accessKey string) (map[string]smartCollage, error)
	// saveSmartCollage records variantKey, empty for none, as the collage the
	// viewer access accessKey names sees for a smart collection's definition.
	saveSmartCollage(ctx context.Context, collectionID, accessKey, definitionKey, variantKey string) error
}

// smartCollage is the collage recorded for one smart collection and viewer
// access. collage.Path is empty when the viewer can see no match with a
// poster.
type smartCollage struct {
	definitionKey string
	refreshedAt   time.Time
	collage       CollectionCollage
}

// smartCollageCollection is one smart collection as one viewer reads it.
type smartCollageCollection struct {
	id string
	// definitionKey names everything that decides the collection's matches
	// and their order.
	definitionKey string
	// sources reads the posters of the first matches the viewer can see, at
	// most CollectionCollageSourceLimit, in the collection's order.
	sources func(ctx context.Context) ([]string, error)
}

// smartCollageSet serves and refreshes smart collections' recorded collages.
type smartCollageSet struct {
	set             collageSet
	records         smartCollageStore
	refreshInterval time.Duration
}

// serve returns the recorded collage each collection shows the viewer whose
// access accessKey names, and asks for a background refresh of each one whose
// record is missing, made for another definition, or due.
func (s smartCollageSet) serve(ctx context.Context, collections []smartCollageCollection, accessKey string) map[string]CollectionPoster {
	posters := make(map[string]CollectionPoster, len(collections))
	if len(collections) == 0 {
		return posters
	}
	ids := make([]string, 0, len(collections))
	for _, c := range collections {
		ids = append(ids, c.id)
	}
	recorded, err := s.records.getSmartCollages(ctx, ids, accessKey)
	if err != nil {
		slog.WarnContext(ctx, "collage: failed to load smart collection collages", "component", "catalog", "error", err)
		return posters
	}
	var touch []CollectionCollageRef
	now := time.Now()
	for _, c := range collections {
		r, ok := recorded[c.id]
		if !ok || r.definitionKey != c.definitionKey {
			s.refreshLater(c, accessKey)
			continue
		}
		// A recorded collage whose stored row the artwork reconcile cleared
		// reads as missing and is rebuilt now, not when the record comes due.
		cleared := r.collage.Key != "" && r.collage.Path == ""
		if cleared || now.Sub(r.refreshedAt) >= s.refreshInterval {
			s.refreshLater(c, accessKey)
		}
		if r.collage.Path == "" {
			continue
		}
		posters[c.id] = CollectionPoster{Path: r.collage.Path, Thumbhash: r.collage.Thumbhash, CollageKey: r.collage.Key}
		if now.Sub(r.collage.LastUsedAt) > collectionCollageTouchInterval {
			touch = append(touch, r.collage.CollectionCollageRef)
		}
	}
	if err := s.records.TouchCollectionCollages(ctx, touch); err != nil {
		slog.DebugContext(ctx, "collage: failed to touch collection collages", "component", "catalog", "error", err)
	}
	return posters
}

// refreshLater runs refresh off the request. Refreshes of one collection for
// one viewer access and definition share a run.
func (s smartCollageSet) refreshLater(c smartCollageCollection, accessKey string) {
	ref := CollectionCollageRef{CollectionID: c.id, Key: "smart:" + accessKey + ":" + c.definitionKey}
	s.set.queue.runLater(ref, func(ctx context.Context) error { return s.refresh(ctx, c, accessKey) })
}

// refresh reads c's first matches, builds their collage unless it is stored,
// and records it as the collage the viewer whose access accessKey names sees.
// When no match has a poster it records that there is none and succeeds: the
// record holds off the next refresh until the refresh interval, so the build
// queue keeps no retry marker for it.
func (s smartCollageSet) refresh(ctx context.Context, c smartCollageCollection, accessKey string) error {
	sources, err := c.sources(ctx)
	if err != nil {
		return err
	}
	key := ""
	if len(sources) > 0 {
		if err := s.set.prepare(ctx, c.id, sources); err != nil {
			return err
		}
		key = CollectionCollageKey(sources)
	}
	return s.records.saveSmartCollage(ctx, c.id, accessKey, c.definitionKey, key)
}

// firstPosters returns the posters of the first items that have one, at most
// CollectionCollageSourceLimit, in order.
func firstPosters(items []*models.MediaItem) []string {
	var sources []string
	for _, item := range items {
		if item.PosterPath != "" {
			sources = append(sources, item.PosterPath)
			if len(sources) == CollectionCollageSourceLimit {
				break
			}
		}
	}
	return sources
}
