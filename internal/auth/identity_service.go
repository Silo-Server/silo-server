package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// ErrUnknownAuthInstallation refuses linking to an installation that has no
// auth provider binding.
var ErrUnknownAuthInstallation = errors.New("installation is not an auth provider")

// ErrScopedKeyAdminIdentity refuses changing an admin's sign-in with a
// scoped API key, including one owned by the server Owner.
var ErrScopedKeyAdminIdentity = errors.New("a scoped API key may not change an admin account's sign-in")

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
// any of its identities now: it has another enabled identity, or its local password
// still signs in (local password sign-in on for the account, and either the
// server switch on or the account break-glass).
func (s *IdentityService) CanUnlinkOwn(ctx context.Context, userID int) (bool, error) {
	user, err := userByID(ctx, s.pool, userID)
	if err != nil {
		return false, err
	}
	identities, err := usableIdentityCount(ctx, s.pool, userID, 0)
	if err != nil {
		return false, err
	}
	if identities >= 2 {
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
// sign-in is on, or the account is break-glass), or another enabled identity.
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
		identities, err := usableIdentityCount(ctx, tx, userID, identityID)
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
		if !canUseLocal && identities == 0 {
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
	Identity     ExternalIdentity
	ActorID      int
	ScopedAPIKey bool
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
		user, err := lockAdminIdentityTarget(ctx, tx, in.UserID, in.ActorID, in.ScopedAPIKey)
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
func (s *IdentityService) AdminUnlink(ctx context.Context, userID int, identityID int64, actorID int, scopedAPIKey bool) error {
	var removed *LinkedIdentity
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := lockAdminIdentityTarget(ctx, tx, userID, actorID, scopedAPIKey); err != nil {
			return err
		}
		var err error
		removed, err = deleteIdentity(ctx, tx, userID, identityID)
		return err
	})
	if err != nil {
		return err
	}
	auditAuthEvent(ctx, "identity_unlinked", auditInstallationID, removed.InstallationID, auditUserID, userID,
		"method", "admin", "actor_user_id", actorID)
	return nil
}

// lockAdminIdentityTarget checks the actor and target in the transaction
// that changes the identity. Both accounts are locked in ID order, as in
// ownership transfer, so a promotion or transfer cannot change their
// standing between authorization and the write.
func lockAdminIdentityTarget(ctx context.Context, tx pgx.Tx, userID, actorID int, scopedAPIKey bool) (*models.User, error) {
	rows, err := tx.Query(ctx, `SELECT `+allColumns+` FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int{userID, actorID})
	if err != nil {
		return nil, fmt.Errorf("locking identity management accounts: %w", err)
	}
	users, err := scanUsers(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	actor := OwnerActor{ID: actorID}
	actorAllowed := actorID == 0
	var target *models.User
	for _, user := range users {
		if user.ID == userID {
			target = user
		}
		if user.ID == actorID {
			actor.IsOwner = user.IsOwner
			actorAllowed = user.Enabled && user.Role == models.RoleAdmin
		}
	}
	if target == nil {
		return nil, ErrNotFound
	}
	if !actorAllowed {
		return nil, ErrNotPermitted
	}
	if scopedAPIKey && target.Role == models.RoleAdmin {
		return nil, ErrScopedKeyAdminIdentity
	}
	if err := CheckOwnerTarget(actor, target); err != nil {
		return nil, err
	}
	return target, nil
}
