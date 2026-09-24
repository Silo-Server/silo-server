package sections

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// NextUpModeCombined keeps next-up episodes inside Continue Watching;
// NextUpModeSeparate gives them their own row. Combined is the contract
// default and what an absent value has always meant.
const (
	NextUpModeCombined = "combined"
	NextUpModeSeparate = "separate"
)

// NextUpMode resolves how the acting profile wants next-up episodes presented:
// the canonical profile-scoped ui.next_up_mode row, else "combined".
//
// A request that passed viewer access already resolved the row with the rest
// of its scope, so a home-section request answers from the context without a
// store read, however many times its fetchers ask. Anything else, such as a
// background caller, resolves it here.
//
// The legacy account-wide next_up_mode setting is no longer read. The Postgres
// migration materialize_retired_settings_fallbacks and SQLite schema v26
// copied it onto every profile that had no canonical row, so each profile
// keeps the mode the fallback used to supply. A failed read degrades to
// combined, the presentation an absent value has always meant.
func NextUpMode(ctx context.Context, store userstore.UserStore, profileID string) string {
	if profileID == "" {
		return NextUpModeCombined
	}
	mode := ""
	if scope, ok := access.GetScope(ctx); ok && scope.ProfileID == profileID {
		mode = scope.NextUpMode
	}
	if mode == "" {
		mode = access.ResolveViewerPreferences(ctx, store, profileID).NextUpMode
	}
	if mode == "" {
		return NextUpModeCombined
	}
	return mode
}
