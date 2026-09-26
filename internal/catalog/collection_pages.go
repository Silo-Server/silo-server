package catalog

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

// ListItemsPage checks the persisted membership witness and reads only limit+1
// ordered rows in one snapshot. Membership mutations never become offset drift.
func (r *LibraryCollectionRepository) ListItemsPage(ctx context.Context, collectionID string, opts userstore.CollectionItemsPageOptions) (userstore.CollectionItemsPage, error) {
	page := userstore.CollectionItemsPage{Items: []userstore.CollectionItem{}}
	if err := opts.Validate(); err != nil {
		return page, err
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = tx.QueryRow(ctx, `SELECT v.revision FROM library_collections p JOIN library_collection_revisions v ON v.collection_id = p.id WHERE p.id = $1`, collectionID).Scan(&page.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, ErrLibraryCollectionNotFound
	}
	if err != nil {
		return page, err
	}
	if opts.Revision != 0 && opts.Revision != page.Revision {
		return page, userstore.ErrCollectionChanged
	}
	query := `SELECT collection_id, media_item_id, COALESCE(position, 0) FROM library_collection_items WHERE collection_id = $1`
	args := []any{collectionID}
	limitParam := "$2"
	if opts.After != nil {
		query += ` AND (COALESCE(position, 0), media_item_id) > ($2, $3)`
		args = append(args, opts.After.Position, opts.After.MediaItemID)
		limitParam = "$4"
	}
	query += " ORDER BY COALESCE(position, 0), media_item_id LIMIT " + limitParam
	args = append(args, opts.Limit+1)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item userstore.CollectionItem
		if err := rows.Scan(&item.CollectionID, &item.MediaItemID, &item.Position); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	page.HasMore = len(page.Items) > opts.Limit
	if page.HasMore {
		page.Items = page.Items[:opts.Limit]
	}
	return page, nil
}

// CollectionRevision is a durable fence for access and definition reads made
// outside the membership transaction (including cross-store viewer checks).
func (r *LibraryCollectionRepository) CollectionRevision(ctx context.Context, collectionID string) (int64, error) {
	var revision int64
	err := r.pool.QueryRow(ctx, `SELECT v.revision FROM library_collections p JOIN library_collection_revisions v ON v.collection_id = p.id WHERE p.id = $1`, collectionID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrLibraryCollectionNotFound
	}
	return revision, err
}
