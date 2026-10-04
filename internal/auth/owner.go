package auth

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

// The server Owner is the account that claimed the server at first-run setup
// (users.is_owner), or the account ownership was last transferred to. Only the
// Owner may grant the admin role or change another admin's account: other
// admins manage ordinary accounts and their own. Nobody else may change the
// Owner's account, and the Owner stays an enabled admin: nothing demotes,
// disables or deletes it.
var (
	// ErrOwnerProtected refuses a change to the Owner's account by anyone else.
	ErrOwnerProtected = errors.New("only the server owner can change the owner account")
	// ErrOwnerStanding refuses a change that would demote, disable or delete the Owner.
	ErrOwnerStanding = errors.New("the server owner cannot be demoted, disabled or deleted")
	// ErrAdminProtected refuses a caller other than the Owner granting the
	// admin role or changing another admin's account.
	ErrAdminProtected = errors.New("only the server owner can grant the admin role or change another admin account")
	// ErrSelfStanding refuses an account changing its own role, disabling
	// itself, or deleting itself: an admin that does any of these locks
	// itself out, and only the Owner changes or removes admins.
	ErrSelfStanding = errors.New("an account cannot change its own role, disable itself, or delete itself")
	// ErrAdminPolicyProtected refuses an admin other than the Owner changing
	// its own access policy: an admin's limits are the Owner's to set, so a
	// restricted shared admin cannot lift them.
	ErrAdminPolicyProtected = errors.New("only the server owner can change an admin account's access policy")
	// ErrBreakGlassOwnerOnly refuses a caller other than the Owner setting or
	// clearing an account's break-glass flag: the flag keeps an admin's
	// password sign-in and its admin role through provider demotion, so an
	// admin must not grant it to itself.
	ErrBreakGlassOwnerOnly = errors.New("only the server owner can make or unmake a break-glass account")
	// ErrSelfPasswordOwnerOnly refuses a caller other than the Owner setting
	// its own password through account administration while its local
	// password sign-in is off (it signs in through a provider). An
	// administrator's password write turns local sign-in back on, and local
	// sign-ins skip the provider's role sync and re-check, so the admin would
	// keep its role through provider demotion, as break-glass would.
	ErrSelfPasswordOwnerOnly = errors.New("only the server owner can turn password sign-in back on for an account of its own")
	// ErrNotOwner refuses a caller other than the Owner transferring ownership.
	ErrNotOwner = errors.New("only the server owner can transfer ownership")
	// ErrOwnershipTarget refuses transferring ownership to an account that
	// is not another enabled admin.
	ErrOwnershipTarget = errors.New("ownership can only move to another enabled admin account")
)

// OwnerActor is the account making a change, as the Owner rules see it. The
// zero value is no account and never the Owner.
type OwnerActor struct {
	ID      int
	IsOwner bool
}

// CheckOwnerTarget refuses actor acting on target when target is the Owner
// and actor is not, or when target is another admin and actor is not the
// Owner. Every account may act on itself.
func CheckOwnerTarget(actor OwnerActor, target *models.User) error {
	if target == nil || target.ID == actor.ID {
		return nil
	}
	if target.IsOwner {
		return ErrOwnerProtected
	}
	if target.Role == models.RoleAdmin && !actor.IsOwner {
		return ErrAdminProtected
	}
	return nil
}

// CheckGrantAdmin refuses a caller other than the Owner creating or inviting
// an account with role.
func CheckGrantAdmin(actor OwnerActor, role string) error {
	if role == models.RoleAdmin && !actor.IsOwner {
		return ErrAdminProtected
	}
	return nil
}

// CheckOwnerUpdate is CheckOwnerTarget plus promotion and standing: only the
// Owner may make an account an admin or change its break-glass flag, the
// update may not remove the Owner's admin role or disable it, no account may
// change its own role or disable itself, no account but the Owner may set its
// own password while its local password sign-in is off, and only the Owner may
// change an admin's access policy.
func CheckOwnerUpdate(actor OwnerActor, target *models.User, input models.UpdateUserInput) error {
	if err := CheckOwnerTarget(actor, target); err != nil {
		return err
	}
	if target == nil {
		return nil
	}
	if input.Role != nil && *input.Role == models.RoleAdmin && target.Role != models.RoleAdmin {
		if err := CheckGrantAdmin(actor, *input.Role); err != nil {
			return err
		}
	}
	if target.IsOwner &&
		((input.Role != nil && *input.Role != models.RoleAdmin) || (input.Enabled != nil && !*input.Enabled)) {
		return ErrOwnerStanding
	}
	if target.ID == actor.ID &&
		((input.Role != nil && *input.Role != target.Role) || (input.Enabled != nil && !*input.Enabled)) {
		return ErrSelfStanding
	}
	if input.BreakGlass != nil && *input.BreakGlass != target.BreakGlass && !actor.IsOwner {
		return ErrBreakGlassOwnerOnly
	}
	if target.ID == actor.ID && !actor.IsOwner && input.Password != nil && !target.LocalPasswordLoginEnabled {
		return ErrSelfPasswordOwnerOnly
	}
	// CheckOwnerTarget already keeps other admins' accounts to the Owner, so
	// this reaches an admin editing its own account.
	if target.Role == models.RoleAdmin && !actor.IsOwner && changesAccessPolicy(target, input) {
		return ErrAdminPolicyProtected
	}
	return nil
}

