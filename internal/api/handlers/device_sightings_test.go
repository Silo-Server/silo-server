package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// effectiveRequest is valuesRequest (profile-1, header device device-1) with
// the device's display name and platform declared.
func effectiveRequest(method, target string, body []byte) *http.Request {
	req := valuesRequest(method, target, body)
	req.Header.Set(deviceNameHeader, "Living room")
	req.Header.Set(devicePlatformHeader, "tvOS")
	return req
}

func getEffective(t *testing.T, h *SettingValuesHandler, req *http.Request) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.HandleGetEffective(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET effective = %d: %s", rec.Code, rec.Body.String())
	}
}

// A client that reads its settings but never writes a device override must
// still appear in the device registry: the effective read is what most
// clients do on launch.
func TestEffectiveReadRegistersCallersOwnDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	getEffective(t, handler, effectiveRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode", nil))

	requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
}

func TestPostEffectiveRegistersCallersOwnDevice(t *testing.T) {
	handler, store := newValuesTestHandler(t)

	req := effectiveRequest(http.MethodPost, "/settings/values/effective",
		[]byte(`{"keys":["playback.subtitle_mode"],"contexts":[{"context_id":"a","series_id":"s1"}]}`))
	rec := httptest.NewRecorder()
	handler.HandlePostEffective(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST effective = %d: %s", rec.Code, rec.Body.String())
	}

	requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
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

func TestEffectiveReadOfAnotherDeviceDoesNotRegisterCaller(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if err := store.(userstore.DeviceRegistry).RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "apple-tv",
	}); err != nil {
		t.Fatalf("registering apple-tv: %v", err)
	}

	getEffective(t, handler, effectiveRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode&device_id=apple-tv", nil))

	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("registered the caller's device while it read another device's settings")
	}
	if got := registeredDevice(t, store, "profile-1", "apple-tv"); got == nil || got.DeviceName != "" {
		t.Errorf("the named device's row was rewritten from the caller's headers: %+v", got)
	}
}

// Naming the caller's own device explicitly is still the caller's own device.
func TestEffectiveReadNamingOwnDeviceRegistersIt(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	if err := store.(userstore.DeviceRegistry).RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "device-1",
	}); err != nil {
		t.Fatalf("registering device-1: %v", err)
	}

	getEffective(t, handler, effectiveRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode&device_id=device-1", nil))

	requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
}

func TestEffectiveReadForAnotherProfileDoesNotRegisterCaller(t *testing.T) {
	handler, store := newHouseholdValuesHandler(t, "")

	getEffective(t, handler, effectiveRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode&profile_id=profile-2", nil))

	if registeredDevice(t, store, "profile-2", "device-1") != nil {
		t.Error("registered the parent's device under the child's profile")
	}
	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("registered the parent's device from a read made on the child's behalf")
	}
}

// impersonating marks a context as an administrator's view-as session on the
// account's profile-1.
func impersonating(ctx context.Context) context.Context {
	admin := 99
	ctx = apimw.SetClaims(ctx, &auth.Claims{UserID: 1, Role: "user", TokenType: auth.TokenTypeAccess, ImpersonatorUserID: &admin})
	return apimw.SetProfileID(ctx, "profile-1")
}

// An administrator viewing as the account is using their own browser, not one
// of the profile's devices: settings reads and playback starts register
// nothing, and do not hold the throttle against the profile's own sessions.
func TestImpersonationSessionDoesNotRegisterDevice(t *testing.T) {
	sightings := NewDeviceSightings()
	values, store := newValuesTestHandler(t)
	values.DeviceSightings = sightings
	playbackHandler := newDeviceSightingsPlaybackHandler(t, store, sightings)

	read := effectiveRequest(http.MethodGet, "/settings/values/effective?keys=playback.subtitle_mode", nil)
	getEffective(t, values, read.WithContext(impersonating(read.Context())))
	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("an impersonated settings read registered the administrator's device")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(impersonating(context.Background()))
	req.Header.Set(deviceIDHeader, "device-1")
	rec := httptest.NewRecorder()
	playbackHandler.HandleStartPlayback(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("an impersonated playback start registered the administrator's device")
	}

	getEffective(t, values, effectiveRequest(http.MethodGet, "/settings/values/effective?keys=playback.subtitle_mode", nil))
	requireRegisteredDevice(t, store, "profile-1", "device-1", "Living room", "tvOS")
}

// Within the throttle window a repeat read does not upsert again. Forgetting
// the row in between makes a second upsert observable.
func TestEffectiveReadRegistrationIsThrottled(t *testing.T) {
	handler, store := newValuesTestHandler(t)
	read := func() {
		getEffective(t, handler, effectiveRequest(http.MethodGet,
			"/settings/values/effective?keys=playback.subtitle_mode", nil))
	}

	read()
	if registeredDevice(t, store, "profile-1", "device-1") == nil {
		t.Fatal("first read did not register the device")
	}
	if err := store.(userstore.DeviceRegistry).ForgetDevice(context.Background(), "profile-1", "device-1"); err != nil {
		t.Fatalf("ForgetDevice: %v", err)
	}
	read()
	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("a repeat read within the throttle window upserted the device again")
	}
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

