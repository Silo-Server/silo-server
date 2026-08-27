package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// --- devices.forget_requires_primary ---

func restrictedForgetHandler(t *testing.T, value string) (*DeviceHandler, userstore.UserStore) {
	t.Helper()
	handler, store := householdDevicesHandler(t)
	handler.ServerSettings = &fakeServerSettingsStore{values: map[string]string{
		forgetRequiresPrimarySettingKey: value,
	}}
	return handler, store
}

// erroringSettingsReader simulates the server-settings store being
// unreachable, the one state where the forget policy cannot be evaluated.
type erroringSettingsReader struct{}

func (erroringSettingsReader) Get(context.Context, string) (string, error) {
	return "", errors.New("settings store unavailable")
}

// An unreadable policy must reject the forget rather than guess: failing open
// would let a non-primary profile forget devices while the operator believes
// the restriction is enforced. The device and its settings must survive.
func TestForgetDevice_SettingReadErrorRejectsAndKeepsDevice(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	handler.ServerSettings = erroringSettingsReader{}
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")
	seedDeviceValue(t, store, "profile-2", "device-9", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9", "device-9",
		"profile-2", handler.HandleForgetDevice)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("forget with unreadable policy = %d, want 500: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-9")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("a rejected forget still removed the device")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "device-9" {
		t.Errorf("a rejected forget still removed the setting row (device %q)", got)
	}
}

// Clear-settings never consults the forget policy, so an unreadable settings
// store must not affect it.
func TestClearDeviceSettings_SettingReadErrorStillClears(t *testing.T) {
	handler, store := householdDevicesHandler(t)
	handler.ServerSettings = erroringSettingsReader{}
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9/settings", "device-9",
		"profile-2", handler.HandleClearDeviceSettings)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("clear with unreadable policy = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// The restriction is opt-in: with the setting stored as false a non-primary
// profile keeps forgetting its own devices, exactly as before the setting
// existed. The unwired case (no ServerSettings at all) is every other forget
// test in this file.
func TestForgetDevice_RequirePrimaryOff_NonPrimaryForgetsOwnDevice(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "false")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9", "device-9",
		"profile-2", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE with restriction off = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

func TestForgetDevice_RequirePrimaryOn_NonPrimaryOwnDeviceForbidden(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")
	seedDeviceValue(t, store, "profile-2", "device-9", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9", "device-9",
		"profile-2", handler.HandleForgetDevice)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("restricted non-primary forget = %d, want 403: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-9")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("a refused forget still removed the device")
	}
	if got := storedDeviceIDFor(t, store, "player.hdr_enabled"); got != "device-9" {
		t.Errorf("a refused forget still removed the setting row (device %q)", got)
	}
}

func TestForgetDevice_RequirePrimaryOn_PrimaryForgetsOwnDevice(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-1", "device-1",
		"profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("restricted primary forget = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// An admin account passes the guard regardless of which profile is active:
// canManageHousehold answers for admins before it ever looks at profiles.
func TestForgetDevice_RequirePrimaryOn_AdminForgetsOwnDevice(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	req := httptest.NewRequest(http.MethodDelete, "/devices/device-9", nil)
	req.Header.Set(deviceIDHeader, "device-1")
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, Role: "admin"})
	req = req.WithContext(apimw.SetProfileID(ctx, "profile-2"))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("device_id", "device-9")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	handler.HandleForgetDevice(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("restricted admin forget = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// Clearing settings is a recoverable reset, not a removal, so the restriction
// deliberately leaves it open to every profile.
func TestClearDeviceSettings_RequirePrimaryOn_NonPrimaryStillClears(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")
	seedDeviceValue(t, store, "profile-2", "device-9", "player.hdr_enabled", `false`)

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9/settings", "device-9",
		"profile-2", handler.HandleClearDeviceSettings)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("restricted non-primary clear = %d, want 204: %s", rec.Code, rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-2", "device-9")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("clear removed the registry row under the restriction")
	}
}

// Cross-profile forgets already required the primary profile; the setting must
// not narrow what the household parent could do.
func TestForgetDevice_RequirePrimaryOn_PrimaryStillForgetsHouseholdDevice(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-2", "device-9", "Robin's iPad")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-9?profile_id=profile-2",
		"device-9", "profile-1", handler.HandleForgetDevice)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("restricted household forget = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

func TestForgetDevice_RequirePrimaryOn_NonPrimaryCrossProfileStillForbidden(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")

	rec := routeDevice(t, handler, http.MethodDelete, "/devices/device-1?profile_id=profile-1",
		"device-1", "profile-2", handler.HandleForgetDevice)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("restricted cross-profile forget = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// pinLockedForgetRequest drives HandleForgetDevice as the primary profile with
// a session-bearing claim, optionally presenting an X-Profile-Token. The
// shared devicesRequest helper carries no session id, which a profile token
// must bind to, so the PIN tests build their request here.
func pinLockedForgetRequest(
	t *testing.T, handler *DeviceHandler, deviceID, token string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/devices/"+deviceID, nil)
	req.Header.Set(deviceIDHeader, "device-1")
	if token != "" {
		req.Header.Set("X-Profile-Token", token)
	}
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: 1, SessionID: "session-1"})
	req = req.WithContext(apimw.SetProfileID(ctx, "profile-1"))
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("device_id", deviceID)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))

	rec := httptest.NewRecorder()
	handler.HandleForgetDevice(rec, req)
	return rec
}

func pinLockPrimary(t *testing.T, store userstore.UserStore) {
	t.Helper()
	pin := "1234"
	if err := store.UpdateProfile(context.Background(), "profile-1", userstore.UpdateProfileInput{
		PIN: &pin,
	}); err != nil {
		t.Fatalf("set pin: %v", err)
	}
}

// The restriction rides canManageHousehold, so a PIN-locked primary must
// verify its PIN even to forget its own device: with a valid token the forget
// goes through.
func TestForgetDevice_RequirePrimaryOn_PinLockedPrimaryWithTokenForgets(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	pinLockPrimary(t, store)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")

	token, _, err := handler.ProfileTokens.Mint(access.ProfileTokenClaims{
		UserID: 1, SessionID: "session-1", ProfileID: "profile-1", PolicyRevision: 0,
	})
	if err != nil {
		t.Fatalf("issuing token: %v", err)
	}

	rec := pinLockedForgetRequest(t, handler, "device-1", token)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("pin-locked primary with token = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

// ...and without the token, X-Profile-Id alone cannot walk past the profile
// lock — the refusal names the PIN so the client knows which door to knock on.
func TestForgetDevice_RequirePrimaryOn_PinLockedPrimaryWithoutTokenForbidden(t *testing.T) {
	handler, store := restrictedForgetHandler(t, "true")
	pinLockPrimary(t, store)
	seedDevice(t, store, "profile-1", "device-1", "Sam's laptop")

	rec := pinLockedForgetRequest(t, handler, "device-1", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pin-locked primary without token = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "verifying the primary profile PIN") {
		t.Errorf("refusal should name the PIN verification, got: %s", rec.Body.String())
	}

	registry := store.(userstore.DeviceRegistry)
	exists, err := registry.DeviceExists(context.Background(), "profile-1", "device-1")
	if err != nil {
		t.Fatalf("DeviceExists: %v", err)
	}
	if !exists {
		t.Error("an unverified forget still removed the device")
	}
}
