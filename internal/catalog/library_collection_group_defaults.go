package catalog

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
)

const (
	CanonicalUserCollectionsGroupLabel     = "user-collections"
	CanonicalUserCollectionsGroupTitle     = "My collections"
	CanonicalUserCollectionsGroupSortOrder = 9998
)

// CanonicalUserCollectionsGroupID returns the stable placement-group ID for a
// library's viewer-private personal collections.
func CanonicalUserCollectionsGroupID(libraryID int) string {
	return fmt.Sprintf("lcg_user_%d", libraryID)
}

// seedCanonicalUserCollectionsGroup inserts the required personal-collection
// placement group in the caller's library-creation transaction.
func seedCanonicalUserCollectionsGroup(ctx context.Context, tx pgx.Tx, libraryID int) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO library_collection_groups (
			id, library_id, label, title, name, slug, kind, default_sort_mode, sort_order
		)
		VALUES ($1, $2, $3, $4, $4, $3, $5, $6, $7)`,
		CanonicalUserCollectionsGroupID(libraryID),
		libraryID,
		CanonicalUserCollectionsGroupLabel,
		CanonicalUserCollectionsGroupTitle,
		models.GroupKindUserCollections,
		models.GroupSortManual,
		CanonicalUserCollectionsGroupSortOrder,
	)
	if err != nil {
		return fmt.Errorf("seeding user collections group: %w", err)
	}
	return nil
}
