package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
)

type AccessGroupStore interface {
	List(ctx context.Context) ([]access.Group, error)
	Get(ctx context.Context, id int64) (*access.Group, error)
	Create(ctx context.Context, input access.CreateGroupInput) (*access.Group, error)
	Update(ctx context.Context, id int64, input access.UpdateGroupInput) (*access.Group, error)
	Delete(ctx context.Context, id int64) error
}

type AccessGroupHandler struct {
	store AccessGroupStore
	// RecWorker is told when a group's libraries change or the group is
	// deleted, so its members' recommendations are rebuilt. Optional.
	RecWorker AccountsScopeNotifier
}

func NewAccessGroupHandler(store AccessGroupStore) *AccessGroupHandler {
	return &AccessGroupHandler{store: store}
}
