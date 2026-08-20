package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newDevicesTestHandler(t *testing.T) (*DeviceHandler, userstore.UserStore) {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) +
		"?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	ctx := context.Background()
	for _, p := range []userstore.Profile{
		{ID: "profile-1", Name: "Sam", IsPrimary: true},
		{ID: "profile-2", Name: "Robin"},
	} {
		if err := store.CreateProfile(ctx, p); err != nil {
			t.Fatalf("create profile %s: %v", p.ID, err)
		}
	}

	return NewDeviceHandler(testUserStoreProvider{store: store}), store
}

func seedDevice(t *testing.T, store userstore.UserStore, profileID, deviceID, name string) {
	t.Helper()
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatal("store does not implement DeviceRegistry")
	}
	if err := registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID: profileID, DeviceID: deviceID, DeviceName: name, DevicePlatform: "web",
	}); err != nil {
		t.Fatalf("registering %s: %v", deviceID, err)
	}
}

func seedDeviceValue(t *testing.T, store userstore.UserStore, profileID, deviceID, key, value string) {
	t.Helper()
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       key,
		Scope:     settingscontract.ScopeProfileDevice,
		ProfileID: profileID,
		DeviceID:  deviceID,
	}, json.RawMessage(value)); err != nil {
		t.Fatalf("seeding %s on %s: %v", key, deviceID, err)
	}
}

func devicesRequest(method, target, profileID string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set(deviceIDHeader, "device-1")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1})
	return req.WithContext(apimw.SetProfileID(ctx, profileID))
}

func listDevices(t *testing.T, h *DeviceHandler, query, profileID string) deviceListResponse {
	t.Helper()
	target := "/devices"
	if query != "" {
		target += "?" + query
	}
	rec := httptest.NewRecorder()
	h.HandleListDevices(rec, devicesRequest(http.MethodGet, target, profileID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
	}
	var body deviceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return body
}

// TestListDevices_FiltersToCallingProfile is the security test for this
// endpoint. ListDevices is account-wide by construction in both backends —
// "WHERE user_id" in Postgres and no WHERE at all in the per-user SQLite — so a
// passthrough would show every household member's devices to everyone.
func TestListDevices_FiltersToCallingProfile(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	body := listDevices(t, handler, "", "profile-1")

	if len(body.Devices) != 1 {
		t.Fatalf("returned %d devices, want 1: %+v", len(body.Devices), body.Devices)
	}
	if body.Devices[0].DeviceID != "device-1" {
		t.Errorf("returned device %q, want device-1", body.Devices[0].DeviceID)
	}
	for _, device := range body.Devices {
		if device.ProfileID != "profile-1" {
			t.Errorf("leaked device %q from profile %q", device.DeviceID, device.ProfileID)
		}
	}
}

func TestListDevices_CountsChangedSettings(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Laptop")
	seedDevice(t, store, "profile-1", "device-2", "Apple TV")
	seedDeviceValue(t, store, "profile-1", "device-2", "player.hdr_enabled", `false`)
	seedDeviceValue(t, store, "profile-1", "device-2", "playback.subtitle_mode", `"always"`)
	// Another profile's row on the same device must not be counted.
	seedDeviceValue(t, store, "profile-2", "device-2", "player.seek_cache_enabled", `false`)

	body := listDevices(t, handler, "", "profile-1")

	counts := map[string]int{}
	for _, device := range body.Devices {
		counts[device.DeviceID] = device.ChangedCount
	}
	if counts["device-2"] != 2 {
		t.Errorf("device-2 changed_count = %d, want 2", counts["device-2"])
	}
	if counts["device-1"] != 0 {
		t.Errorf("device-1 changed_count = %d, want 0", counts["device-1"])
	}
}

// TestListDevices_CountsAMirroredPairOnce. The intro-skip preference is stored
// under two keys while old clients are in the field, and the badge on the
// device list is meant to tell a household how much this device does
// differently — not how many rows the compatibility mirror needed to say it.
// Counting rows would also make the badge drop by one on every affected device
// the day the mirror is retired, which reads as a change nobody made.
func TestListDevices_CountsAMirroredPairOnce(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Laptop")
	seedDeviceValue(t, store, "profile-1", "device-1",
		settingskeys.PlaybackIntroSkipMode, `"never"`)
	seedDeviceValue(t, store, "profile-1", "device-1",
		settingskeys.PlaybackAutoSkipIntro, `false`)
	seedDeviceValue(t, store, "profile-1", "device-1", "player.hdr_enabled", `false`)

	body := listDevices(t, handler, "", "profile-1")

	if len(body.Devices) != 1 {
		t.Fatalf("returned %d devices, want 1: %+v", len(body.Devices), body.Devices)
	}
	if body.Devices[0].ChangedCount != 2 {
		t.Errorf("changed_count = %d, want 2: the mirrored intro pair is one preference",
			body.Devices[0].ChangedCount)
	}
}

func TestListDevices_MarksCurrentDevice(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "This browser")
	seedDevice(t, store, "profile-1", "device-2", "Apple TV")

	body := listDevices(t, handler, "", "profile-1")

	for _, device := range body.Devices {
		want := device.DeviceID == "device-1"
		if device.IsCurrentDevice != want {
			t.Errorf("device %q is_current_device = %v, want %v",
				device.DeviceID, device.IsCurrentDevice, want)
		}
	}
}

