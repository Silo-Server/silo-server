package handlers

import (
	"context"
	"log/slog"

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
// leaves keys on): every login session a key opened ends in the write's own
// transaction, which holds the settings lock, so the write and the sweep
// commit together or not at all, and a key path waiting on the lock reads
// the switch off when it gets it. before and after are the effective values.
// Turning the switch on needs nothing: keys were never revoked.
func (h *AdminHandler) checkAppKeySetting(ctx context.Context, tx pgx.Tx, before, after map[string]string) error {
	if !appKeySwitch(before) || appKeySwitch(after) {
		return nil
	}
	if tx == nil {
		// The sweep must share the write's transaction; only a settings
		// store without one (a test fake) gets here.
		return ErrAdminSettingsUnavailable
	}
	ended, err := auth.EndAppKeySessionsTx(ctx, tx)
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "app key switch turned off; ending key sessions", "component", "auth", "sessions", ended)
	return nil
}
