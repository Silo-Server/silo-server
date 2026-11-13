package handlers

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// registeredDevice returns the registry row for (profile, device), or nil.
func registeredDevice(t *testing.T, store userstore.UserStore, profileID, deviceID string) *userstore.DeviceEntry {
	t.Helper()
	devices, err := store.(userstore.DeviceRegistry).ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	for i := range devices {
		if devices[i].ProfileID == profileID && devices[i].DeviceID == deviceID {
			return &devices[i]
		}
	}
	return nil
}

func requireRegisteredDevice(t *testing.T, store userstore.UserStore, profileID, deviceID, name, platform string) {
	t.Helper()
	got := registeredDevice(t, store, profileID, deviceID)
	if got == nil {
		t.Fatalf("device %s is not registered for %s", deviceID, profileID)
	}
	if got.DeviceName != name || got.DevicePlatform != platform {
		t.Errorf("registered name/platform = %q/%q, want %q/%q", got.DeviceName, got.DevicePlatform, name, platform)
	}
	if got.LastSeenAt == "" {
		t.Error("registered device has no last_seen_at")
	}
}

// The seam is what the v2 operations call; it registers as the v1 route does.
func TestResolveEffectiveSettingsSeamRegistersCallersOwnDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	if _, err := handler.ResolveEffectiveSettings(context.Background(), 1, EffectiveSettingsQuery{
		Keys:            []string{"playback.subtitle_mode"},
		ActiveProfileID: "profile-1",
		Device:          NewDeviceMetadata("phone-1", "Pocket", "iOS"),
	}); err != nil {
		t.Fatalf("ResolveEffectiveSettings: %v", err)
	}

	requireRegisteredDevice(t, store, "profile-1", "phone-1", "Pocket", "iOS")
}

// impersonating marks a context as an administrator's view-as session on the
// account's profile-1.
func impersonating(ctx context.Context) context.Context {
	admin := 99
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &admin})
	return apimw.SetProfileID(ctx, "profile-1")
}

func newDeviceSightingsPlaybackHandler(t *testing.T, store userstore.UserStore, sightings *DeviceSightings) *PlaybackHandler {
	t.Helper()
	handler := NewPlaybackHandler(playback.NewSessionManager(0, 0), testPlaybackFileResolver{file: v3HandlerFixtureFile(t)})
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{}}
	handler.ItemAccess = allowAllPlaybackItemAccess{}
	handler.StoreProvider = testUserStoreProvider{store: store}
	handler.DeviceSightings = sightings
	return handler
}

func TestPlaybackStartV2RegistersDeclaredDevice(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := newDeviceSightingsPlaybackHandler(t, store, NewDeviceSightings())
	handler.InstallationID = serviceInstallation

	if _, err := handler.StartPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{
		UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation,
		DeclaredDevice: NewDeviceMetadata("phone-1", "Pocket", "iOS"),
	}, v3HandlerStartRequest()); err != nil {
		t.Fatalf("StartPlaybackV2: %v", err)
	}

	requireRegisteredDevice(t, store, "profile-1", "phone-1", "Pocket", "iOS")
}

type failingThenStoreProvider struct {
	store  userstore.UserStore
	failed *bool
}

func (p failingThenStoreProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	if !*p.failed {
		*p.failed = true
		return nil, errors.New("store unavailable")
	}
	return p.store, nil
}

func (p failingThenStoreProvider) Close() error { return nil }

// A failed registration does not hold the throttle window, so the device's
// next request retries.
func TestFailedRegistrationIsRetried(t *testing.T) {
	store := newPlaybackTestStore(t)
	sightings := NewDeviceSightings()
	failed := false
	provider := failingThenStoreProvider{store: store, failed: &failed}
	device := NewDeviceMetadata("tv-1", "Den TV", "Android TV")

	sightings.RecordFor(context.Background(), provider, 1, "profile-1", device)
	if !failed || registeredDevice(t, store, "profile-1", "tv-1") != nil {
		t.Fatal("the first registration should have failed")
	}
	sightings.RecordFor(context.Background(), provider, 1, "profile-1", device)
	requireRegisteredDevice(t, store, "profile-1", "tv-1", "Den TV", "Android TV")
}

// livingRoom is the caller's own device as it declares itself.
var livingRoom = NewDeviceMetadata("device-1", "Living room", "tvOS")

// readEffectiveAs resolves effective settings acting as profile-1 from
// livingRoom; query fills in the rest.
func readEffectiveAs(t *testing.T, ctx context.Context, h *SettingValuesHandler, query EffectiveSettingsQuery) {
	t.Helper()
	query.Keys = []string{"playback.subtitle_mode"}
	query.ActiveProfileID = "profile-1"
	query.Device = livingRoom
	if _, err := h.ResolveEffectiveSettings(ctx, 1, query); err != nil {
		t.Fatalf("ResolveEffectiveSettings: %v", err)
	}
}

