package sections

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func setHideWatchedItemsFromHome(t *testing.T, store userstore.UserStore, profileID string, enabled bool) {
	t.Helper()
	value, err := json.Marshal(enabled)
	if err != nil {
		t.Fatalf("marshal preference: %v", err)
	}
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.HomeHideWatchedItems,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	}, value); err != nil {
		t.Fatalf("upsert Home preference: %v", err)
	}
}

func TestHideWatchedItemsFromHomeDefaultsOff(t *testing.T) {
	store := newNextUpModeTestStore(t)
	if HideWatchedItemsFromHome(context.Background(), store, "profile-1") {
		t.Fatal("HideWatchedItemsFromHome = true, want false by default")
	}
}

func TestHideWatchedItemsFromHomeIsProfileScoped(t *testing.T) {
	ctx := context.Background()
	store := newNextUpModeTestStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create second profile: %v", err)
	}
	setHideWatchedItemsFromHome(t, store, "profile-1", true)

	if !HideWatchedItemsFromHome(ctx, store, "profile-1") {
		t.Error("HideWatchedItemsFromHome for profile-1 = false, want true")
	}
	if HideWatchedItemsFromHome(ctx, store, "profile-2") {
		t.Error("HideWatchedItemsFromHome leaked to profile-2")
	}
}

func TestPreserveWatchedItemsOnHome(t *testing.T) {
	for _, sectionType := range []SectionType{
		SectionMostWatched,
		SectionProfileActivityFeed,
		SectionForgottenFavorites,
	} {
		if !PreserveWatchedItemsOnHome(sectionType) {
			t.Errorf("PreserveWatchedItemsOnHome(%q) = false, want true", sectionType)
		}
	}
	if PreserveWatchedItemsOnHome(SectionRecentlyAdded) {
		t.Error("PreserveWatchedItemsOnHome(recently_added) = true, want false")
	}
}
