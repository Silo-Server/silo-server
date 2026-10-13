package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

var _ userstore.CollectionMembershipReader = (*PostgresUserStore)(nil)

// ManualCollectionsHolding reads, in one query, which of the profile's own
// manual collections hold the title. Each EXISTS probe is a primary-key
// lookup on the item table.
func (s *PostgresUserStore) ManualCollectionsHolding(ctx context.Context, creatorProfileID, mediaItemID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id FROM user_personal_collections c
		 WHERE c.user_id = $1 AND c.creator_profile_id = $2 AND c.collection_type = 'manual'
		   AND EXISTS (
		     SELECT 1 FROM user_personal_collection_items i
		     WHERE i.user_id = c.user_id AND i.collection_id = c.id
		       AND i.media_item_id = $3 AND i.sub_item_id = ''
		   )`,
		s.userID, creatorProfileID, mediaItemID)
	if err != nil {
		return nil, fmt.Errorf("listing collections holding an item: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("scanning collection ids: %w", err)
	}
	return ids, nil
}