// changesAccessPolicy reports whether input sets an access-policy override to
// a value other than target's current one. A form that re-sends unchanged
// values is not a change.
func changesAccessPolicy(target *models.User, input models.UpdateUserInput) bool {
	return changesLibraryIDs(input.LibraryIDs, target.LibraryIDs) ||
		changesOverride(input.MaxPlaybackQuality, target.MaxPlaybackQuality) ||
		changesOverride(input.MaxStreams, target.MaxStreams) ||
		changesOverride(input.MaxTranscodes, target.MaxTranscodes) ||
		changesOverride(input.MaxRemoteStreamBitrateKbps, target.MaxRemoteStreamBitrateKbps) ||
		changesOverride(input.MaxLocalStreamBitrateKbps, target.MaxLocalStreamBitrateKbps) ||
		changesOverride(input.TranscodeAllowed, target.TranscodeAllowed) ||
		changesOverride(input.AudioTranscodeAllowed, target.AudioTranscodeAllowed) ||
		changesOverride(input.DownloadAllowed, target.DownloadAllowed) ||
		changesOverride(input.DownloadTranscodeAllowed, target.DownloadTranscodeAllowed) ||
		changesOverride(input.RequestsAllowed, target.RequestsAllowed)
}

func changesOverride[T comparable](input models.Optional[T], current *T) bool {
	if !input.Set {
		return false
	}
	if input.Value == nil || current == nil {
		return input.Value != nil || current != nil
	}
	return *input.Value != *current
}

// changesLibraryIDs compares library lists as sets; nil means inherit and an
// empty list means no libraries.
func changesLibraryIDs(input models.Optional[[]int], current []int) bool {
	if !input.Set {
		return false
	}
	if input.Value == nil || current == nil {
		return input.Value != nil || current != nil
	}
	next, saved := slices.Clone(*input.Value), slices.Clone(current)
	slices.Sort(next)
	slices.Sort(saved)
	return !slices.Equal(slices.Compact(next), slices.Compact(saved))
}

// CheckOwnerDelete is CheckOwnerTarget, and refuses deleting the Owner by
// anyone and any account deleting itself.
func CheckOwnerDelete(actor OwnerActor, target *models.User) error {
	if err := CheckOwnerTarget(actor, target); err != nil {
		return err
	}
	if target != nil && target.IsOwner {
		return ErrOwnerStanding
	}
	if target != nil && target.ID == actor.ID {
		return ErrSelfStanding
	}
	return nil
}

// OwnerActor loads the Owner standing of the account making a change. An
// unknown account, or no account, is not the Owner.
func (r *UserRepository) OwnerActor(ctx context.Context, actorID int) (OwnerActor, error) {
	return ownerActor(ctx, r.pool, actorID)
}

func ownerActor(ctx context.Context, db interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, actorID int) (OwnerActor, error) {
	actor := OwnerActor{ID: actorID}
	if actorID <= 0 {
		return actor, nil
	}
	err := db.QueryRow(ctx, `SELECT is_owner FROM users WHERE id=$1`, actorID).Scan(&actor.IsOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return actor, nil
	}
	return actor, err
}

// CheckOwnerTargetByID loads the actor and the account userID and applies
// CheckOwnerTarget, for callers that hold only the account ID.
func (r *UserRepository) CheckOwnerTargetByID(ctx context.Context, actorID, userID int) error {
	var target models.User
	var actorIsOwner bool
	err := r.pool.QueryRow(ctx, `
		SELECT id, role, is_owner,
		       COALESCE((SELECT is_owner FROM users WHERE id = $1), false)
		FROM users WHERE id = $2`, actorID, userID).Scan(&target.ID, &target.Role, &target.IsOwner, &actorIsOwner)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	return CheckOwnerTarget(OwnerActor{ID: actorID, IsOwner: actorIsOwner}, &target)
}

