package sections

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// legacySettingNextUpMode is the legacy account-wide user-settings key that
// stored the next-up presentation mode. It is read only as a fallback now: the
// setting moved to the profile-scoped canonical key ui.next_up_mode, and the
// legacy write endpoint no longer accepts this key.
const legacySettingNextUpMode = "next_up_mode"

// NextUpModeCombined keeps next-up episodes inside Continue Watching;
// NextUpModeSeparate gives them their own row. Combined is the contract
// default and what an absent value has always meant.
const (
	NextUpModeCombined = "combined"
	NextUpModeSeparate = "separate"
)

// NextUpMode resolves how the acting profile wants next-up episodes presented:
// the canonical profile-scoped ui.next_up_mode row, else the legacy
// account-wide next_up_mode setting, else "combined".
//
// The canonical row is what the web writes since the settings cutover — the
// legacy endpoint rejects the unregistered key, so an account-key read alone
// would silently ignore every edit made after the cutover. The legacy fallback
// stays because the one-time backfill only ran on stores that existed when it
// shipped: a store restored from a pre-backfill snapshot still carries the
// mode only in the account key. A stored canonical row always wins, so the
// fallback can never override a post-cutover edit.
func NextUpMode(ctx context.Context, store userstore.UserStore, profileID string) string {
	if mode, ok := canonicalNextUpMode(ctx, store, profileID); ok {
		return mode
	}
	mode, _ := store.GetSetting(ctx, legacySettingNextUpMode)
	if mode == "" {
		return NextUpModeCombined
	}
	return mode
}

// canonicalNextUpMode reads the profile-scoped canonical row. The second
// return reports whether a stored row decided the answer: a resolution that
// fell through to the contract default means "nothing stored", which is what
// lets the caller consult the legacy account key.
func canonicalNextUpMode(ctx context.Context, store userstore.UserStore, profileID string) (string, bool) {
	if store == nil || profileID == "" {
		return "", false
	}
	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "next-up mode resolution degraded to the legacy setting: loading settings contract failed",
			"component", "sections", "profile_id", profileID, "error", err)
		return "", false
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{settingskeys.UiNextUpMode}, nil)
	if err != nil {
		slog.WarnContext(ctx, "next-up mode resolution degraded to the legacy setting: reading setting values failed",
			"component", "sections", "profile_id", profileID, "error", err)
		return "", false
	}
	if len(resolved) == 0 || resolved[0].Source == settingscontract.ScopeDefault {
		return "", false
	}
	var mode string
	if json.Unmarshal(resolved[0].Value, &mode) != nil || mode == "" {
		return "", false
	}
	return mode, true
}