func TestPlaybackStartRegistersCallersOwnDevice(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := newDeviceSightingsPlaybackHandler(t, store, NewDeviceSightings())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	req.Header.Set(deviceIDHeader, "tv-1")
	req.Header.Set(deviceNameHeader, "Den TV")
	req.Header.Set(devicePlatformHeader, "Android TV")
	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}

	requireRegisteredDevice(t, store, "profile-1", "tv-1", "Den TV", "Android TV")
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

// A refused start registers nothing: the device did not play.
func TestRefusedPlaybackStartDoesNotRegister(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := newDeviceSightingsPlaybackHandler(t, store, NewDeviceSightings())

	start := v3HandlerStartRequest()
	start.ProfileID = "someone-else"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, start))).WithContext(newAuthorizedPlaybackContext())
	req.Header.Set(deviceIDHeader, "tv-1")
	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, req)
	if rec.Code == http.StatusCreated {
		t.Fatalf("start for another profile was accepted: %s", rec.Body.String())
	}
	if registeredDevice(t, store, "profile-1", "tv-1") != nil || registeredDevice(t, store, "someone-else", "tv-1") != nil {
		t.Error("a refused start registered the device")
	}
}

// A start that returns a terminal decision did not play, so it registers
// nothing, on the v1 route and the v2 seam alike.
func TestTerminalPlaybackDecisionDoesNotRegister(t *testing.T) {
	store := newPlaybackTestStore(t)
	handler := newDeviceSightingsPlaybackHandler(t, store, NewDeviceSightings())
	handler.InstallationID = serviceInstallation
	handler.SettingsRepo = &mutablePlaybackSettingsV3{values: map[string]string{"transcode_enabled": "false"}}
	unplayable := func() playback.StartRequestV3 {
		start := v3HandlerStartRequest()
		start.Capabilities.CodecsVideo = nil
		start.Capabilities.CodecsVideoHardware = nil
		start.Capabilities.VideoDecode = nil
		start.Capabilities.Containers = nil
		return start
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, unplayable()))).WithContext(newAuthorizedPlaybackContext())
	req.Header.Set(deviceIDHeader, "tv-1")
	rec := httptest.NewRecorder()
	handler.HandleStartPlayback(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"terminal"`) {
		t.Fatalf("start = %d, want a terminal decision: %s", rec.Code, rec.Body.String())
	}
	if registeredDevice(t, store, "profile-1", "tv-1") != nil {
		t.Error("a terminal v1 start registered the device")
	}

	v2Start := unplayable()
	v2Start.PlaybackAttemptID = "terminal-v2-attempt"
	response, err := handler.StartPlaybackV2(newAuthorizedPlaybackContext(), PlaybackCaller{
		UserID: 1, ProfileID: "profile-1", InstallationID: serviceInstallation,
		DeclaredDevice: NewDeviceMetadata("phone-1", "Pocket", "iOS"),
	}, v2Start)
	if err != nil {
		t.Fatalf("StartPlaybackV2: %v", err)
	}
	if response.Terminal == nil {
		t.Fatalf("v2 start = %#v, want a terminal decision", response)
	}
	if registeredDevice(t, store, "profile-1", "phone-1") != nil {
		t.Error("a terminal v2 start registered the device")
	}
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

// Settings and playback share one recorder in the router, so a device that
// read its settings is not upserted again when it starts playback within the
// window.
func TestDeviceSightingsThrottleIsSharedAcrossSurfaces(t *testing.T) {
	sightings := NewDeviceSightings()
	values, store := newValuesTestHandler(t)
	values.DeviceSightings = sightings
	playbackHandler := newDeviceSightingsPlaybackHandler(t, store, sightings)

	getEffective(t, values, effectiveRequest(http.MethodGet,
		"/settings/values/effective?keys=playback.subtitle_mode", nil))
	if err := store.(userstore.DeviceRegistry).ForgetDevice(context.Background(), "profile-1", "device-1"); err != nil {
		t.Fatalf("ForgetDevice: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/start",
		strings.NewReader(marshalV3StartRequest(t, v3HandlerStartRequest()))).WithContext(newAuthorizedPlaybackContext())
	req.Header.Set(deviceIDHeader, "device-1")
	rec := httptest.NewRecorder()
	playbackHandler.HandleStartPlayback(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	if registeredDevice(t, store, "profile-1", "device-1") != nil {
		t.Error("playback start upserted a device the settings read had just registered")
	}
}
