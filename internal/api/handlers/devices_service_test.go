package handlers

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/notifications"
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
	name := "Kitchen"
	for _, update := range []DeviceUpdate{{Rename: true, CustomName: &name}, {}} {
		if err := h.UpdateDevice(ctx, actor, "", "d", update); err != nil {
			t.Fatalf("wrapped update %+v: %v", update, err)
		}
	}
	if err := h.RemoveDeviceSettings(ctx, actor, "", "d", true); err != nil {
		t.Fatal(err)
	}
}

// Renaming follows forgetting's authorization, keeps the reported name, and
// stores only visible text.
func TestUpdateDeviceRename(t *testing.T) {
	h, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "tv", "Apple TV")
	seedDevice(t, store, "profile-2", "tv", "Apple TV")
	status := func(err error) int {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return apiErr.Status
		}
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return http.StatusNoContent
	}
	rename := func(name string) DeviceUpdate { return DeviceUpdate{Rename: true, CustomName: &name} }
	names := func() map[string]string {
		t.Helper()
		page, err := store.(userstore.DeviceSettingsStore).ListDeviceSettingsPage(t.Context(), userstore.DevicePageOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, entry := range page {
			got[entry.ProfileID] = entry.DeviceName + "|" + entry.CustomName
		}
		return got
	}

	member := DeviceSettingsActor{UserID: 1, ProfileID: "profile-2"}
	ctx := devicesRequest(http.MethodPatch, "/devices/tv", "profile-2").Context()
	if got := status(h.UpdateDevice(ctx, member, "", "tv", rename(" \x00Living\tRoom\n "))); got != http.StatusNoContent {
		t.Fatalf("own rename: %d", got)
	}
	if got := names()["profile-2"]; got != "Apple TV|Living Room" {
		t.Fatalf("stored name: %q", got)
	}
	if got := status(h.UpdateDevice(ctx, member, "profile-1", "tv", rename("Den"))); got != http.StatusForbidden {
		t.Fatalf("member renaming another profile's device: %d", got)
	}
	if got := status(h.UpdateDevice(ctx, member, "", "tv", rename("\t\x00 "))); got != http.StatusBadRequest {
		t.Fatalf("blank name: %d", got)
	}
	for _, update := range []DeviceUpdate{rename("Den"), {}} {
		if got := status(h.UpdateDevice(ctx, member, "", "missing", update)); got != http.StatusNotFound {
			t.Fatalf("unknown device %+v: %d", update, got)
		}
	}
	if got := status(h.UpdateDevice(ctx, member, "", "tv", DeviceUpdate{})); got != http.StatusNoContent {
		t.Fatalf("empty update: %d", got)
	}

	primary := DeviceSettingsActor{UserID: 1, ProfileID: "profile-1"}
	ctx = devicesRequest(http.MethodPatch, "/devices/tv", "profile-1").Context()
	if got := status(h.UpdateDevice(ctx, primary, "profile-2", "tv", DeviceUpdate{Rename: true})); got != http.StatusNoContent {
		t.Fatalf("household clear: %d", got)
	}
	if got := names(); got["profile-2"] != "Apple TV|" || got["profile-1"] != "Apple TV|" {
		t.Fatalf("after clear: %v", got)
	}
}
