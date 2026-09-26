package handlers

import (
	"context"
	"errors"
	"net/http"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

// ownerTargetChecker refuses a caller other than the server Owner acting on
// the Owner's account. *auth.UserRepository implements it.
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
	}
	return err
}
