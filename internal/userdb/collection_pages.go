package userdb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ListCollectionItemsPage checks the persisted membership witness and reads only limit+1
// ordered rows in one snapshot. Membership mutations never become offset drift.
func (s *SQLiteUserStore) ListCollectionItemsPage(ctx context.Context, collectionID string, opts userstore.CollectionItemsPageOptions) (userstore.CollectionItemsPage, error) {
	page := userstore.CollectionItemsPage{Items: []userstore.CollectionItem{}}
	if err := opts.Validate(); err != nil {
		return page, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback() //nolint:errcheck
	err = tx.QueryRowContext(ctx, `SELECT v.revision FROM personal_collections p JOIN personal_collection_revisions v ON v.collection_id = p.id WHERE p.id = ?`, collectionID).Scan(&page.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return page, userstore.ErrCollectionNotFound
	}
	if err != nil {
		return page, err
	}
	if opts.Revision != 0 && opts.Revision != page.Revision {
		return page, userstore.ErrCollectionChanged
	}
	query := `SELECT collection_id, media_item_id, position, added_at FROM personal_collection_items WHERE collection_id = ?`
	args := []any{collectionID}
	limitParam := "?"
	if opts.After != nil {
		query += ` AND (position, media_item_id) > (?, ?)`
		args = append(args, opts.After.Position, opts.After.MediaItemID)
	}
	query += " ORDER BY position, media_item_id LIMIT " + limitParam
	args = append(args, opts.Limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var item userstore.CollectionItem
		if err := rows.Scan(&item.CollectionID, &item.MediaItemID, &item.Position, &item.AddedAt); err != nil {
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

func (s *SQLiteUserStore) CollectionFeatures() userstore.CollectionFeatures {
	return userstore.CollectionFeatures{}
}

// CollectionRevision is a durable fence for access and definition reads made
// outside the membership transaction (including cross-store viewer checks).
func (s *SQLiteUserStore) CollectionRevision(ctx context.Context, collectionID string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT v.revision FROM personal_collections p JOIN personal_collection_revisions v ON v.collection_id = p.id WHERE p.id = ?`, collectionID).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, userstore.ErrCollectionNotFound
	}
	return revision, err
}
