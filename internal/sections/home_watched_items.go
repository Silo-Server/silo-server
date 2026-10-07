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
	return profileBoolSetting(ctx, store, profileID, settingskeys.HomeHideWatchedItems)
}

// ShowAdultInFeatured resolves whether the acting profile lets adult titles
// appear in Home featured sections. Failure reads as false, so an unavailable
// preference store keeps them out, which is also the default.
func ShowAdultInFeatured(ctx context.Context, store userstore.UserStore, profileID string) bool {
	return profileBoolSetting(ctx, store, profileID, settingskeys.HomeShowAdultInFeatured)
}

// profileBoolSetting resolves one boolean setting for a profile. Any failure
// reads as false; each caller documents why false is its safe answer.
func profileBoolSetting(ctx context.Context, store userstore.UserStore, profileID, key string) bool {
	if store == nil || profileID == "" {
		return false
	}

	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "Home preference unavailable: loading settings contract failed",
			"component", "sections", "key", key, "profile_id", profileID, "error", err)
		return false
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{key}, nil)
	if err != nil {
		slog.WarnContext(ctx, "Home preference unavailable: reading setting values failed",
			"component", "sections", "key", key, "profile_id", profileID, "error", err)
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
// is section configuration rather than a section type. Continue Watching (and
// its listening and reading variants, which share the type) is exempt: a
// rewatch keeps Completed set while its resume position is active, and hiding
// it would drop the one card the viewer is resuming.
func PreserveWatchedItemsOnHome(sectionType SectionType) bool {
	switch sectionType {
	case SectionMostWatched, SectionProfileActivityFeed, SectionForgottenFavorites, SectionContinueWatching:
		return true
	default:
		return false
	}
}
