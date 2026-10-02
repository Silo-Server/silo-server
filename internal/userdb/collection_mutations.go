package userdb

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

func checkSQLiteCollectionRevision(tx *sql.Tx, id string, expected *int64) error {
	if expected == nil {
		return nil
	}
	// A conditional UPDATE obtains SQLite's writer lock before reading the
	// current witness, so concurrent writers cannot both consume it.
	result, err := tx.Exec(`UPDATE personal_collection_revisions SET revision=revision+1 WHERE collection_id=? AND (revision=? OR ?=-1) AND EXISTS(SELECT 1 FROM personal_collections WHERE id=?)`, id, *expected, *expected, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM personal_collections WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return userstore.ErrCollectionNotFound
	} else if err != nil {
		return err
	}
	return userstore.ErrCollectionRevisionMismatch
}
func (s *SQLiteUserStore) DeleteCollectionIfRevision(_ context.Context, id string, expected int64) error {
	return deleteCollection(s.db, id, &expected)
}
func (s *SQLiteUserStore) CollectionOrderRevision(ctx context.Context) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx, `SELECT revision FROM personal_collection_order_revision WHERE singleton=1`).Scan(&revision)
	return revision, err
}
func (s *SQLiteUserStore) ReorderCollectionItemsIfRevision(context.Context, string, []string, int64) error {
	return userstore.ErrCollectionPagingUnsupported
}
func (s *SQLiteUserStore) ReorderCollectionsIfRevision(context.Context, string, *string, []string, int64) error {
	return userstore.ErrCollectionPagingUnsupported
}
func (s *SQLiteUserStore) UpdateCollectionGroupIfRevision(context.Context, string, *string, *string, *userstore.GroupSortMode, int64) (*userstore.CollectionGroup, error) {
	return nil, userstore.ErrCollectionPagingUnsupported
}
func (s *SQLiteUserStore) DeleteCollectionGroupIfRevision(context.Context, string, int64) error {
	return userstore.ErrCollectionPagingUnsupported
}
func (s *SQLiteUserStore) ReorderCollectionGroupsIfRevision(context.Context, []string, int64) error {
	return userstore.ErrCollectionPagingUnsupported
}
