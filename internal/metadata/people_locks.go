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
// group into existing so the merge keeps it. The unlocked group is replaced
// by the incoming one, or kept as stored when the providers sent none for it.
// (Without either lock a replace merge takes the incoming credits wholesale,
// as before.) When the credits cannot be read, the returned locks hold both
// groups and the credits stay as stored.
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

// keepStoredLocalCredits prepares a fill-empty write (scheduled refresh,
// initial match of a stored item): the cast or crew the local provider
// supplied fills an empty stored group but does not replace a stored one, as
// keepStoredLists does for the other lists; a manual refresh replaces it. A
// stored group that no provider sent anything for is kept too, since the write
// replaces credits wholesale. The item row carries no credits, so the stored
// ones are read. When they cannot be read, the credits stay as stored.
func (s *MetadataService) keepStoredLocalCredits(ctx context.Context, contentID string, accumulator *MetadataResult) {
	l := accumulator.localLists
	if !l.cast && !l.crew {
		return
	}
	repo, ok := s.itemRepo.(metadataItemPeopleRepo)
	if !ok {
		return
	}
	stored, err := repo.GetPeople(ctx, contentID)
	if err != nil {
		slog.WarnContext(ctx, "metadata: could not read stored credits; leaving credits unchanged", "component", "metadata",
			"content_id", contentID, "error", err)
		accumulator.People = nil
		return
	}
	storedCast, storedCrew := splitPeopleByRole(stored)
	cast, crew := splitPeopleByRole(accumulator.People)
	if len(storedCast) > 0 && (l.cast || len(cast) == 0) {
		cast = storedCast
	}
	if len(storedCrew) > 0 && (l.crew || len(crew) == 0) {
		crew = storedCrew
	}
	accumulator.People = append(cast, crew...)
}
