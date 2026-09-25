package handlers

import (
	"context"
	"errors"
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
}

func (r *mutatingUserRepo) GetAdminSnapshot(context.Context, int) (auth.AdminUserSnapshot, error) {
	return auth.AdminUserSnapshot{User: &r.current}, nil
}

func (r *mutatingUserRepo) MutateAdminAccount(_ context.Context, _ int, _ int64, _ *models.UpdateUserInput, validate func(*models.User, pgx.Tx) (bool, error)) (auth.AdminUserSnapshot, error) {
	if _, err := validate(&r.current, nil); err != nil {
		return auth.AdminUserSnapshot{User: &r.current}, err
	}
	r.applied = true
	return auth.AdminUserSnapshot{User: &r.current}, nil
}

func TestTemporaryPasswordNeedsLocalPasswordSignIn(t *testing.T) {
	password := "temporary-pass"
	for name, local := range map[string]bool{"local": true, "external provider": false} {
		repo := &mutatingUserRepo{current: models.User{ID: 7, Role: models.RoleUser, LocalPasswordLoginEnabled: local}}
		h := &AdminHandler{userRepo: repo}
		_, err := h.UpdateAdminAccount(context.Background(), 7, -1, 0, models.UpdateUserInput{Password: &password, PasswordChangeRequired: true})
		var apiErr *APIError
		switch {
		case local && (err != nil || !repo.applied):
			t.Errorf("%s: %v, applied %v", name, err, repo.applied)
		case !local && (!errors.As(err, &apiErr) || apiErr.Status != 409 || repo.applied):
			// The account could never run the change, so it would be locked out.
			t.Errorf("%s: err = %v, applied %v; want 409 and no write", name, err, repo.applied)
		}
	}
}
