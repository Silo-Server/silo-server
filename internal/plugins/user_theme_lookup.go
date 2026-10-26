package plugins

import "context"

// SiloWebTheme is the id of the one theme the Silo web client paints. Admin
// accent, token and CSS overrides layer on top of it; there is no per-user or
// per-profile theme choice.
const SiloWebTheme = "midnight-cinema"

// FixedUserThemeLookup answers every lookup with SiloWebTheme, so the plugin
// proxy stamps the same X-Silo-Theme the host UI paints.
//
// It replaced a lookup that read each profile's ui.theme setting. That setting
// is retired and its stored rows are deleted, and the old lookup's fallback to
// the legacy account-level user_settings.ui_theme row would otherwise hand
// plugins a theme the host no longer shows.
type FixedUserThemeLookup struct{}

func (FixedUserThemeLookup) LookupUITheme(context.Context, int, string) (string, error) {
	return SiloWebTheme, nil
}
