package pgstore

import (
	"context"
	"errors"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/jackc/pgx/v5"
)

// ListCollectionItemsPage checks the persisted membership witness and reads only limit+1
// ordered rows in one snapshot. Membership mutations never become offset drift.
func (s *PostgresUserStore) ListCollectionItemsPage(ctx context.Context, collectionID string, opts userstore.CollectionItemsPageOptions) (userstore.CollectionItemsPage, error) {
	page := userstore.CollectionItemsPage{Items: []userstore.CollectionItem{}}
	if err := opts.Validate(); err != nil {
		return page, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = tx.QueryRow(ctx, `SELECT v.revision FROM user_personal_collections p JOIN user_collection_revisions v ON v.collection_id = p.id AND v.user_id = p.user_id WHERE p.user_id = $1 AND p.id = $2`, s.userID, collectionID).Scan(&page.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return page, userstore.ErrCollectionNotFound
	}
	if err != nil {
		return page, err
	}
	if opts.Revision != 0 && opts.Revision != page.Revision {
		return page, userstore.ErrCollectionChanged
	}
	query := `SELECT collection_id, media_item_id, COALESCE(position, 0), added_at FROM user_personal_collection_items WHERE user_id = $1 AND collection_id = $2 AND sub_item_id = ''`
	args := []any{s.userID, collectionID}
	limitParam := "$3"
	if opts.After != nil {
		query += ` AND (COALESCE(position, 0), media_item_id) > ($3, $4)`
		args = append(args, opts.After.Position, opts.After.MediaItemID)
		limitParam = "$5"
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
		var addedAt time.Time
		if err := rows.Scan(&item.CollectionID, &item.MediaItemID, &item.Position, &addedAt); err != nil {
			return page, err
		}
		item.AddedAt = timeToString(addedAt)
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

func (s *PostgresUserStore) CollectionFeatures() userstore.CollectionFeatures {
	return userstore.CollectionFeatures{Groups: true, Imports: true, Artwork: true, ItemReorder: true}
}

// CollectionRevision is a durable fence for access and definition reads made
// outside the membership transaction (including cross-store viewer checks).
func (s *PostgresUserStore) CollectionRevision(ctx context.Context, collectionID string) (int64, error) {
	var revision int64
	err := s.pool.QueryRow(ctx, `SELECT v.revision FROM user_personal_collections p JOIN user_collection_revisions v ON v.collection_id = p.id AND v.user_id = p.user_id WHERE p.user_id = $1 AND p.id = $2`, s.userID, collectionID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, userstore.ErrCollectionNotFound
	}
	return revision, err
}