// TransferOwnership makes toID the Owner in place of fromID, which must be
// the Owner. toID must be another enabled admin. fromID stays an admin.
func (r *UserRepository) TransferOwnership(ctx context.Context, fromID, toID int) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if fromID == toID {
		return ErrOwnershipTarget
	}
	// Lock both accounts in id order so a transfer cannot interleave with
	// another change to either.
	rows, err := tx.Query(ctx, `SELECT id, role, enabled, is_owner FROM users WHERE id = ANY($1) ORDER BY id FOR UPDATE`, []int{fromID, toID})
	if err != nil {
		return err
	}
	accounts := map[int]models.User{}
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Role, &u.Enabled, &u.IsOwner); err != nil {
			rows.Close()
			return err
		}
		accounts[u.ID] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	from, ok := accounts[fromID]
	if !ok || !from.IsOwner {
		return ErrNotOwner
	}
	to, ok := accounts[toID]
	if !ok {
		return ErrNotFound
	}
	if to.Role != models.RoleAdmin || !to.Enabled {
		return ErrOwnershipTarget
	}
	if err := moveOwnership(ctx, tx, fromID, toID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// moveOwnership clears the current Owner before marking the next: the
// single-Owner index is checked row by row. The new Owner becomes a
// break-glass account, so the one account that must never be locked out
// keeps password sign-in by default; it may clear the flag itself. The
// previous Owner keeps whatever flag it had, which never leaves the server
// with fewer break-glass accounts. It also ends every session in
// which someone views the server as the new Owner, and the previous Owner's
// sessions viewing as other admins, and deletes the new Owner's API keys and
// reset link, and revokes pending admin invitations: nobody may act as the
// Owner, only the Owner may act as another admin, and nothing issued before
// the move may carry the Owner's authority.
func moveOwnership(ctx context.Context, tx pgx.Tx, fromID, toID int) error {
	if fromID > 0 {
		if _, err := tx.Exec(ctx, `UPDATE users SET is_owner = false WHERE id = $1`, fromID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET is_owner = true, break_glass = true WHERE id = $1`, toID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE auth_sessions SET revoked_at = NOW()
		WHERE user_id = $1 AND impersonator_user_id IS NOT NULL AND revoked_at IS NULL`, toID); err != nil {
		return fmt.Errorf("ending sessions viewing as the owner: %w", err)
	}
	// Only the Owner may view as another admin, so the previous Owner's
	// sessions viewing as an admin end with its ownership.
	if _, err := tx.Exec(ctx, `
		UPDATE auth_sessions s SET revoked_at = NOW() FROM users t
		WHERE s.impersonator_user_id = $1 AND s.user_id = t.id AND t.role = 'admin' AND s.revoked_at IS NULL`, fromID); err != nil {
		return fmt.Errorf("ending the previous owner's sessions viewing as admins: %w", err)
	}
	// Pending admin invitations were issued under the previous ownership; a
	// kept link must not let a former Owner grant the admin role. The new
	// Owner resends any it still wants.
	if _, err := tx.Exec(ctx, `
		UPDATE invitations SET revoked_at = clock_timestamp(), updated_at = clock_timestamp()
		WHERE role = 'admin' AND accepted_at IS NULL AND revoked_at IS NULL`); err != nil {
		return fmt.Errorf("revoking pending admin invitations: %w", err)
	}
	// The previous Owner may have created keys and a reset link on the
	// account before handing it over; none of them may carry Owner authority.
	return revokeCredentialsIssuedToNonAdmin(ctx, tx, toID)
}

// SetOwner makes the account username the Owner, for recovery from the
// server's command line when the Owner is lost or locked out. It promotes
// and enables the account when it is not already an enabled admin, and
// returns the account and the previous Owner's id (0 when there was none).
func (r *UserRepository) SetOwner(ctx context.Context, username string) (*models.User, int, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Lock the named account and the current Owner in one statement, in id
	// order, so concurrent recoveries serialize instead of deadlocking.
	rows, err := tx.Query(ctx, `
		SELECT id, username = $1, is_owner, role, enabled FROM users
		WHERE username = $1 OR is_owner ORDER BY id FOR UPDATE`, NormalizeUsername(strings.TrimSpace(username)))
	if err != nil {
		return nil, 0, err
	}
	id, previous := 0, 0
	var target models.User
	for rows.Next() {
		var u models.User
		var named bool
		if err := rows.Scan(&u.ID, &named, &u.IsOwner, &u.Role, &u.Enabled); err != nil {
			rows.Close()
			return nil, 0, err
		}
		if named {
			id, target = u.ID, u
		}
		if u.IsOwner {
			previous = u.ID
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if id == 0 {
		return nil, 0, ErrNotFound
	}
	if previous != id {
		if target.Role != models.RoleAdmin || !target.Enabled {
			// Recovery also signs the account out everywhere, so every
			// session starts again under its new authority.
			if err := updateUser(ctx, tx, id, models.UpdateUserInput{Role: new(models.RoleAdmin), Enabled: new(true)}); err != nil {
				return nil, 0, fmt.Errorf("promoting account: %w", err)
			}
			if err := RevokeSignInsInTransaction(ctx, tx, id); err != nil {
				return nil, 0, err
			}
		}
		if err := moveOwnership(ctx, tx, previous, id); err != nil {
			return nil, 0, err
		}
	}
	user, err := userByID(ctx, tx, id)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return user, previous, nil
}
