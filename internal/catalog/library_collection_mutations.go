package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const libraryCollectionTypeManual = "manual"

var (
	ErrLibraryCollectionNotManual        = errors.New("library collection is not manual")
	ErrLibraryCollectionItemNotFound     = errors.New("item not found in collection libraries")
	ErrLibraryCollectionRevisionMismatch = errors.New("library collection revision mismatch")
	ErrLibraryCollectionInUse            = errors.New("library collection is used by a page section")
)

// A collection witness covers its definition and items. A library witness covers
// all collection memberships, groups, and the synthetic ungrouped position in
// that library. Group editors use their owning library's witness.
type libraryCollectionMutation struct {
	pool         *pgxpool.Pool
	collectionID string
	libraryID    int
	groupID      string
}

type libraryRevisionReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (m libraryCollectionMutation) revision(ctx context.Context, q libraryRevisionReader) (int64, error) {
	var revision int64
	if m.collectionID != "" {
		err := q.QueryRow(ctx, `SELECT v.revision FROM library_collections c JOIN library_collection_revisions v ON v.collection_id=c.id WHERE c.id=$1`, m.collectionID).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrLibraryCollectionNotFound
		}
		return revision, err
	}
	libraryID := m.libraryID
	if m.groupID != "" {
		err := q.QueryRow(ctx, `SELECT library_id FROM library_collection_groups WHERE id=$1`, m.groupID).Scan(&libraryID)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrLibraryCollectionGroupNotFound
		}
		if err != nil {
			return 0, err
		}
	}
	err := q.QueryRow(ctx, `SELECT COALESCE(v.revision,1) FROM media_folders f LEFT JOIN library_collection_order_revisions v ON v.library_id=f.id WHERE f.id=$1`, libraryID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLibraryCollectionNotFound
	}
	return revision, err
}

func (r *LibraryCollectionRepository) CollectionOrderRevision(ctx context.Context, libraryID int) (int64, error) {
	return (libraryCollectionMutation{pool: r.pool, libraryID: libraryID}).revision(ctx, r.pool)
}
func (r *LibraryCollectionGroupRepository) CollectionOrderRevision(ctx context.Context, libraryID int) (int64, error) {
	return (libraryCollectionMutation{pool: r.pool, libraryID: libraryID}).revision(ctx, r.pool)
}

// run retries whole database-only transactions, preserving the original expected
// revision. A 40001 becomes a precondition mismatch only after a committed read
// proves that witness changed. Wildcards and unrelated SSI conflicts retry at
// most twice. Deadlocks and other database errors retain their original identity.
func (m libraryCollectionMutation) run(ctx context.Context, expected *int64, mutate func(pgx.Tx) error) error {
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := m.attempt(ctx, expected, mutate)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if expected == nil || !ok || pgErr.Code != "40001" {
			return err
		}
		if *expected != -1 {
			current, readErr := m.revision(ctx, m.pool)
			if readErr != nil {
				return readErr
			}
			if current != *expected {
				return fmt.Errorf("%w: %w", ErrLibraryCollectionRevisionMismatch, err)
			}
		}
		if attempt == 2 {
			return err
		}
	}
	panic("unreachable library collection mutation retry")
}