// A settings read registers only the caller's own device on the caller's own
// profile, at most once per throttle window: a read of another device or on
// another profile's behalf is a household parent inspecting it, and an
// administrator viewing as the account is using their own browser.
func TestEffectiveReadRegistersOnlyTheCallersOwnDevice(t *testing.T) {
	ctx := apimw.SetProfileID(apimw.SetClaims(context.Background(), &auth.Claims{UserID: 1}), "profile-1")

	t.Run("another device", func(t *testing.T) {
		handler, store := newValuesTestHandler(t)
		if err := store.(userstore.DeviceRegistry).RegisterDevice(context.Background(), userstore.DeviceEntry{
			ProfileID: "profile-1", DeviceID: "apple-tv",
		}); err != nil {
			t.Fatalf("registering apple-tv: %v", err)
		}
		readEffectiveAs(t, ctx, handler, EffectiveSettingsQuery{DeviceID: "apple-tv"})
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("registered the caller's device while it read another device's settings")
		}
		if got := registeredDevice(t, store, "profile-1", "apple-tv"); got == nil || got.DeviceName != "" {
			t.Errorf("the named device's row was rewritten from the caller's device: %+v", got)
		}
	})

	t.Run("another profile", func(t *testing.T) {
		handler, store := newHouseholdValuesHandler(t, "")
		readEffectiveAs(t, ctx, handler, EffectiveSettingsQuery{ProfileID: "profile-2", VerifyProfile: noProfileVerification})
		if registeredDevice(t, store, "profile-2", "device-1") != nil {
			t.Error("registered the parent's device under the child's profile")
		}
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("registered the parent's device from a read made on the child's behalf")
		}
	})

	t.Run("impersonation", func(t *testing.T) {
		handler, store := newValuesTestHandler(t)
		readEffectiveAs(t, impersonating(context.Background()), handler, EffectiveSettingsQuery{})
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("an impersonated settings read registered the administrator's device")
		}
		// It did not hold the throttle against the profile's own sessions.
		readEffectiveAs(t, ctx, handler, EffectiveSettingsQuery{})
		requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
	})

	// Within the throttle window a repeat read does not upsert again.
	// Forgetting the row in between makes a second upsert observable.
	t.Run("throttled", func(t *testing.T) {
		handler, store := newValuesTestHandler(t)
		readEffectiveAs(t, ctx, handler, EffectiveSettingsQuery{})
		requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
		if err := store.(userstore.DeviceRegistry).ForgetDevice(context.Background(), "profile-1", "device-1"); err != nil {
			t.Fatalf("ForgetDevice: %v", err)
		}
		readEffectiveAs(t, ctx, handler, EffectiveSettingsQuery{})
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("a repeat read within the throttle window upserted the device again")
		}
	})
}

// A playback start registers its device only when the device played as the
// caller: a refused start, a terminal decision and an administrator's
// view-as session register nothing, and the throttle is shared with the
// settings reads.
func TestPlaybackStartRegistersOnlyAPlayedDevice(t *testing.T) {
	caller := PlaybackCaller{UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation, DeclaredDevice: livingRoom}
	newHandler := func(t *testing.T, store userstore.UserStore, sightings *DeviceSightings) *PlaybackHandler {
		handler := newDeviceSightingsPlaybackHandler(t, store, sightings)
		handler.InstallationID = serviceInstallation
		return handler
	}

	t.Run("refused start", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		start := v3HandlerStartRequest()
		start.ProfileID = "someone-else"
		if response, err := newHandler(t, store, NewDeviceSightings()).StartPlaybackV2(newAuthorizedPlaybackContext(), caller, start); err == nil {
			t.Fatalf("start for another profile was accepted: %+v", response)
		}
		if registeredDevice(t, store, "profile-1", "device-1") != nil || registeredDevice(t, store, "someone-else", "device-1") != nil {
			t.Error("a refused start registered the device")
		}
	})

	t.Run("terminal decision", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		handler := newHandler(t, store, NewDeviceSightings())
		handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false"}}
		start := v3HandlerStartRequest()
		start.Capabilities.CodecsVideo = nil
		start.Capabilities.CodecsVideoHardware = nil
		start.Capabilities.VideoDecode = nil
		start.Capabilities.Containers = nil
		response, err := handler.StartPlaybackV2(newAuthorizedPlaybackContext(), caller, start)
		if err != nil {
			t.Fatalf("StartPlaybackV2: %v", err)
		}
		if response.Terminal == nil {
			t.Fatalf("start = %#v, want a terminal decision", response)
		}
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("a terminal start registered the device")
		}
	})

	t.Run("impersonation", func(t *testing.T) {
		store := newPlaybackTestStore(t)
		if _, err := newHandler(t, store, NewDeviceSightings()).StartPlaybackV2(impersonating(context.Background()), caller, v3HandlerStartRequest()); err != nil {
			t.Fatalf("StartPlaybackV2: %v", err)
		}
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("an impersonated playback start registered the administrator's device")
		}
	})

	t.Run("throttle shared with settings reads", func(t *testing.T) {
		sightings := NewDeviceSightings()
		values, store := newValuesTestHandler(t)
		values.DeviceSightings = sightings
		readEffectiveAs(t, context.Background(), values, EffectiveSettingsQuery{})
		requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
		if err := store.(userstore.DeviceRegistry).ForgetDevice(context.Background(), "profile-1", "device-1"); err != nil {
			t.Fatalf("ForgetDevice: %v", err)
		}
		if _, err := newHandler(t, store, sightings).StartPlaybackV2(newAuthorizedPlaybackContext(), caller, v3HandlerStartRequest()); err != nil {
			t.Fatalf("StartPlaybackV2: %v", err)
		}
		if registeredDevice(t, store, "profile-1", "device-1") != nil {
			t.Error("playback start upserted a device the settings read had just registered")
		}
	})
}
