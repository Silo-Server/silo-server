package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/access"
)

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
			return fmt.Errorf("curation item types differ")
		}
	}
	return nil
}
