package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// memberMovingTestStore is a guarded group store whose delete moves members,
// recording what the handler asks it to do.
type memberMovingTestStore struct {
	*accessGroupHandlerTestStore
	deleteErr    error
	movingDelete int
	plainDeletes int
}

func (s *memberMovingTestStore) ListPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error) {
	return nil, false, nil
}

func (s *memberMovingTestStore) UpdateConditional(context.Context, int64, access.UpdateGroupInput, access.GroupPrecondition) (*access.Group, error) {
	return nil, errors.New("not used")
}

func (s *memberMovingTestStore) DeleteConditional(context.Context, int64, access.GroupPrecondition) error {
	s.plainDeletes++
	return nil
}

func (s *memberMovingTestStore) DeleteMovingMembers(context.Context, int64, access.GroupPrecondition) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.movingDelete++
	return nil
}

// Members move to the default group without being signed out; the store
// bumps their access_policy_revision (TestGroupStoreDeleteMovingMembersDB).
func TestDeleteAdminAccessGroupMovesMembersThroughTheStore(t *testing.T) {
	store := &memberMovingTestStore{accessGroupHandlerTestStore: newAccessGroupHandlerTestStore()}
	handler := NewAccessGroupHandler(store)

	if err := handler.DeleteAdminAccessGroup(t.Context(), 5, access.GroupPrecondition{Any: true}); err != nil {
		t.Fatalf("DeleteAdminAccessGroup() error: %v", err)
	}
	if store.plainDeletes != 0 || store.movingDelete != 1 {
		t.Fatalf("plain deletes %d, member-moving deletes %d; want only the member-moving delete", store.plainDeletes, store.movingDelete)
	}
}

func TestDeleteAdminAccessGroupKeepsStoreErrors(t *testing.T) {
	store := &memberMovingTestStore{
		accessGroupHandlerTestStore: newAccessGroupHandlerTestStore(),
		deleteErr:                   access.ErrDefaultGroupRequired,
	}
	handler := NewAccessGroupHandler(store)

	err := handler.DeleteAdminAccessGroup(t.Context(), 1, access.GroupPrecondition{Any: true})
	if !errors.Is(err, access.ErrDefaultGroupRequired) {
		t.Fatalf("error = %v, want ErrDefaultGroupRequired", err)
	}
}
