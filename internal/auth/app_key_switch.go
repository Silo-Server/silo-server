package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
)

// ErrAppKeySignInDisabled refuses a key path that found the switch off under
// the settings lock: auth.app_key_sign_in is off, or the server is in demo
// mode. The routes answer it as they answer the switch read before the
// service runs.
var ErrAppKeySignInDisabled = errors.New("app key sign-in is turned off on this server")

// AppKeySwitchOn is the switch as one fact: auth.app_key_sign_in on (a
// value that doesn't parse is the default, on) and demo mode off (only the
// stored value demomode.OnValue, untrimmed, turns it on). The API layer reads
// the same two settings (appKeySwitch in internal/apiv2); every reader must
// agree with it.
func AppKeySwitchOn(appKeySignIn, demoEnabled string) bool {
	return settingBool(appKeySignIn, true) && demoEnabled != demomode.OnValue
}

// appKeySwitchOn reads the switch from server_settings in one query.
func appKeySwitchOn(ctx context.Context, db rowQuerier) (bool, error) {
	var setting, demo string
	err := db.QueryRow(ctx, `SELECT
			COALESCE((SELECT value FROM server_settings WHERE key = $1), ''),
			COALESCE((SELECT value FROM server_settings WHERE key = $2), '')`,
		config.AuthAppKeySignInSettingKey, demomode.SettingKey).Scan(&setting, &demo)
	if err != nil {
		return false, fmt.Errorf("reading the app key switch: %w", err)
	}
	return AppKeySwitchOn(strings.TrimSpace(setting), demo), nil
}

