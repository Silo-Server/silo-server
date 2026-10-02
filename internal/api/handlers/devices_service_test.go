package handlers

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestDeviceSettingsServiceScope(t *testing.T) {
	h, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "shared", "one")
	seedDevice(t, store, "profile-2", "shared", "two")
	ctx := devicesRequest(http.MethodGet, "/devices", "profile-2").Context()
	actor := DeviceSettingsActor{UserID: 1, ProfileID: "profile-2"}
	page, err := h.DeviceSettingsPage(ctx, actor, false, userstore.DevicePageOptions{ProfileID: "profile-1", Limit: 10})
	if err != nil || len(page) != 1 || page[0].ProfileID != "profile-2" {
		t.Fatalf("scope override: %+v %v", page, err)
	}
	if _, err := h.DeviceSettingsPage(ctx, actor, true, userstore.DevicePageOptions{Limit: 10}); err == nil {
		t.Fatal("member household read allowed")
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "profile-1", "shared", true); err == nil {
		t.Fatal("member household mutation allowed")
	}
	actor.ProfileID = "profile-1"
	ctx = devicesRequest(http.MethodGet, "/devices", "profile-1").Context()
	if _, err := h.DeviceSettingsPage(ctx, actor, true, userstore.DevicePageOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "profile-2", "shared", true); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "profile-2", "shared", true); err == nil {
		t.Fatal("repeat forget succeeded")
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "foreign-profile", "shared", true); err == nil {
		t.Fatal("foreign profile accepted")
	}
	if err := store.UpdateProfile(ctx, "profile-1", userstore.UpdateProfileInput{PIN: new("1234")}); err != nil {
		t.Fatal(err)
	}
	actor.VerifyProfile = func(string) error { return access.ErrProfileUnverified }
	if _, err := h.DeviceSettingsPage(ctx, actor, true, userstore.DevicePageOptions{Limit: 10}); err == nil {
		t.Fatal("unverified primary allowed")
	}
	actor.VerifyProfile = func(string) error { return nil }
	if _, err := h.DeviceSettingsPage(ctx, actor, true, userstore.DevicePageOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceSettingsLogicalCounts(t *testing.T) {
	h, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "d", "device")
	seedDeviceValue(t, store, "profile-1", "d", settingskeys.PlaybackIntroSkipMode, `"never"`)
	seedDeviceValue(t, store, "profile-1", "d", settingskeys.PlaybackAutoSkipIntro, `false`)
	ctx := devicesRequest(http.MethodGet, "/devices", "profile-1").Context()
	page, err := h.DeviceSettingsPage(ctx, DeviceSettingsActor{UserID: 1, ProfileID: "profile-1"}, false, userstore.DevicePageOptions{Limit: 10})
	if err != nil || len(page) != 1 || page[0].ChangedCount != 1 {
		t.Fatalf("logical aliases: %+v %v", page, err)
	}
}

func TestDeviceSettingsProductionWrapper(t *testing.T) {
	_, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "d", "device")
	provider := notifications.WrapUserStoreProvider(testUserStoreProvider{store: store}, &notifications.System{})
	h := NewDeviceHandler(provider)
	actor := DeviceSettingsActor{UserID: 1, ProfileID: "profile-1"}
	ctx := devicesRequest(http.MethodGet, "/devices", "profile-1").Context()
	page, err := h.DeviceSettingsPage(ctx, actor, false, userstore.DevicePageOptions{Limit: 10})
	if err != nil || len(page) != 1 {
		t.Fatalf("wrapped list: %+v %v", page, err)
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "", "d", false); err != nil {
		t.Fatal(err)
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "", "d", true); err != nil {
		t.Fatal(err)
	}
}
