package storetest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

const (
	deletedProfile = "pd-deleted"
	keptProfile    = "pd-kept"
	deleteSeries   = "series:delete-check"
	deleteTour     = "core-2026-07"
	deleteDevice   = "device-delete-check"
	deleteSetting  = "player.playback_speed"
	hiddenTitle    = "movie:hidden-before"
	deleteScope    = "home"
	deletePrefsID  = "usersettings"
	deleteClient   = "emby"
)

// registeredDevice names the device registry row profileStateLeft reports.
const registeredDevice = "registered device"

// profileStateKinds is how many kinds of state seedProfileState writes.
const profileStateKinds = 9

// RunDeleteProfileRemovesProfileState checks that deleting a profile removes
// the per-profile state the store keeps beyond its lists and progress: watch
// history, hidden-history cutoffs, Home dismissals, audio and subtitle
// preferences, onboarding state, device settings and registry rows, section
// overrides and Jellyfin display preferences. Another profile's state
// stays. A profile recreated under the deleted ID starts clean, so nothing
// the old profile left behind (a hidden-history cutoff above all) reaches it.
//
// devicesPurgedElsewhere marks a store whose device registry another step
// clears: the Postgres registry shares user_devices with downloads, which the
// profile handler's device-library purge removes along with them.
func RunDeleteProfileRemovesProfileState(t *testing.T, newStore func(t *testing.T) userstore.UserStore, devicesPurgedElsewhere bool) {
	ctx := context.Background()
	store := newStore(t)
	for _, id := range []string{keptProfile, deletedProfile} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: id, Name: id}); err != nil {
			t.Fatalf("CreateProfile %s: %v", id, err)
		}
	}
	watchedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, id := range []string{keptProfile, deletedProfile} {
		seedProfileState(t, ctx, store, id, watchedAt)
	}

	if err := store.DeleteProfile(ctx, deletedProfile); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	if err := store.CreateProfile(ctx, userstore.Profile{ID: deletedProfile, Name: "Recreated"}); err != nil {
		t.Fatalf("CreateProfile(recreate): %v", err)
	}

	// The recreated profile starts clean.
	left := profileStateLeft(t, ctx, store, deletedProfile)
	if devicesPurgedElsewhere {
		left = slices.DeleteFunc(left, func(kind string) bool { return kind == registeredDevice })
	}
	if len(left) > 0 {
		t.Fatalf("deleted profile left behind: %v", left)
	}
	// History recorded before the old profile's hidden-history cutoff shows.
	entry := userstore.WatchHistoryEntry{ID: "pd-after", ProfileID: deletedProfile, MediaItemID: hiddenTitle, WatchedAt: watchedAt.Format(time.RFC3339), Completed: true}
	if err := store.AddHistory(ctx, entry); err != nil {
		t.Fatalf("AddHistory(recreated): %v", err)
	}
	if rows, err := store.ListHistory(ctx, deletedProfile, 10, 0); err != nil || len(rows) != 1 {
		t.Fatalf("recreated profile history = %d rows, %v; want the new row, not hidden by the old profile's cutoff", len(rows), err)
	}

	// The other profile keeps everything.
	if got := profileStateLeft(t, ctx, store, keptProfile); len(got) != profileStateKinds {
		t.Fatalf("other profile kept %v, want all %d kinds of state", got, profileStateKinds)
	}
}

