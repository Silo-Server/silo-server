package metadata

import (
	"context"
	"fmt"
	"strings"
)

// MergeItems merges the item at fromContentID into toContentID: files, library
// memberships, provider ids, and all per-user state (via the shared
// reattribution engine inside rebindItemToExistingItem) move to the target and
// the source row is deleted. Both items must exist and share a type. This is
// the admin-facing repair for a wrong split — two catalog items that are one
// logical title.
func (s *MetadataService) MergeItems(ctx context.Context, fromContentID, toContentID string) error {
	if s == nil {
		return fmt.Errorf("metadata service unavailable")
	}
	fromContentID = strings.TrimSpace(fromContentID)
	toContentID = strings.TrimSpace(toContentID)
	if fromContentID == "" || toContentID == "" {
		return fmt.Errorf("merge requires source and target content ids")
	}
	if fromContentID == toContentID {
		return fmt.Errorf("merge source and target are the same item")
	}

	from, err := s.itemRepo.GetByID(ctx, fromContentID)
	if err != nil {
		return fmt.Errorf("loading merge source %s: %w", fromContentID, err)
	}
	to, err := s.itemRepo.GetByID(ctx, toContentID)
	if err != nil {
		return fmt.Errorf("loading merge target %s: %w", toContentID, err)
	}
	if from.Type != to.Type {
		return fmt.Errorf("cannot merge %s item into %s item", from.Type, to.Type)
	}

	// allowMatchedSource: an operator merging duplicates typically merges two
	// fully matched items; the source row must still be deletable.
	return s.rebindItemToExistingItem(ctx, fromContentID, toContentID, true)
}
