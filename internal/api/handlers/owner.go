package handlers

import (
	"context"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ownerTargetChecker applies auth.CheckOwnerTarget to an account the caller
// holds only the ID of. *auth.UserRepository implements it.
type ownerTargetChecker interface {
	CheckOwnerTargetByID(ctx context.Context, actorID, userID int) error
}

// actorUserID is the login account making the request; zero without claims,
// which the Owner checks treat as someone other than the Owner.
func actorUserID(ctx context.Context) int {
	if claims := apimw.GetClaims(ctx); claims != nil {
		return claims.UserID
	}
	return 0
}

// requestOwnerActor loads the Owner standing of the account making the
// request. An account the store does not know is not the Owner.
func requestOwnerActor(ctx context.Context, users interface {
	GetByID(context.Context, int) (*models.User, error)
}) (auth.OwnerActor, error) {
	actor := auth.OwnerActor{ID: actorUserID(ctx)}
	if actor.ID <= 0 || users == nil {
		return actor, nil
	}
	user, err := users.GetByID(ctx, actor.ID)
	if err != nil {
		if auth.IsNotFound(err) {
			return actor, nil
		}
		return actor, err
	}
	actor.IsOwner = user.IsOwner
	return actor, nil
}

// codeOwnerProtected is the error code of a refusal under the Owner rules.
const codeOwnerProtected = "owner_protected"

// ownerError renders the Owner rules as a 403 both listeners understand and
// passes every other error through.
func ownerError(err error) error {
	switch {
	case errors.Is(err, auth.ErrOwnerProtected):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can change the owner account", cause: err}
	case errors.Is(err, auth.ErrOwnerStanding):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "The server owner cannot be demoted, disabled or deleted", cause: err}
	case errors.Is(err, auth.ErrAdminProtected):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can grant the admin role or change another admin account", cause: err}
	case errors.Is(err, auth.ErrAdminPolicyProtected):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can change an admin account's access policy", cause: err}
	case errors.Is(err, auth.ErrSelfStanding):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "You cannot change your own role, disable your account, or delete it", cause: err}
	case errors.Is(err, auth.ErrBreakGlassOwnerOnly):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can make or unmake a break-glass account", cause: err}
	case errors.Is(err, auth.ErrSelfPasswordOwnerOnly):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can set a password on your account while password sign-in is off for it", cause: err}
	case errors.Is(err, auth.ErrNotOwner):
		return &APIError{Status: http.StatusForbidden, Code: codeOwnerProtected, Message: "Only the server owner can transfer ownership", cause: err}
	case errors.Is(err, auth.ErrOwnershipTarget):
		return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation_failed", Message: "Ownership can only move to another enabled admin account", cause: err}
	}
	return err
}