func TestListDevices_IncludesProfileName(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Laptop")

	body := listDevices(t, handler, "", "profile-1")

	if len(body.Devices) != 1 || body.Devices[0].ProfileName != "Sam" {
		t.Errorf("profile_name = %q, want Sam", body.Devices[0].ProfileName)
	}
}

func routeDevice(
	t *testing.T, h *DeviceHandler, method, target, deviceID, profileID string,
	handle func(http.ResponseWriter, *http.Request),
) *httptest.ResponseRecorder {
	t.Helper()
	req := devicesRequest(method, target, profileID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("device_id", deviceID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	handle(rec, req)
	return rec
}

func TestForgetDevice_RemovesSettingsAndRegistryRow(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-2", "Apple TV")
	seedDeviceValue(t, store, "profile-1", "device-2", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-2", "device-2",
		"profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-1", "device-2")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if exists {
		t.Error("registry row survived forget")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Errorf("setting row survived forget on device %q", got)
	}
}

func TestForgetDevice_RejectsOtherProfilesDevice(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")
	seedDeviceValue(t, store, "profile-2", "device-9", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9", "device-9",
		"profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE another profile's device = %d, want 404", rec.Code)
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-9")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("another profile's device was removed")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "device-9" {
		t.Errorf("another profile's setting row was removed (device %q)", got)
	}
}

// A repeated forget reports 404, not 500 or a partial delete: once the device
// is gone this profile has no trace of it, which is indistinguishable from a
// device that was never here — and deliberately so, since the same answer is
// what keeps another profile's device ids from being probeable.
func TestForgetDevice_SecondCallIsNotFound(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-2", "Apple TV")

	if rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-2", "device-2",
		"profile-1", handler.HandleForgetDevice); rec.Code != http.StatusNoContent {
		t.Fatalf("first DELETE = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-2", "device-2",
		"profile-1", handler.HandleForgetDevice); rec.Code != http.StatusNotFound {
		t.Fatalf("second DELETE = %d, want 404", rec.Code)
	}
}

// Forgetting a device two profiles share removes only the caller's half.
func TestForgetDevice_LeavesOtherProfilesRowOnSharedDevice(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "shared-tv", "Living Room TV")
	seedDevice(t, store, "profile-2", "shared-tv", "Living Room TV")
	seedDeviceValue(t, store, "profile-2", "shared-tv", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/shared-tv", "shared-tv",
		"profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	stillThere, err := registry.DeviceExists(context.Background(), "profile-2", "shared-tv")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !stillThere {
		t.Error("forgetting one profile's half removed the other profile's row")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "shared-tv" {
		t.Errorf("the other profile's setting row was removed (device %q)", got)
	}
}

func TestClearDeviceSettings_KeepsRegistryRow(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-2", "Apple TV")
	seedDeviceValue(t, store, "profile-1", "device-2", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-2/settings", "device-2",
		"profile-1", handler.HandleClearDeviceSettings)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-1", "device-2")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("registry row was removed; clear must keep the device")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "" {
		t.Errorf("setting row survived clear on device %q", got)
	}
}

// --- Household scope ---

func householdDevicesHandler(t *testing.T) (*DeviceHandler, userstore.UserStore) {
	t.Helper()
	handler, store := newDevicesTestHandler(t)
	handler.UserRepo = stubUserRepo{user: &models.User{ID: 1}}
	handler.ProfileTokens = access.NewProfileTokenService("test-secret-value-at-least-32-chars", 0)
	return handler, store
}

func TestListDevices_PrimarySeesHouseholdWhenRequested(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	body := listDevices(t, handler, "scope=household", "profile-1")

	if len(body.Devices) != 2 {
		t.Fatalf("household scope returned %d devices, want 2: %+v", len(body.Devices), body.Devices)
	}
	names := map[string]string{}
	for _, device := range body.Devices {
		names[device.DeviceID] = device.ProfileName
	}
	if names["device-9"] != "Robin" {
		t.Errorf("device-9 profile_name = %q, want Robin", names["device-9"])
	}
}

func TestListDevices_NonPrimaryCannotRequestHousehold(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	rec := httptest.NewRecorder()
	handler.HandleListDevices(rec, devicesRequest(http.MethodGet, "/devices?scope=household", "profile-2"))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-primary household read = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "device-1") {
		t.Error("refusal leaked another profile's device")
	}
}

// Default scope stays private even for the household parent, so the ordinary
// screen cannot show the family's devices by forgetting to ask for less.
func TestListDevices_PrimaryDefaultsToOwnProfile(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	body := listDevices(t, handler, "", "profile-1")

	if len(body.Devices) != 1 || body.Devices[0].DeviceID != "device-1" {
		t.Fatalf("default scope returned %+v, want only device-1", body.Devices)
	}
}

func TestForgetDevice_PrimaryMayForgetHouseholdDevice(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")
	seedDeviceValue(t, store, "profile-2", "device-9", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9?profile_id=profile-2",
		"device-9", "profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("primary forgetting a household device = %d: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-9")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if exists {
		t.Error("device survived the household forget")
	}
}

func TestForgetDevice_NonPrimaryCannotForgetSiblingsDevice(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-1?profile_id=profile-1",
		"device-1", "profile-2", handler.HandleForgetDevice)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-primary forgetting a sibling's device = %d, want 403: %s",
			rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-1", "device-1")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("a non-primary profile removed a sibling's device")
	}
}
