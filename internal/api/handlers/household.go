package handlers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// userLookup is the slice of the user repository the household check needs: a
// profile token is only valid against the access-policy revision it was minted
// for, so verifying one means reading the user's current revision.
type userLookup interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

// canManageHousehold reports whether the caller may act for the whole household
// — every profile on their own account — rather than only for themselves.
//
// Server admins always may. Otherwise the caller's active profile must be the
// one flagged is_primary, which is the household parent and is deliberately
// *not* the server-wide admin role: a household parent manages their family,
// an admin manages the server.
//
// When that primary profile has a PIN, management additionally requires a valid
// X-Profile-Token from /profiles/{id}/verify-pin. Without that, a client could
// walk past a profile lock by sending only X-Profile-Id.
//
// This is a policy boundary for well-behaved clients rather than a defense
// against the account holder: every profile on an account shares one login
// session, so X-Profile-Id is self-asserted (see the note in
// internal/api/middleware/auth.go). It is the same boundary profile management
// has always used, and it is applied here so household settings management is
// guarded and auditable rather than implicit.
func canManageHousehold(
	r *http.Request,
	store userstore.UserStore,
	users userLookup,
	tokens *access.ProfileTokenService,
) (bool, error) {
	ctx := r.Context()
	if apimw.IsAdmin(ctx) {
		return true, nil
	}
	activeProfileID := apimw.GetProfileID(ctx)
	if activeProfileID == "" {
		activeProfileID = r.Header.Get("X-Profile-Id")
	}
	if activeProfileID == "" {
		return false, nil
	}
	active, err := store.GetProfile(ctx, activeProfileID)
	if err != nil {
		return false, err
	}
	if active == nil {
		return false, nil
	}
	if !active.IsPrimary {
		return false, nil
	}
	if active.PINHash == "" {
		return true, nil
	}
	if err := verifyProfileToken(r, users, tokens, active.ID); err != nil {
		return false, err
	}
	return true, nil
}

// verifyProfileToken checks the X-Profile-Token a PIN-locked profile must
// present. Missing dependencies fail closed: a handler wired without a token
// service cannot verify a PIN, and "cannot verify" is not "verified".
func verifyProfileToken(
	r *http.Request,
	users userLookup,
	tokens *access.ProfileTokenService,
	profileID string,
) error {
	if users == nil || tokens == nil {
		return access.ErrProfileUnverified
	}

	claims := apimw.GetClaims(r.Context())
	if claims == nil || claims.SessionID == "" {
		return access.ErrProfileUnverified
	}

	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		return access.ErrProfileUnverified
	}

	user, err := users.GetByID(r.Context(), userID)
	if err != nil {
		return fmt.Errorf("loading user policy: %w", err)
	}
	if user == nil {
		return access.ErrProfileUnverified
	}

	profileClaims, err := tokens.Validate(r.Header.Get("X-Profile-Token"))
	if err != nil {
		return err
	}
	if profileClaims.UserID != userID ||
		profileClaims.SessionID != claims.SessionID ||
		profileClaims.ProfileID != profileID ||
		profileClaims.PolicyRevision != user.AccessPolicyRevision {
		return access.ErrProfileUnverified
	}

	return nil
}
