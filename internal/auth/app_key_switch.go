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
//     commit, or this waits there, holding no request or session row.
//   - the device sign-in requests those sessions pre-approved (in id order),
//     the rows the auth_sessions_withdraw_preapprovals trigger writes when a
//     session ends. Ending all of an account's sessions
//     (revokeSignInsInTransaction) withdraws its approvals before it ends its
//     sessions, so both take requests before sessions.
//   - the sessions (in id order), whose revocation fires that trigger.
func endKeySessionsTx(ctx context.Context, tx pgx.Tx, only []string) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT id FROM plugin_installations ORDER BY id FOR SHARE`); err != nil {
		return 0, fmt.Errorf("locking plugin installations: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT r.id FROM device_login_requests r
		JOIN auth_sessions s ON s.id = r.preapproved_session_id
		WHERE s.device_key_id IS NOT NULL AND s.revoked_at IS NULL AND ($1::text[] IS NULL OR s.id = ANY($1))
			AND r.preapproved_at IS NOT NULL AND r.status = 'approved'
		ORDER BY r.id FOR UPDATE OF r`, only); err != nil {
		return 0, fmt.Errorf("locking pre-approved device sign-ins: %w", err)
	}
	tag, err := tx.Exec(ctx, `WITH ending AS (
			SELECT id FROM auth_sessions
			WHERE device_key_id IS NOT NULL AND revoked_at IS NULL AND ($1::text[] IS NULL OR id = ANY($1))
			ORDER BY id FOR UPDATE
		)
		UPDATE auth_sessions s SET revoked_at = NOW() FROM ending WHERE s.id = ending.id`, only)
	if err != nil {
		return 0, fmt.Errorf("ending app key sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// endKeySessionIfSwitchOff ends one key session when the app key switch is
// off, and reports whether it did. Renewal calls it for a key session, so a
// session the sweep missed (a database written or put back by hand) ends
// at its next refresh like any revoked session, and its access token is
// refused from then on, rather than pausing until the switch comes back.
// It reads the switch under the settings lock (shared) and ends the session
// in endKeySessionsTx's order.
func endKeySessionIfSwitchOff(ctx context.Context, pool *pgxpool.Pool, sessionID string) (bool, error) {
	ended := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
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
