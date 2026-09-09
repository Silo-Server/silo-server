package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type AccountUserRepository interface {
	Create(ctx context.Context, input models.CreateUserInput) (*models.User, error)
	Delete(ctx context.Context, id int) error
}

type initialAccountUserRepository interface {
	CreateInitial(ctx context.Context, input models.CreateUserInput) (*models.User, error)
}

type DefaultProfileOptions struct {
	Enabled bool
	Name    string
}

type CreateAccountInput struct {
	User           models.CreateUserInput
	DefaultProfile DefaultProfileOptions
}

type AccountProvisioner struct {
	users         AccountUserRepository
	storeProvider userstore.UserStoreProvider
}

func NewAccountProvisioner(
	users AccountUserRepository,
	storeProvider userstore.UserStoreProvider,
) *AccountProvisioner {
	return &AccountProvisioner{
		users:         users,
		storeProvider: storeProvider,
	}
}

func (p *AccountProvisioner) CreateAccount(
	ctx context.Context,
	input CreateAccountInput,
) (*models.User, error) {
	return p.createAccount(ctx, input, p.users.Create)
}

// CreateInitialAccount provisions the first account through the repository's
// database-enforced empty-table claim, then applies the normal profile flow.
func (p *AccountProvisioner) CreateInitialAccount(
	ctx context.Context,
	input CreateAccountInput,
) (*models.User, error) {
	initialUsers, ok := p.users.(initialAccountUserRepository)
	if !ok {
		return nil, fmt.Errorf("initial account creation unavailable")
	}
	return p.createAccount(ctx, input, initialUsers.CreateInitial)
}

func (p *AccountProvisioner) createAccount(
	ctx context.Context,
	input CreateAccountInput,
	createUser func(context.Context, models.CreateUserInput) (*models.User, error),
) (*models.User, error) {
	user, err := createUser(ctx, input.User)
	if err != nil {
		return nil, err
	}

	if !input.DefaultProfile.Enabled {
		return user, nil
	}

	if err := p.createDefaultProfile(ctx, user.ID, input); err != nil {
		if deleteErr := p.users.Delete(ctx, user.ID); deleteErr != nil {
			return nil, fmt.Errorf(
				"create default profile: %w (cleanup user: %v)",
				err,
				deleteErr,
			)
		}
		return nil, fmt.Errorf("create default profile: %w", err)
	}

	return user, nil
}

func (p *AccountProvisioner) createDefaultProfile(
	ctx context.Context,
	userID int,
	input CreateAccountInput,
) error {
	if p.storeProvider == nil {
		return fmt.Errorf("user store provider unavailable")
	}

	store, err := p.storeProvider.ForUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("open user store: %w", err)
	}

	name := strings.TrimSpace(input.DefaultProfile.Name)
	if name == "" {
		name = strings.TrimSpace(input.User.Username)
	}
	if name == "" {
		return fmt.Errorf("default profile name is required")
	}

	if err := store.CreateProfile(ctx, userstore.Profile{
		Name:                name,
		ShowForcedSubtitles: true,
	}); err != nil {
		return fmt.Errorf("store profile: %w", err)
	}

	return nil
}
