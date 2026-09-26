package catalog

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// LockLibraryCollectionReferences participates in a section writer's database
// transaction. Acquire these parents before section rows and scope counters.
// Missing outgoing references may be repaired or deleted; every nonempty
// incoming reference must exist. All callers use the same sorted union order.
//
// The real parent write is essential: a collection delete may already have a
// SERIALIZABLE snapshot from before this section transaction committed. A row
// lock alone would let that snapshot miss the newly committed reference. The
// write forces that delete to conflict, while the existing revision trigger
// invalidates its exact witness. Keep these locks through the section commit.
func LockLibraryCollectionReferences(ctx context.Context, tx pgx.Tx, previousIDs, incomingIDs []string) error {
	ids := append(slices.Clone(previousIDs), incomingIDs...)
	ids = slices.DeleteFunc(ids, func(id string) bool { return id == "" })
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id FROM library_collections WHERE id=ANY($1::text[]) ORDER BY id FOR NO KEY UPDATE`, ids)
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		found[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range incomingIDs {
		if id != "" && !found[id] {
			return ErrLibraryCollectionNotFound
		}
	}
	// Keep the display timestamp stable, but create a new MVCC row version and
	// advance the durable collection revision through its existing update trigger.
	for _, id := range ids {
		if !found[id] {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE library_collections SET updated_at=updated_at WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return nil
}
