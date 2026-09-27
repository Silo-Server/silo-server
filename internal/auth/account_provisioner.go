package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type AccountUserRepository interface {
	Create(ctx context.Context, input models.CreateUserInput) (*models.User, error)
	Delete(ctx context.Context, id int) error
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
	user, err := p.users.Create(ctx, input.User)
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

	profile, err := defaultAccountProfile(input)
	if err != nil {
		return err
	}
	if err := store.CreateProfile(ctx, profile); err != nil {
		return fmt.Errorf("store profile: %w", err)
	}

	return nil
}

// CreateInvitedAccount couples account provisioning to invite redemption.
func (p *AccountProvisioner) CreateInvitedAccount(ctx context.Context, input CreateAccountInput, code string) (*models.User, error) {
	users, ok := p.users.(interface {
		CreateInvited(ctx context.Context, input models.CreateUserInput, code string, provision func(*models.User, pgx.Tx) error) (*models.User, error)
	})
	if !ok {
		return nil, fmt.Errorf("invited account provisioning unavailable")
	}
	return users.CreateInvited(ctx, input.User, code, func(user *models.User, tx pgx.Tx) error {
		return p.createProfileInTransactionOrBridge(ctx, tx, user.ID, input)
	})
}

// CreateInitialAccountInTransaction inserts the first administrator, which
// becomes the server Owner, and its optional profile in the caller's
// transaction. The caller owns commit and rollback. SQLite bridge stores keep
// their separate profile writer; the account still does not commit if that
// writer fails.
func (p *AccountProvisioner) CreateInitialAccountInTransaction(ctx context.Context, tx pgx.Tx, input CreateAccountInput) (*models.User, error) {
	user, err := createUser(ctx, tx, input.User)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET is_owner = true WHERE id = $1`, user.ID); err != nil {
		return nil, fmt.Errorf("marking server owner: %w", err)
	}
	user.IsOwner = true
	if err := p.createProfileInTransactionOrBridge(ctx, tx, user.ID, input); err != nil {
		return nil, err
	}
	return user, nil
}

// createProfileInTransactionOrBridge writes the requested default profile
// through tx when the store can join it, otherwise through the SQLite bridge
// store's own writer. Either failure must abort the caller's transaction.
func (p *AccountProvisioner) createProfileInTransactionOrBridge(ctx context.Context, tx pgx.Tx, userID int, input CreateAccountInput) error {
	if !input.DefaultProfile.Enabled {
		return nil
	}
	if provider, ok := p.storeProvider.(transactionalProfileCreator); ok {
		profile, err := defaultAccountProfile(input)
		if err != nil {
			return err
		}
		if err := provider.CreateProfileInTransaction(ctx, tx, userID, profile); err != nil {
			return fmt.Errorf("store profile: %w", err)
		}
		return nil
	}
	// SQLite bridge stores are separate from the account database. Preserve
	// their existing profile writer, but do not commit the account or invite
	// if that writer fails.
	return p.createDefaultProfile(ctx, userID, input)
}

// ErrTransactionalProfileUnavailable means the selected profile store cannot
// participate in account creation's transaction. No account has been inserted.
var ErrTransactionalProfileUnavailable = errors.New("transactional profile creation unavailable")

type transactionalProfileCreator interface {
	CreateProfileInTransaction(context.Context, pgx.Tx, int, userstore.Profile) error
}

// CreateAccountInTransaction creates an account and its requested profile in
// the caller's transaction. The caller owns commit and rollback. Unlike the
// legacy signup bridge, this operation never writes a separate SQLite store.
func (p *AccountProvisioner) CreateAccountInTransaction(ctx context.Context, tx pgx.Tx, input CreateAccountInput) (*models.User, error) {
	var profile userstore.Profile
	var provider transactionalProfileCreator
	if input.DefaultProfile.Enabled {
		var ok bool
		provider, ok = p.storeProvider.(transactionalProfileCreator)
		if !ok {
			return nil, ErrTransactionalProfileUnavailable
		}
		var err error
		profile, err = defaultAccountProfile(input)
		if err != nil {
			return nil, err
		}
	}
	user, err := createUser(ctx, tx, input.User)
	if err != nil {
		return nil, err
	}
	if provider != nil {
		if err := provider.CreateProfileInTransaction(ctx, tx, user.ID, profile); err != nil {
			return nil, fmt.Errorf("store profile: %w", err)
		}
	}
	return user, nil
}

func defaultAccountProfile(input CreateAccountInput) (userstore.Profile, error) {
	name := strings.TrimSpace(input.DefaultProfile.Name)
	if name == "" {
		name = strings.TrimSpace(input.User.Username)
	}
	if name == "" {
		return userstore.Profile{}, fmt.Errorf("default profile name is required")
	}
	return userstore.Profile{Name: name, ShowForcedSubtitles: true}, nil
}

// SupportsTransactionalProfiles reports the selected provider's capability,
// including wrappers that preserve its transaction-aware profile writer.
func (p *AccountProvisioner) SupportsTransactionalProfiles() bool {
	_, ok := p.storeProvider.(transactionalProfileCreator)
	return ok
}
