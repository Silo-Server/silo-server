package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type stubAccountUsers struct {
	createFn func(context.Context, models.CreateUserInput) (*models.User, error)
	deleteFn func(context.Context, int) error
}

func (s stubAccountUsers) Create(ctx context.Context, input models.CreateUserInput) (*models.User, error) {
	return s.createFn(ctx, input)
}

func (s stubAccountUsers) Delete(ctx context.Context, id int) error {
	if s.deleteFn == nil {
		return nil
	}
	return s.deleteFn(ctx, id)
}

type stubStoreProvider struct {
	store userstore.UserStore
	err   error
}

func (s stubStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.store, nil
}

func (s stubStoreProvider) Close() error { return nil }

type stubUserStore struct {
	userstore.UserStore
	createProfileFn func(context.Context, userstore.Profile) error
}

func (s stubUserStore) CreateProfile(ctx context.Context, p userstore.Profile) error {
	return s.createProfileFn(ctx, p)
}

func TestAccountProvisionerCreateAccount_DeletesUserWhenProfileCreationFails(t *testing.T) {
	var deletedUserID int
	provisioner := NewAccountProvisioner(
		stubAccountUsers{
			createFn: func(context.Context, models.CreateUserInput) (*models.User, error) {
				return &models.User{ID: 9, Username: "alex"}, nil
			},
			deleteFn: func(_ context.Context, id int) error {
				deletedUserID = id
				return nil
			},
		},
		stubStoreProvider{
			store: stubUserStore{
				createProfileFn: func(context.Context, userstore.Profile) error {
					return errors.New("boom")
				},
			},
		},
	)

	_, err := provisioner.CreateAccount(context.Background(), CreateAccountInput{
		User: models.CreateUserInput{Username: "alex"},
		DefaultProfile: DefaultProfileOptions{
			Enabled: true,
		},
	})
	if err == nil {
		t.Fatal("CreateAccount returned nil error, want failure")
	}
	if deletedUserID != 9 {
		t.Fatalf("deletedUserID = %d, want 9", deletedUserID)
	}
}

func TestAccountProvisionerInvitedRequiresAtomicRepository(t *testing.T) {
	provisioner := NewAccountProvisioner(stubAccountUsers{}, nil)
	if _, err := provisioner.CreateInvitedAccount(t.Context(), CreateAccountInput{}, "code"); err == nil {
		t.Fatal("invited provisioning must reject a repository without atomic invite support")
	}
}