func (m libraryCollectionMutation) attempt(ctx context.Context, expected *int64, mutate func(pgx.Tx) error) error {
	options := pgx.TxOptions{}
	if expected != nil {
		options.IsoLevel = pgx.Serializable
	}
	tx, err := m.pool.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if expected != nil {
		current, err := m.revision(ctx, tx)
		if err != nil {
			return err
		}
		if *expected != -1 && current != *expected {
			return ErrLibraryCollectionRevisionMismatch
		}
	}
	// Resolve the group scope before a successful delete removes the group row.
	libraryID := m.libraryID
	if expected != nil && m.groupID != "" {
		if err := tx.QueryRow(ctx, `SELECT library_id FROM library_collection_groups WHERE id=$1`, m.groupID).Scan(&libraryID); err != nil {
			return err
		}
	}
	if err := mutate(tx); err != nil {
		return err
	}
	if expected != nil {
		// Consume no-ops only after target writes: never reserve a counter before
		// acquiring rows that legacy writers acquire before their revision triggers.
		if m.collectionID != "" {
			_, err = tx.Exec(ctx, `UPDATE library_collection_revisions SET revision=revision+1 WHERE collection_id=$1`, m.collectionID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO library_collection_order_revisions(library_id,revision) VALUES($1,2) ON CONFLICT(library_id) DO UPDATE SET revision=library_collection_order_revisions.revision+1`, libraryID)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteIfRevision checks references under the same parent lock used by section
// writers, then deletes and consumes the caller's witness in one transaction.
func (r *LibraryCollectionRepository) DeleteIfRevision(ctx context.Context, id string, expected int64) error {
	_, err := r.deleteCollection(ctx, id, &expected, false)
	return err
}

// DeleteSectionManagedIfUnreferenced skips collections whose management mode
// changed before the parent lock was acquired. Callers may clean up external
// artifacts only when deleted is true and err is nil.
func (r *LibraryCollectionRepository) DeleteSectionManagedIfUnreferenced(ctx context.Context, id string) (deleted bool, err error) {
	return r.deleteCollection(ctx, id, nil, true)
}

func (r *LibraryCollectionRepository) deleteCollection(ctx context.Context, id string, expected *int64, sectionManagedOnly bool) (bool, error) {
	deleted := false
	err := (libraryCollectionMutation{pool: r.pool, collectionID: id}).run(ctx, expected, func(tx pgx.Tx) error {
		deleted = false
		var mode string
		if err := tx.QueryRow(ctx, `SELECT management_mode FROM library_collections WHERE id=$1 FOR UPDATE`, id).Scan(&mode); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLibraryCollectionNotFound
			}
			return err
		}
		if sectionManagedOnly && mode != "section" {
			return nil
		}
		var inUse bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM page_sections WHERE config->>'library_collection_id'=$1)`, id).Scan(&inUse); err != nil {
			return err
		}
		if inUse {
			return ErrLibraryCollectionInUse
		}
		if _, err := tx.Exec(ctx, `DELETE FROM library_collections WHERE id=$1`, id); err != nil {
			return err
		}
		deleted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

// AddItemIfAbsent is the native add operation. Existing membership retains its
// position and timestamp; the legacy AddItem method keeps its upsert behavior.
func (r *LibraryCollectionRepository) AddItemIfAbsent(ctx context.Context, collectionID, mediaItemID string, position int) error {
	return (libraryCollectionMutation{pool: r.pool, collectionID: collectionID}).run(ctx, nil, func(tx pgx.Tx) error {
		legacyLibraryID, err := lockManualLibraryCollection(ctx, tx, collectionID)
		if err != nil {
			return err
		}
		// Resolve eligibility and insert in one statement. The parent lock protects
		// collection type and scopes from definition changes until this commits.
		// Admin edits deliberately include hidden libraries and collections.
		var eligible bool
		err = tx.QueryRow(ctx, `WITH eligible AS MATERIALIZED (
   SELECT $2::text AS media_item_id
   WHERE EXISTS (
    SELECT 1 FROM media_item_libraries mil JOIN media_items mi ON mi.content_id=mil.content_id
    WHERE mil.content_id=$2 AND (
     EXISTS(SELECT 1 FROM library_collection_libraries l WHERE l.collection_id=$1 AND l.library_id=mil.media_folder_id)
     OR (NOT EXISTS(SELECT 1 FROM library_collection_libraries l WHERE l.collection_id=$1) AND mil.media_folder_id=$4)
    )
   )
  ), inserted AS (
   INSERT INTO library_collection_items(collection_id,media_item_id,position,source_rank)
   SELECT $1,media_item_id,$3,0 FROM eligible
   ON CONFLICT(collection_id,media_item_id) DO NOTHING
   RETURNING 1
  ) SELECT EXISTS(SELECT 1 FROM eligible)`, collectionID, mediaItemID, position, legacyLibraryID).Scan(&eligible)
		if err != nil {
			return err
		}
		if !eligible {
			return ErrLibraryCollectionItemNotFound
		}
		return nil
	})
}

// Item and membership mutations acquire parent rows before their revision
// triggers can run. Definition updates already acquire parents first. This
// ordering prevents items->revision->parent and membership->revision inversions
// between these repository writers, including the legacy entry points.
func lockLibraryCollectionParent(ctx context.Context, tx pgx.Tx, id string) error {
	var found string
	err := tx.QueryRow(ctx, `SELECT id FROM library_collections WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLibraryCollectionNotFound
	}
	return err
}
func lockLibraryCollectionParents(ctx context.Context, tx pgx.Tx, libraryID int) error {
	// Moves compact the source and destination groups, so their affected parents
	// include more than the submitted IDs. Lock the library's parents in one
	// stable order, without materializing item IDs.
	rows, err := tx.Query(ctx, `SELECT c.id FROM library_collections c JOIN library_collection_libraries l ON l.collection_id=c.id WHERE l.library_id=$1 ORDER BY c.id FOR NO KEY UPDATE OF c`, libraryID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// Membership replacement can remove one library before adding another. Lock the
// entire old/new scope before the first statement; trigger-local sorting cannot
// prevent opposite swaps from retaining opposite first counters.
func lockLibraryCollectionMembershipOrders(ctx context.Context, tx pgx.Tx, collectionID string, requested []int) error {
	rows, err := tx.Query(ctx, `SELECT library_id FROM library_collection_libraries WHERE collection_id=$1`, collectionID)
	if err != nil {
		return err
	}
	affected := slices.Clone(requested)
	for rows.Next() {
		var libraryID int
		if err := rows.Scan(&libraryID); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, libraryID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return lockLibraryCollectionOrderRevisions(ctx, tx, affected)
}

// Call only after acquiring the transaction's collection parents. Sorting this
// lock set preserves submitted library order (including the legacy primary
// library), while establishing one transaction-wide order for aggregate locks.
func lockLibraryCollectionOrderRevisions(ctx context.Context, tx pgx.Tx, libraryIDs []int) error {
	ordered := slices.Clone(libraryIDs)
	slices.Sort(ordered)
	ordered = slices.Compact(ordered)
	if len(ordered) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO library_collection_order_revisions(library_id,revision) SELECT id,1 FROM unnest($1::bigint[]) ids(id) ORDER BY id ON CONFLICT(library_id) DO NOTHING`, ordered); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT library_id FROM library_collection_order_revisions WHERE library_id=ANY($1::bigint[]) ORDER BY library_id FOR UPDATE`, ordered)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

// RemoveManualItem is the native removal operation. The legacy RemoveItem
// entry point retains its caller-validated behavior.
func (r *LibraryCollectionRepository) RemoveManualItem(ctx context.Context, collectionID, mediaItemID string) error {
	return (libraryCollectionMutation{pool: r.pool, collectionID: collectionID}).run(ctx, nil, func(tx pgx.Tx) error {
		if _, err := lockManualLibraryCollection(ctx, tx, collectionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM library_collection_items WHERE collection_id=$1 AND media_item_id=$2`, collectionID, mediaItemID)
		return err
	})
}

func lockManualLibraryCollection(ctx context.Context, tx pgx.Tx, id string) (int, error) {
	var kind string
	var libraryID int
	err := tx.QueryRow(ctx, `SELECT collection_type,library_id FROM library_collections WHERE id=$1 FOR NO KEY UPDATE`, id).Scan(&kind, &libraryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLibraryCollectionNotFound
	}
	if err != nil {
		return 0, err
	}
	if kind != libraryCollectionTypeManual {
		return 0, ErrLibraryCollectionNotManual
	}
	return libraryID, nil
}
