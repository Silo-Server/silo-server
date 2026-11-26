package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/demomode"
)

// appKeySwitch is the app key switch in effect for settings values: the
// auth.app_key_sign_in setting on and demo mode off (auth.AppKeySwitchOn).
func appKeySwitch(values map[string]string) bool {
	return auth.AppKeySwitchOn(values[config.AuthAppKeySignInSettingKey], values[demomode.SettingKey])
}

// checkAppKeySetting applies a settings write that turns the app key switch
// off (auth.app_key_sign_in going off, or demo mode going on, while the other
// leaves keys on). It refuses while an account would be left with no way in
// (auth.CheckAppKeysOff, which names them). Otherwise every login session a
// key opened ends in the write's own transaction, which holds the settings
// lock, so the write and the sweep commit together or not at all, and a key
// path waiting on the lock reads the switch off when it gets it. before and
// after are the effective values. Turning the switch on needs nothing: keys
// were never revoked. While the Owner holds a live key session, only the
// Owner may turn it off (auth.CheckSwitchOffActor, owner_protected); actorID
// is the account making the write.
func (h *AdminHandler) checkAppKeySetting(ctx context.Context, tx pgx.Tx, actorID int, before, after map[string]string) error {
	if !appKeySwitch(before) || appKeySwitch(after) {
		return nil
	}
	if tx == nil {
		// The sweep must share the write's transaction; only a settings
		// store without one (a test fake) gets here.
		return ErrAdminSettingsUnavailable
	}
	if err := auth.CheckSwitchOffActor(ctx, tx, actorID); err != nil {
		if errors.Is(err, auth.ErrOwnershipChanging) {
			return &APIError{Status: http.StatusConflict, Code: codeConflict, Message: "The server's ownership is changing. Save again."}
		}
		return ownerError(err)
	}
	if err := auth.CheckAppKeysOff(ctx, tx, signInSettingsOf(after)); err != nil {
		return breakGlassError(err)
	}
	ended, err := auth.EndAppKeySessionsTx(ctx, tx)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "app key switch turned off; ending key sessions", "component", "auth", "sessions", ended)
	return nil
}

// AccountsAtRisk lists the accounts change would leave with no way in, for
// the owner's settings page (auth.AccountsAtRisk).
func (h *AdminHandler) AccountsAtRisk(ctx context.Context, change auth.SignInChange) ([]auth.AccountAtRisk, error) {
	if h.pool == nil {
		return nil, ErrAdminSettingsUnavailable
	}
	return auth.AccountsAtRisk(ctx, h.pool, change)
}

// signInSettingsOf is the settings the "no way in" checks read, from
// effective setting values.
func signInSettingsOf(values map[string]string) auth.SignInSettings {
	return auth.SignInSettingsOf(values[config.AuthLocalPasswordLoginSettingKey], values[config.ServerPublicURLSettingKey])
}

// checkPublicURLSetting refuses a settings write that changes
// server.public_url while an account would lose the only provider it can
// sign in with (an OAuth provider needs the address) and have no password
// that signs in (auth.CheckPublicURLChange, which names them).
func (h *AdminHandler) checkPublicURLSetting(ctx context.Context, tx pgx.Tx, before, after map[string]string) error {
	if before[config.ServerPublicURLSettingKey] == after[config.ServerPublicURLSettingKey] {
		return nil
	}
	if tx == nil {
		return ErrAdminSettingsUnavailable
	}
	return breakGlassError(auth.CheckPublicURLChange(ctx, tx, signInSettingsOf(before), signInSettingsOf(after)))
}

// touchesAppKeySwitch reports whether a write of keys could turn the app key
// switch off: it names auth.app_key_sign_in or demo.enabled.
func touchesAppKeySwitch(keys ...string) bool {
	for _, key := range keys {
		if key == config.AuthAppKeySignInSettingKey || key == demomode.SettingKey {
			return true
		}
	}
	return false
}

// inSwitchWriteTx runs write in a transaction that takes the ownership lock
// (shared) before anything else, when locking is set, so the settings lock the
// write takes next, and the Owner check under it (auth.CheckSwitchOffActor),
// come after it: the orders are ownership then settings (this write),
// ownership then account rows (an ownership transfer), and account row then
// settings (key and password sign-in), and no path takes the settings lock
// or an account row before the ownership lock. Otherwise, or with no
// database, write runs with no transaction of its own (nil), as before.
func (h *AdminHandler) inSwitchWriteTx(ctx context.Context, locking bool, write func(tx pgx.Tx) error) error {
	if !locking || h.pool == nil {
		return write(nil)
	}
	return pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		if err := auth.LockOwnershipShared(ctx, tx); err != nil {
			return err
		}
		return write(tx)
	})
}
