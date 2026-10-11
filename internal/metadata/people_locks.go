package metadata

import (
	"context"
	"log/slog"
	"slices"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// metadataItemPeopleRepo reads an item's stored credits. It is asserted on
// itemRepo; *catalog.ItemRepository satisfies it.
type metadataItemPeopleRepo interface {
	GetPeople(ctx context.Context, contentID string) ([]models.ItemPerson, error)
}

var _ metadataItemPeopleRepo = (*catalog.ItemRepository)(nil)

// peopleLocksForMerge prepares a stored item for the persist merge when
// exactly one of its Cast and Crew locks is set. The item row carries no
// credits, and the persist step replaces the stored set wholesale with the
// merged one, so the locked group would be lost: this loads the stored locked
// group into existing so the merge keeps it. The unlocked group behaves as it
// does without a lock: replaced by the incoming one, or kept as stored when
// the providers sent none. When the credits cannot be read, the returned
// locks hold both groups and the credits stay as stored.
func (s *MetadataService) peopleLocksForMerge(ctx context.Context, contentID string, locked []MetadataField, existing, incoming *MetadataResult) []MetadataField {
	castLocked, crewLocked := isFieldLocked(locked, FieldCast), isFieldLocked(locked, FieldCrew)
	if castLocked == crewLocked || len(existing.People) > 0 {
		return locked
	}
	bothLocked := append(slices.Clone(locked), FieldCast, FieldCrew)
	repo, ok := s.itemRepo.(metadataItemPeopleRepo)
	if !ok {
		return bothLocked
	}
	people, err := repo.GetPeople(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "metadata: could not read stored credits for a cast or crew lock; leaving credits unchanged", "component", "metadata",
			"content_id", contentID, "error", err)
		return bothLocked
	}
	storedCast, storedCrew := splitPeopleByRole(people)
	incomingCast, incomingCrew := splitPeopleByRole(incoming.People)
	if castLocked {
		existing.People = storedCast
		if len(incomingCrew) == 0 {
			existing.People = append(existing.People, storedCrew...)
		}
	} else {
		if len(incomingCast) == 0 {
			existing.People = storedCast
		}
		existing.People = append(existing.People, storedCrew...)
	}
	return locked
}
