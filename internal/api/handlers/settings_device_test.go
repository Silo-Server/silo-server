package handlers

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testAdminUserRepo struct {
	users map[int]*models.User
}

func (r testAdminUserRepo) List(context.Context) ([]*models.User, error) {
	out := make([]*models.User, 0, len(r.users))
	for _, user := range r.users {
		out = append(out, user)
	}
	return out, nil
}

func (r testAdminUserRepo) ListPage(context.Context, int, int, string) ([]*models.User, error) {
	return nil, nil
}

func (r testAdminUserRepo) Create(context.Context, models.CreateUserInput) (*models.User, error) {
	panic("unexpected Create call")
}

func (r testAdminUserRepo) Update(context.Context, int, models.UpdateUserInput) error {
	panic("unexpected Update call")
}

func (r testAdminUserRepo) Delete(context.Context, int) error {
	panic("unexpected Delete call")
}

func (r testAdminUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	return r.users[id], nil
}

type mappedTestUserStoreProvider struct {
	stores map[int]userstore.UserStore
}

func (p mappedTestUserStoreProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	return p.stores[userID], nil
}

func (p mappedTestUserStoreProvider) Close() error { return nil }