// lockSettingsShared takes the settings lock in shared mode for the rest of
// tx. Key paths take it shared: they only read the settings under it, and no
// key path asks for it in exclusive mode later in the same transaction (an
// app code's join takes it exclusive before, which a shared request in the
// same transaction then holds already). Every writer of server_settings takes
// it exclusive, so a key path and a settings write still run one after the
// other.
func lockSettingsShared(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1, 0))`, config.ServerSettingsMutationLock); err != nil {
		return fmt.Errorf("acquiring settings lock: %w", err)
	}
	return nil
}

// lockAppKeySwitch takes the settings lock (shared) and refuses with
// ErrAppKeySignInDisabled while the switch is off. Every key path that opens
// a session, enrolls a key or verifies a signed profile set calls it through
// lockChain, after the account row lock. A switch-off commits either before
// (the path reads off and is refused) or after (it waits for the path to
// commit, and then ends the session the path opened: EndAppKeySessionsTx).
func lockAppKeySwitch(ctx context.Context, tx pgx.Tx) error {
	if err := lockSettingsShared(ctx, tx); err != nil {
		return err
	}
	on, err := appKeySwitchOn(ctx, tx)
	if err != nil {
		return err
	}
	if !on {
		return ErrAppKeySignInDisabled
	}
	return nil
}

// EndAppKeySessionsTx ends every live login session a key opened, on every
// account: those of the person's own device keys and those of apps enrolled
// with a grant (both carry device_key_id). Sessions opened by a password, a
// provider or device sign-in are untouched, and so are the keys: turning the
// switch back on lets each device sign in again. The settings write that
// turns the switch off calls it in its own transaction, which holds the
// settings lock in exclusive mode, so the sweep and the write commit
// together. It answers how many sessions it ended.
//
// It runs endKeySessionsTx over every key session.
func EndAppKeySessionsTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	return endKeySessionsTx(ctx, tx, nil)
}

// endKeySessionsTx ends the live key sessions (device_key_id set) of the
// session IDs in only, or every one when only is nil. It works across
// accounts, so it can't take each account's row and identities first as
// other writers of sessions do. Its order, after the settings lock the
// caller holds:
//
//   - every plugin installation row (share, in id order). A plugin uninstall
//     locks its installation row before it deletes the identities whose
//     sessions it clears, so either the uninstall waits there for this to
//     commit, or this waits there, holding no session row.
//   - the sessions, in id order (lockSessionsInOrder), as every path that
//     ends several sessions takes them. Ending a session writes only its
//     row: a device sign-in it approved in advance stops being honored
//     because collection reads the session's state (approverLive).
func endKeySessionsTx(ctx context.Context, tx pgx.Tx, only []string) (int64, error) {
	// Callers take these before the settings lock (lockInstallationsShared);
	// taking them again here changes nothing for them.
	if err := lockInstallationsShared(ctx, tx); err != nil {
		return 0, err
	}
	const ending = `device_key_id IS NOT NULL AND revoked_at IS NULL AND ($1::text[] IS NULL OR id = ANY($1))`
	if err := lockSessionsInOrder(ctx, tx, ending, only); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at = NOW() WHERE `+ending, only)
	if err != nil {
		return 0, fmt.Errorf("ending app key sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// lockInstallationsShared takes every plugin installation row (share, in id
// order). A path that both ends key sessions and holds the settings lock
// takes them first, before the settings lock: a plugin write that guards
// sign-in (plugins.signInGuard) takes its installation row and then the
// settings lock, so the settings lock is never held while waiting for an
// installation row.
func lockInstallationsShared(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM plugin_installations ORDER BY id FOR SHARE`); err != nil {
		return fmt.Errorf("locking plugin installations: %w", err)
	}
	return nil
}

// LockSwitchWrite takes, at the start of a settings write that could turn the
// app key switch off, the locks that must come before the settings lock: the
// ownership lock (shared; see lockOwnership) and every plugin installation
// row (share; see lockInstallationsShared), which the sweep needs.
func LockSwitchWrite(ctx context.Context, tx pgx.Tx) error {
	if err := LockOwnershipShared(ctx, tx); err != nil {
		return err
	}
	return lockInstallationsShared(ctx, tx)
}

// afterRenewalSwitchRead, when set, runs between a key session renewal's
// plain read of the switch and the rest of it. Tests set it to place a
// switch-off in that gap; it is nil otherwise.
var afterRenewalSwitchRead func()

// endKeySessionIfSwitchOff ends one key session when the app key switch is
// off, and reports whether it did. Renewal calls it for a key session, so a
// session the sweep missed (a database written or put back by hand) ends
// at its next refresh like any revoked session, and its access token is
// refused from then on, rather than pausing until the switch comes back.
// It reads the switch once without a lock; only when that read says off
// does it take the installation rows and the settings lock (shared), read
// again and end the session in endKeySessionsTx's order. Skipping the lock while the switch is on is safe:
// a switch-off that commits just after the plain read runs its own sweep,
// which ends this session in the same transaction as the write, so tokens
// this renewal goes on to mint name a revoked session and fail on first use.
func endKeySessionIfSwitchOff(ctx context.Context, pool *pgxpool.Pool, sessionID string) (bool, error) {
	on, err := appKeySwitchOn(ctx, pool)
	if afterRenewalSwitchRead != nil {
		afterRenewalSwitchRead()
	}
	if err != nil || on {
		return false, err
	}
	ended := false
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if err := lockInstallationsShared(ctx, tx); err != nil {
			return err
		}
		if err := lockSettingsShared(ctx, tx); err != nil {
			return err
		}
		on, err := appKeySwitchOn(ctx, tx)
		if err != nil || on {
			return err
		}
		ended = true
		_, err = endKeySessionsTx(ctx, tx, []string{sessionID})
		return err
	})
	return ended, err
}

// ErrOwnershipChanging refuses a switch-off whose transaction could not take
// the ownership lock because ownership is moving at that moment; the write
// can be made again.
var ErrOwnershipChanging = errors.New("the server's ownership is changing; try again")

// CheckSwitchOffActor refuses, with ErrOwnerProtected, turning the app key
// switch off by anyone but the Owner while the Owner holds a live session a
// key opened: the sweep would end the Owner's sessions, which only the Owner
// may do (CheckOwnerTarget). actorID is the account making the settings
// write (0 for none, which is not the Owner). The settings write calls it in
// its transaction, which holds the settings lock in exclusive mode, before
// the sweep: every path that opens a key session takes that lock (shared,
// lockChain) before it inserts the session, so the Owner can't gain one
// between this check and the sweep.
func CheckSwitchOffActor(ctx context.Context, tx pgx.Tx, actorID int) error {
	// Ownership can't move between this read and the sweep: its writers
	// take the ownership lock exclusively. The settings write has taken it
	// (shared) before the settings lock, so this try succeeds at once. A
	// write that brought its own transaction without it (a restore) only
	// tries, never waits here while holding the settings lock, and is refused
	// with ErrOwnershipChanging while a transfer holds it.
	var held bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock_shared(hashtextextended($1, 0))`, ownershipLock).Scan(&held); err != nil {
		return fmt.Errorf("locking ownership: %w", err)
	}
	if !held {
		return ErrOwnershipChanging
	}
	var refused bool
	if err := tx.QueryRow(ctx, `SELECT NOT COALESCE((SELECT is_owner FROM users WHERE id = $1), false)
		AND EXISTS (SELECT 1 FROM auth_sessions s JOIN users u ON u.id = s.user_id
			WHERE u.is_owner AND s.device_key_id IS NOT NULL AND s.revoked_at IS NULL AND s.expires_at > NOW())`,
		actorID).Scan(&refused); err != nil {
		return fmt.Errorf("checking the owner's key sessions: %w", err)
	}
	if refused {
		return ErrOwnerProtected
	}
	return nil
}
