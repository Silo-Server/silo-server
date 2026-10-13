package userdb

import (
	"context"
	"fmt"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.CollectionMembershipReader = (*SQLiteUserStore)(nil)

// ManualCollectionsHolding reads, in one query, which of the profile's own
// manual collections hold the title.
func (s *SQLiteUserStore) ManualCollectionsHolding(ctx context.Context, creatorProfileID, mediaItemID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id FROM personal_collections c
		 WHERE c.creator_profile_id = ? AND c.collection_type = 'manual'
		   AND EXISTS (
		     SELECT 1 FROM personal_collection_items i
		     WHERE i.collection_id = c.id AND i.media_item_id = ?
		   )`,
		creatorProfileID, mediaItemID)
	if err != nil {
		return nil, fmt.Errorf("listing collections holding an item: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning collection id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
