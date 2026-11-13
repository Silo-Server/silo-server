package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

// MetadataCurationTxOptions pins the fresh-snapshot isolation required by the
// curation guard, without changing automatic processing's transaction defaults.
func MetadataCurationTxOptions(ctx context.Context) pgx.TxOptions {
	if access.HasMetadataCurationAuthorizer(ctx) {
		return pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	}
	return pgx.TxOptions{}
}

// LockMetadataCurationItems locks existing parents in content-id order, then
// authorizes each item's full library set in a separate READ COMMITTED statement.
// FOR UPDATE fences FK membership inserts/rebinds while a fresh statement sees
// memberships committed by a writer we waited for. Repository membership writers
// do not update media_folder_id in place. Missing parents are absent from the
// result; callers must distinguish an allowed creation from a missing source.
func LockMetadataCurationItems(ctx context.Context, tx pgx.Tx, ids ...string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT content_id, type FROM media_items
		WHERE content_id = ANY($1::text[]) ORDER BY content_id FOR UPDATE`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock curation items: %w", err)
	}
	types := make(map[string]string, len(ids))
	for rows.Next() {
		var id, itemType string
		if err := rows.Scan(&id, &itemType); err != nil {
			rows.Close()
			return nil, err
		}
		types[id] = itemType
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Keep a stable authorization order too, including empty sets.
	rows, err = tx.Query(ctx, `SELECT mi.content_id, mil.media_folder_id
		FROM media_items mi LEFT JOIN media_item_libraries mil USING (content_id)
		WHERE mi.content_id = ANY($1::text[])
		ORDER BY mi.content_id, mil.media_folder_id`, ids)
	if err != nil {
		return nil, fmt.Errorf("read curation libraries: %w", err)
	}
	libraries := make(map[string][]int, len(types))
	var ordered []string
	for rows.Next() {
		var id string
		var library *int
		if err := rows.Scan(&id, &library); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := libraries[id]; !ok {
			ordered = append(ordered, id)
			libraries[id] = nil
		}
		if library != nil {
			libraries[id] = append(libraries[id], *library)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ordered {
		if err := access.AuthorizeMetadataCurationLibraries(ctx, libraries[id]); err != nil {
			return nil, err
		}
	}
	return types, nil
}

// LockMetadataCurationFiles locks the files an explicit curation is about to
// move or rename, before GuardMetadataCurationItems locks their parents. Split
// and scanner writers lock files before parent items; taking the parents first
// deadlocks with them. Automatic processing is unchanged.
func LockMetadataCurationFiles(ctx context.Context, tx pgx.Tx, ids ...string) error {
	if !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM media_files
		WHERE content_id = ANY($1::text[]) ORDER BY id FOR UPDATE`, ids); err != nil {
		return fmt.Errorf("lock curation files: %w", err)
	}
	return nil
}

// GuardMetadataCurationItems leaves automatic metadata processing unchanged.
// Explicit curation must find every expected existing item under these locks.
func GuardMetadataCurationItems(ctx context.Context, tx pgx.Tx, ids ...string) error {
	if !access.HasMetadataCurationAuthorizer(ctx) {
		return nil
	}
	types, err := LockMetadataCurationItems(ctx, tx, ids...)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := types[id]; !ok {
			return ErrItemNotFound
		}
		if types[id] != types[ids[0]] {
			return errors.New("curation item types differ")
		}
	}
	return nil
}
