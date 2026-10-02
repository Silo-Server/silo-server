package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnknownAuthInstallation refuses linking to an installation that has no
// auth provider binding.
var ErrUnknownAuthInstallation = errors.New("installation is not an auth provider")

// IdentityService manages the external identities linked to accounts: the
// account's own list and unlink, and the administrator's link and unlink.
type IdentityService struct {
	pool *pgxpool.Pool
}

// NewIdentityService builds the service over the account database.
func NewIdentityService(pool *pgxpool.Pool) *IdentityService {
	return &IdentityService{pool: pool}
}

// ListForUser returns the identities linked to an account, oldest first.
func (s *IdentityService) ListForUser(ctx context.Context, userID int) ([]LinkedIdentity, error) {
	if _, err := userByID(ctx, s.pool, userID); err != nil {
		return nil, err
	}
	return listIdentitiesForUser(ctx, s.pool, userID)
}

// CanUnlinkOwn reports whether UnlinkOwn would let the account disconnect
// one of its identities now: it has another identity, or its local password
// still signs in (local password sign-in on for the account, and either the
// server switch on or the account break-glass).
func (s *IdentityService) CanUnlinkOwn(ctx context.Context, userID int) (bool, error) {
	user, err := userByID(ctx, s.pool, userID)
	if err != nil {
		return false, err
	}
	identities, err := listIdentitiesForUser(ctx, s.pool, userID)
	if err != nil {
		return false, err
	}
	if len(identities) >= 2 {
		return true, nil
	}
	if !user.LocalPasswordLoginEnabled {
		return false, nil
	}
	if user.BreakGlass {
		return true, nil
	}
	return localPasswordLoginAllowed(ctx, s.pool)
}

// UnlinkOwn removes one of the caller's identities, but only while the
// account can still sign in another way: its local password (when local
// sign-in is on, or the account is break-glass), or another identity.
func (s *IdentityService) UnlinkOwn(ctx context.Context, userID int, identityID int64) error {
	var removed *LinkedIdentity
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		user, err := lockUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		if _, err := identityForUser(ctx, tx, userID, identityID); err != nil {
			return err
		}
		identities, err := listIdentitiesForUser(ctx, tx, userID)
		if err != nil {
			return err
		}
		canUseLocal := false
		if user.LocalPasswordLoginEnabled {
			canUseLocal = user.BreakGlass
			if !canUseLocal {
				// Serialize with a settings write that could turn local sign-in
				// off between this check and the delete.
				if err := lockServerSettings(ctx, tx); err != nil {
					return err
				}
				if canUseLocal, err = localPasswordLoginAllowed(ctx, tx); err != nil {
					return err
				}
			}
		}
		if !canUseLocal && len(identities) < 2 {
			return ErrLastSignInMethod
		}
		removed, err = deleteIdentity(ctx, tx, userID, identityID)
		return err
	})
	if err != nil {
		return err
	}
	auditAuthEvent(ctx, "identity_unlinked", auditInstallationID, removed.InstallationID, auditUserID, userID, "method", "self")
	return nil
}

// AdminLinkInput is an administrator linking an account to a provider
// identity by its exact subject.
type AdminLinkInput struct {
	UserID         int
	InstallationID int
	// Identity carries the subject and optional display details; the
	// provider refreshes the details at the next sign-in.
	Identity ExternalIdentity
	ActorID  int
}

// AdminLink links an account to an identity. The installation must have an
// auth provider binding; the identity must not belong to another account,
// and the account must not already have one at that installation. Linking
// turns the account's local password off unless it is break-glass.
func (s *IdentityService) AdminLink(ctx context.Context, in AdminLinkInput) (*LinkedIdentity, error) {
	in.Identity.Subject = strings.TrimSpace(in.Identity.Subject)
	if in.Identity.Subject == "" {
		return nil, ErrIdentityNotFound
	}
	var linked *LinkedIdentity
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plugin_auth_bindings WHERE plugin_installation_id = $1)`,
			in.InstallationID).Scan(&known); err != nil {
			return fmt.Errorf("checking auth binding: %w", err)
		}
		if !known {
			return ErrUnknownAuthInstallation
		}
		if err := lockExternalSubject(ctx, tx, in.InstallationID, in.Identity.Subject); err != nil {
			return err
		}
		user, err := lockUser(ctx, tx, in.UserID)
		if err != nil {
			return err
		}
		linked, err = linkIdentityTx(ctx, tx, user, in.InstallationID, in.Identity, false, true)
		return err
	})
	if err != nil {
		return nil, err
	}
	auditAuthEvent(ctx, "identity_linked", auditInstallationID, in.InstallationID, auditUserID, in.UserID,
		"method", "admin", "actor_user_id", in.ActorID)
	return linked, nil
}

// AdminUnlink removes an identity from an account. The administrator may
// leave the account without a sign-in method: setting a password for it
// turns its local password sign-in back on.
func (s *IdentityService) AdminUnlink(ctx context.Context, userID int, identityID int64, actorID int) error {
	if _, err := userByID(ctx, s.pool, userID); err != nil {
		return err
	}
	removed, err := deleteIdentity(ctx, s.pool, userID, identityID)
	if err != nil {
		return err
	}
	auditAuthEvent(ctx, "identity_unlinked", auditInstallationID, removed.InstallationID, auditUserID, userID,
		"method", "admin", "actor_user_id", actorID)
	return nil
}
