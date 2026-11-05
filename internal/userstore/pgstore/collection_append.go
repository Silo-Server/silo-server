package pgstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const collectionTypeManual = "manual"

// AppendCollectionItem takes the same parent lock as reorder and definition
// writes. The indexed tail lookup does not count or enumerate the membership.
func (s *PostgresUserStore) AppendCollectionItem(ctx context.Context, collectionID, mediaItemID string) error {
	return s.addCollectionItem(ctx, collectionID, mediaItemID, 0, true)
}

func (s *PostgresUserStore) addCollectionItem(ctx context.Context, collectionID, mediaItemID string, position int, appendItem bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := s.lockCollectionRow(ctx, tx, collectionID); err != nil {
		return err
	}
	if appendItem {
		if err := s.requireManualCollection(ctx, tx, collectionID); err != nil {
			return err
		}
	}
	if err := s.insertCollectionItem(ctx, tx, collectionID, mediaItemID, position, appendItem); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresUserStore) requireManualCollection(ctx context.Context, tx pgx.Tx, collectionID string) error {
	var kind string
	err := tx.QueryRow(ctx, `SELECT collection_type FROM user_personal_collections WHERE user_id=$1 AND id=$2`, s.userID, collectionID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return userstore.ErrCollectionNotFound
	}
	if err != nil {
		return err
	}
	if kind != collectionTypeManual {
		return userstore.ErrCollectionNotManual
	}
	return nil
}

func (s *PostgresUserStore) insertCollectionItem(ctx context.Context, tx pgx.Tx, collectionID, mediaItemID string, position int, appendItem bool) error {
	_, err := tx.Exec(ctx, `INSERT INTO user_personal_collection_items(user_id,collection_id,media_item_id,position,added_at)
 SELECT $1,$2,$3,CASE WHEN $6 THEN COALESCE((
  SELECT COALESCE(position,0) FROM user_personal_collection_items
  WHERE user_id=$1 AND collection_id=$2 AND sub_item_id=''
  ORDER BY COALESCE(position,0) DESC,media_item_id DESC LIMIT 1
 ),-1)+1 ELSE $4 END,$5
 WHERE NOT EXISTS(SELECT 1 FROM user_personal_collection_items WHERE user_id=$1 AND collection_id=$2 AND media_item_id=$3 AND sub_item_id='')
 ON CONFLICT(user_id,collection_id,media_item_id,sub_item_id) DO NOTHING`, s.userID, collectionID, mediaItemID, position, nowUTC(), appendItem)
	return err
}
