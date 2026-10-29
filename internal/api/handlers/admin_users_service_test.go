package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestProfileLastSeen keeps the latest registration per profile across
// devices and skips blank profiles and unparsable timestamps.
func TestProfileLastSeen(t *testing.T) {
	got := profileLastSeen([]userstore.DeviceEntry{
		{ProfileID: "p1", DeviceID: "tv", LastSeenAt: "2026-09-28T10:00:00Z"},
		{ProfileID: "p1", DeviceID: "phone", LastSeenAt: "2026-09-29T10:00:00.5Z"},
		{ProfileID: " p1 ", DeviceID: "tablet", LastSeenAt: "2026-09-29T10:00:00Z"},
		{ProfileID: "p2", DeviceID: "tv", LastSeenAt: "2026-09-27T08:00:00+02:00"},
		{ProfileID: "p3", DeviceID: "tv", LastSeenAt: "not a time"},
		{ProfileID: "", DeviceID: "tv", LastSeenAt: "2026-09-30T00:00:00Z"},
	})
	want := map[string]time.Time{
		"p1": time.Date(2026, 9, 29, 10, 0, 0, 500_000_000, time.UTC),
		"p2": time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC),
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for profile, at := range want {
		if !got[profile].Equal(at) {
			t.Errorf("%s: %v, want %v", profile, got[profile], at)
		}
	}
}

// TestAdminAccountDeviceAndProfileLastSeen reads one account's devices and
// profiles from a real store: a registered device carries its last-seen time
// and the profile's, a device known only from saved settings carries none,
// and the profile list reports the profile's latest registration and avatar.
func TestAdminAccountDeviceAndProfileLastSeen(t *testing.T) {
	ctx := context.Background()
	store := newIsolatedProfileTestStore(t, "account")
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Guest", Avatar: "preset:avatar-3"}); err != nil {
		t.Fatal(err)
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("store does not support device registry")
	}
	if err := registry.RegisterDevice(ctx, userstore.DeviceEntry{ProfileID: "profile-1", DeviceID: "tv", DeviceName: "Apple TV", DevicePlatform: "tvOS"}); err != nil {
		t.Fatal(err)
	}
	// A canonical per-device value does not register the device, so this
	// device is known only from saved settings.
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfileDevice, ProfileID: "profile-1", DeviceID: "settings-only"}, json.RawMessage(`"always"`)); err != nil {
		t.Fatal(err)
	}
	h := &AdminHandler{
		userRepo:  testAdminUserRepo{users: map[int]*models.User{7: {ID: 7, Username: "alice"}}},
		storeProv: mappedTestUserStoreProvider{stores: map[int]userstore.UserStore{7: store}},
	}

	views, err := h.ReadAdminUserDevices(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AdminUserDeviceView{}
	for _, v := range views {
		byID[v.DeviceID] = v
	}
	tv, phone := byID["tv"], byID["settings-only"]
	if len(views) != 2 || tv.LastSeenAt == "" || tv.ProfileLastSeenAt["profile-1"] != tv.LastSeenAt || tv.DeviceName != "Apple TV" {
		t.Fatalf("registered device: %+v", views)
	}
	if phone.LastSeenAt != "" || len(phone.ProfileLastSeenAt) != 0 || phone.OverrideCount != 1 {
		t.Fatalf("settings-only device: %+v", phone)
	}
	if _, err := h.ReadAdminUserDevices(ctx, 99); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("missing account: %v", err)
	}

	profiles, err := h.ListAdminAccountProfiles(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	byProfile := map[string]AdminProfileView{}
	for _, p := range profiles {
		byProfile[p.ID] = p
	}
	parent, guest := byProfile["profile-1"], byProfile["profile-2"]
	if len(profiles) != 2 || parent.LastSeenAt == nil || parent.LastSeenAt.UTC().Format(time.RFC3339Nano) != tv.LastSeenAt || guest.LastSeenAt != nil {
		t.Fatalf("profile last seen: %+v", profiles)
	}
	if guest.Avatar != "preset:avatar-3" || guest.AvatarSource != "preset" || guest.AvatarURL != "/profile-avatars/avatar-3.svg" || parent.AvatarSource != "none" || parent.AvatarURL != "" {
		t.Fatalf("profile avatars: %+v", profiles)
	}
}
