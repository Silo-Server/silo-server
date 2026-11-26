package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

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
// It works across accounts, so it can't take each account's row and
// identities first as other writers of sessions do. Its order is the
// settings lock, every plugin installation row (share, in id order), then
// the session rows (in id order), the order of a key path's installation
// and session locks. A plugin uninstall locks its installation row before
// it deletes the identities whose sessions it clears, so either the
// uninstall waits at its installation for the sweep to commit, or the sweep
// waits there, holding no session row, for the uninstall to commit.
func EndAppKeySessionsTx(ctx context.Context, tx pgx.Tx) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT id FROM plugin_installations ORDER BY id FOR SHARE`); err != nil {
		return 0, fmt.Errorf("locking plugin installations: %w", err)
	}
	tag, err := tx.Exec(ctx, `WITH ending AS (
			SELECT id FROM auth_sessions WHERE device_key_id IS NOT NULL AND revoked_at IS NULL
			ORDER BY id FOR UPDATE
		)
		UPDATE auth_sessions s SET revoked_at = NOW() FROM ending WHERE s.id = ending.id`)
	if err != nil {
		return 0, fmt.Errorf("ending app key sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
