package auth

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// The server Owner is the account that claimed the server at first-run setup
// (users.is_owner). Other admins manage every other account as before, but
// only the Owner may change the Owner's account, and the Owner stays an
// enabled admin: nothing demotes, disables or deletes it.
var (
	// ErrOwnerProtected refuses a change to the Owner's account by anyone else.
	ErrOwnerProtected = errors.New("only the server owner can change the owner account")
	// ErrOwnerStanding refuses a change that would demote, disable or delete the Owner.
	ErrOwnerStanding = errors.New("the server owner cannot be demoted, disabled or deleted")
)

// CheckOwnerTarget refuses actorID acting on target when target is the Owner
// and actorID is not.
func CheckOwnerTarget(actorID int, target *models.User) error {
	if target != nil && target.IsOwner && target.ID != actorID {
		return ErrOwnerProtected
	}
	return nil
}

// CheckOwnerUpdate is CheckOwnerTarget plus the Owner's own standing: the
// update may not remove the Owner's admin role or disable it.
func CheckOwnerUpdate(actorID int, target *models.User, input models.UpdateUserInput) error {
	if err := CheckOwnerTarget(actorID, target); err != nil {
		return err
	}
	if target != nil && target.IsOwner &&
		((input.Role != nil && *input.Role != models.RoleAdmin) || (input.Enabled != nil && !*input.Enabled)) {
		return ErrOwnerStanding
	}
	return nil
}

// CheckOwnerDelete refuses deleting the Owner, by anyone.
func CheckOwnerDelete(actorID int, target *models.User) error {
	if err := CheckOwnerTarget(actorID, target); err != nil {
		return err
	}
	if target != nil && target.IsOwner {
		return ErrOwnerStanding
	}
	return nil
}

// CheckOwnerTargetByID loads the account userID and applies CheckOwnerTarget,
// for callers that hold only the account ID.
func (r *UserRepository) CheckOwnerTargetByID(ctx context.Context, actorID, userID int) error {
	var isOwner bool
	if err := r.pool.QueryRow(ctx, `SELECT is_owner FROM users WHERE id=$1`, userID).Scan(&isOwner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return CheckOwnerTarget(actorID, &models.User{ID: userID, IsOwner: isOwner})
}