// seedProfileState gives profileID one row of each kind of state, with
// movie:hidden-before hidden from its history.
func seedProfileState(t *testing.T, ctx context.Context, store userstore.UserStore, profileID string, watchedAt time.Time) {
	t.Helper()
	at := watchedAt.Format(time.RFC3339)
	for _, entry := range []userstore.WatchHistoryEntry{
		{ID: profileID + "-h1", ProfileID: profileID, MediaItemID: "movie:history", WatchedAt: at, Completed: true},
		{ID: profileID + "-h2", ProfileID: profileID, MediaItemID: hiddenTitle, WatchedAt: at, Completed: true},
	} {
		if err := store.AddHistory(ctx, entry); err != nil {
			t.Fatalf("AddHistory %s: %v", profileID, err)
		}
	}
	if err := store.RemoveHistoryItems(ctx, profileID, []string{hiddenTitle}, watchedAt.Add(time.Hour)); err != nil {
		t.Fatalf("RemoveHistoryItems %s: %v", profileID, err)
	}
	progressAt := at
	if err := store.UpsertHomeDismissal(ctx, userstore.HomeItemDismissal{
		ProfileID: profileID, Surface: userstore.HomeSurfaceContinueWatching, MediaItemID: "movie:dismissed",
		ProgressUpdatedAt: &progressAt, DismissedAt: at,
	}); err != nil {
		t.Fatalf("UpsertHomeDismissal %s: %v", profileID, err)
	}
	if err := store.SetAudioPreference(ctx, userstore.AudioPreference{ProfileID: profileID, SeriesID: deleteSeries, AudioTrackIndex: 1, AudioLanguage: "en"}); err != nil {
		t.Fatalf("SetAudioPreference %s: %v", profileID, err)
	}
	if err := store.SetSubtitlePreference(ctx, userstore.SubtitlePreference{ProfileID: profileID, SeriesID: deleteSeries, SubtitleTrackIndex: 2, SubtitleLanguage: "en", SubtitleMode: "on"}); err != nil {
		t.Fatalf("SetSubtitlePreference %s: %v", profileID, err)
	}
	if err := store.UpsertOnboardingState(ctx, userstore.OnboardingState{ProfileID: profileID, TourID: deleteTour, LastStep: "subtitles"}); err != nil {
		t.Fatalf("UpsertOnboardingState %s: %v", profileID, err)
	}
	if err := store.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{ProfileID: profileID, DeviceID: deleteDevice, Key: deleteSetting, Value: "1.25"}); err != nil {
		t.Fatalf("SetDeviceSetting %s: %v", profileID, err)
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("the store keeps no device registry")
	}
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: profileID, DeviceID: deleteDevice, DeviceName: "Phone", DevicePlatform: "iOS", LastSeenAt: at}); err != nil {
		t.Fatalf("RegisterDevice %s: %v", profileID, err)
	}
	if err := store.SaveSectionOverrides(ctx, profileID, deleteScope, "", []userstore.SectionOverride{{SectionID: "default-recently-added", Hidden: true}}); err != nil {
		t.Fatalf("SaveSectionOverrides %s: %v", profileID, err)
	}
	if err := store.SetJellycompatDisplayPrefs(ctx, userstore.ProfileDisplayPrefsID(profileID, deletePrefsID), deleteClient, `{"SortBy":"SortName"}`); err != nil {
		t.Fatalf("SetJellycompatDisplayPrefs %s: %v", profileID, err)
	}
}

// profileStateLeft names each kind of seeded state profileID still has.
func profileStateLeft(t *testing.T, ctx context.Context, store userstore.UserStore, profileID string) []string {
	t.Helper()
	var left []string
	if rows, err := store.ListHistory(ctx, profileID, 10, 0); err != nil {
		t.Fatalf("ListHistory %s: %v", profileID, err)
	} else if len(rows) > 0 {
		left = append(left, "watch history")
	}
	if rows, err := store.ListHomeDismissals(ctx, profileID, userstore.HomeSurfaceContinueWatching); err != nil {
		t.Fatalf("ListHomeDismissals %s: %v", profileID, err)
	} else if len(rows) > 0 {
		left = append(left, "home dismissals")
	}
	if pref, err := store.GetAudioPreference(ctx, profileID, deleteSeries); err != nil {
		t.Fatalf("GetAudioPreference %s: %v", profileID, err)
	} else if pref != nil {
		left = append(left, "audio preference")
	}
	if pref, err := store.GetSubtitlePreference(ctx, profileID, deleteSeries); err != nil {
		t.Fatalf("GetSubtitlePreference %s: %v", profileID, err)
	} else if pref != nil {
		left = append(left, "subtitle preference")
	}
	if state, err := store.GetOnboardingState(ctx, profileID, deleteTour); err != nil {
		t.Fatalf("GetOnboardingState %s: %v", profileID, err)
	} else if state != nil {
		left = append(left, "onboarding state")
	}
	if setting, err := store.GetDeviceSetting(ctx, profileID, deleteDevice, deleteSetting); err != nil {
		t.Fatalf("GetDeviceSetting %s: %v", profileID, err)
	} else if setting != nil {
		left = append(left, "device setting")
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("the store keeps no device registry")
	}
	devices, err := registry.ListDevices(ctx)
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	for _, d := range devices {
		if d.ProfileID == profileID {
			left = append(left, registeredDevice)
			break
		}
	}
	if overrides, err := store.ListSectionOverrides(ctx, profileID, deleteScope, ""); err != nil {
		t.Fatalf("ListSectionOverrides %s: %v", profileID, err)
	} else if len(overrides) > 0 {
		left = append(left, "section overrides")
	}
	if prefs, err := store.GetJellycompatDisplayPrefs(ctx, userstore.ProfileDisplayPrefsID(profileID, deletePrefsID), deleteClient); err != nil {
		t.Fatalf("GetJellycompatDisplayPrefs %s: %v", profileID, err)
	} else if prefs != "" {
		left = append(left, "display preferences")
	}
	return left
}
