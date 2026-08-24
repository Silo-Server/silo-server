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

// HideWatchedItemsFromHome resolves the acting profile's Home preference.
// Failure is fail-open: an unavailable preference store must not make media
// disappear from Home unexpectedly.
func HideWatchedItemsFromHome(ctx context.Context, store userstore.UserStore, profileID string) bool {
	if store == nil || profileID == "" {
		return false
	}

	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "hide-watched Home preference unavailable: loading settings contract failed",
			"component", "sections", "profile_id", profileID, "error", err)
		return false
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{settingskeys.HomeHideWatchedItems}, nil)
	if err != nil {
		slog.WarnContext(ctx, "hide-watched Home preference unavailable: reading setting values failed",
			"component", "sections", "profile_id", profileID, "error", err)
		return false
	}
	if len(resolved) != 1 {
		return false
	}

	var enabled bool
	if err := json.Unmarshal(resolved[0].Value, &enabled); err != nil {
		return false
	}
	return enabled
}

// PreserveWatchedItemsOnHome identifies sections whose meaning depends on
// watched history. Featured sections are handled separately because Featured
// is section configuration rather than a section type.
func PreserveWatchedItemsOnHome(sectionType SectionType) bool {
	switch sectionType {
	case SectionMostWatched, SectionProfileActivityFeed, SectionForgottenFavorites:
		return true
	default:
		return false
	}
}
