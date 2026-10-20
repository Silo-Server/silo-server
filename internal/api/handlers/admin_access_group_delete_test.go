package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
)

// failingDeleteGroupStore is a guarded group store whose delete fails.
type failingDeleteGroupStore struct {
	*accessGroupHandlerTestStore
	deleteErr error
}

func (s *failingDeleteGroupStore) ListPage(context.Context, *access.GroupPageKey, int) ([]access.Group, bool, error) {
	return nil, false, nil
}

func (s *failingDeleteGroupStore) UpdateConditional(context.Context, int64, access.UpdateGroupInput, access.GroupPrecondition) (*access.Group, error) {
	return nil, errors.New("not used")
}

func (s *failingDeleteGroupStore) DeleteConditional(context.Context, int64, access.GroupPrecondition) error {
	return s.deleteErr
}

func TestDeleteAdminAccessGroupKeepsStoreErrors(t *testing.T) {
	store := &failingDeleteGroupStore{
		accessGroupHandlerTestStore: newAccessGroupHandlerTestStore(),
		deleteErr:                   access.ErrDefaultGroupRequired,
	}
	handler := NewAccessGroupHandler(store)

	err := handler.DeleteAdminAccessGroup(t.Context(), 1, access.GroupPrecondition{Any: true})
	if !errors.Is(err, access.ErrDefaultGroupRequired) {
		t.Fatalf("error = %v, want ErrDefaultGroupRequired", err)
	}
}
