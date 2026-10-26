package handlers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// mutatingUserRepo runs an admin account mutation's validation against one
// stored account, the way MutateAdminAccount does inside its transaction.
type mutatingUserRepo struct {
	UserRepository
	current models.User
	applied bool
	// input is the update the mutation received.
	input *models.UpdateUserInput
}

// GetByID knows the stored account and the Owner, so the Owner rules can
// load the caller.
func (r *mutatingUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	if id == r.current.ID {
		return &r.current, nil
	}
	if id == testOwnerID {
		owner := ownerAccount()
		return &owner, nil
	}
	return nil, auth.ErrNotFound
}

func (r *mutatingUserRepo) GetAdminSnapshot(context.Context, int) (auth.AdminUserSnapshot, error) {
	return auth.AdminUserSnapshot{User: &r.current}, nil
}

func (r *mutatingUserRepo) MutateAdminAccount(_ context.Context, _ int, _ int64, input *models.UpdateUserInput, validate func(*models.User, pgx.Tx) (bool, error)) (auth.AdminUserSnapshot, error) {
	r.input = input
	if _, err := validate(&r.current, nil); err != nil {
		return auth.AdminUserSnapshot{User: &r.current}, err
	}
	r.applied = true
	return auth.AdminUserSnapshot{User: &r.current}, nil
}

// An administrator's password write turns the account's local password
// sign-in on, so a temporary password works for an account an external
// provider managed too: it is how an administrator recovers an account whose
// provider is gone (docs/architecture/external-sign-in.md).
func TestTemporaryPasswordTurnsLocalPasswordSignInOn(t *testing.T) {
	password := "temporary-pass"
	for name, local := range map[string]bool{"local": true, "external provider": false} {
		repo := &mutatingUserRepo{current: models.User{ID: 7, Role: models.RoleUser, LocalPasswordLoginEnabled: local}}
		h := &AdminHandler{userRepo: repo}
		_, err := h.UpdateAdminAccount(context.Background(), 7, -1, 0, models.UpdateUserInput{Password: &password, PasswordChangeRequired: true})
		if err != nil || !repo.applied || repo.input.LocalPasswordLoginEnabled == nil || !*repo.input.LocalPasswordLoginEnabled {
			t.Errorf("%s: err = %v, applied %v, input %+v", name, err, repo.applied, repo.input)
		}
	}
}
