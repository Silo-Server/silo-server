package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type testAdminUserRepo struct {
	users map[int]*models.User
}

func (r testAdminUserRepo) List(context.Context) ([]*models.User, error) {
	out := make([]*models.User, 0, len(r.users))
	for _, user := range r.users {
		out = append(out, user)
	}
	return out, nil
}

func (r testAdminUserRepo) Create(context.Context, models.CreateUserInput) (*models.User, error) {
	panic("unexpected Create call")
}

func (r testAdminUserRepo) Update(context.Context, int, models.UpdateUserInput) error {
	panic("unexpected Update call")
}

func (r testAdminUserRepo) Delete(context.Context, int) error {
	panic("unexpected Delete call")
}

func (r testAdminUserRepo) GetByID(_ context.Context, id int) (*models.User, error) {
	return r.users[id], nil
}

func TestRegisterRequestDeviceNilStore(t *testing.T) {
	registerRequestDevice(context.Background(), nil, "profile-1", requestDeviceMetadata{
		DeviceID:       "device-1",
		DeviceName:     "Living Room",
		DevicePlatform: "web",
	})
}

type mappedTestUserStoreProvider struct {
	stores map[int]userstore.UserStore
}

func (p mappedTestUserStoreProvider) ForUser(_ context.Context, userID int) (userstore.UserStore, error) {
	return p.stores[userID], nil
}

func (p mappedTestUserStoreProvider) Close() error { return nil }

func newIsolatedProfileTestStore(t *testing.T, suffix string) userstore.UserStore {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()+"-"+suffix) + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return store
}

func TestEffectiveSubtitleAppearancePrefersDeviceOverride(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID:      "profile-1",
		DeviceID:       "device-1",
		DeviceName:     "Living Room",
		DevicePlatform: "tvOS",
		Key:            subtitleAppearanceSettingKey,
		Value:          `{"fontSize":"small"}`,
	}); err != nil {
		t.Fatalf("SetDeviceSetting: %v", err)
	}

	handler := NewSettingsHandler(testUserStoreProvider{store: store})
	req := httptest.NewRequest(http.MethodGet, "/settings/subtitle_appearance/effective", nil)
	req.Header.Set(deviceIDHeader, "device-1")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleGetEffectiveSubtitleAppearance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp effectiveSubtitleAppearanceResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.HasDeviceOverride || resp.EffectiveValue != `{"fontSize":"small"}` || resp.GlobalValue != "" {
		t.Fatalf("response = %#v", resp)
	}
}

func TestSubtitleAppearanceDeviceOverrideRoundTrip(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	body := bytes.NewBufferString(`{"value":"{\"fontSize\":\"xxlarge\"}"}`)
	req := httptest.NewRequest(http.MethodPut, "/settings/device/subtitle_appearance", body)
	req.Header.Set(deviceIDHeader, "iphone")
	req.Header.Set(deviceNameHeader, "Example iPhone")
	req.Header.Set(devicePlatformHeader, "iOS")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleSetSubtitleAppearanceDeviceOverride(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set status = %d body=%s", rec.Code, rec.Body.String())
	}
	entry, err := store.GetDeviceSetting(context.Background(), "profile-1", "iphone", subtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting: %v", err)
	}
	if entry == nil || entry.Value != `{"fontSize":"xxlarge"}` || entry.DevicePlatform != "iOS" {
		t.Fatalf("entry = %#v", entry)
	}

	req = httptest.NewRequest(http.MethodDelete, "/settings/device/subtitle_appearance", nil)
	req.Header.Set(deviceIDHeader, "iphone")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec = httptest.NewRecorder()
	handler.HandleDeleteSubtitleAppearanceDeviceOverride(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}
	entry, err = store.GetDeviceSetting(context.Background(), "profile-1", "iphone", subtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting after delete: %v", err)
	}
	if entry != nil {
		t.Fatalf("entry after delete = %#v, want nil", entry)
	}
}

func TestGetEffectiveSettingsResolvesUserDeviceAndDefaultSources(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID: "profile-1",
		DeviceID:  "apple-tv",
		Key:       "playback.preferred_quality",
		Value:     "1080p",
	}); err != nil {
		t.Fatalf("SetDeviceSetting(preferred_quality): %v", err)
	}
	if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID: "profile-1",
		DeviceID:  "apple-tv",
		Key:       "player.playback_speed",
		Value:     "1.25",
	}); err != nil {
		t.Fatalf("SetDeviceSetting: %v", err)
	}

	handler := NewSettingsHandler(testUserStoreProvider{store: store})
	req := httptest.NewRequest(
		http.MethodGet,
		"/settings/effective?keys=playback.preferred_quality,player.playback_speed,player.hdr_enabled,ui.remember_library_page_state",
		nil,
	)
	req.Header.Set(deviceIDHeader, "apple-tv")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleGetEffectiveSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp effectiveSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Settings) != 4 {
		t.Fatalf("settings len = %d", len(resp.Settings))
	}
	byKey := make(map[string]effectiveSettingResponse, len(resp.Settings))
	for _, entry := range resp.Settings {
		byKey[entry.Key] = entry
	}
	if got := byKey["playback.preferred_quality"]; got.EffectiveValue != "1080p" || got.Source != "device" || !got.HasDeviceOverride {
		t.Fatalf("preferred_quality = %#v", got)
	}
	if got := byKey["player.playback_speed"]; got.EffectiveValue != "1.25" || got.Source != "device" || !got.HasDeviceOverride {
		t.Fatalf("playback_speed = %#v", got)
	}
	if got := byKey["player.hdr_enabled"]; got.EffectiveValue != "true" || got.Source != "default" {
		t.Fatalf("hdr_enabled = %#v", got)
	}
	if got := byKey[rememberLibraryPageStateSettingKey]; got.EffectiveValue != "true" || got.Source != "default" {
		t.Fatalf("remember_library_page_state = %#v", got)
	}
}

func TestGenericSettingsRejectInvalidRegisteredValues(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(
		http.MethodPut,
		"/settings/playback.auto_skip_intro",
		bytes.NewBufferString(`{"value":"maybe"}`),
	)
	req = withRouteParams(req, map[string]string{"key": "playback.auto_skip_intro"})
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}))
	rec := httptest.NewRecorder()

	handler.HandleSetSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestLibraryPageStateIsDeviceScopedJSONSetting(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(
		http.MethodPut,
		"/settings/device/ui.library_page_state",
		bytes.NewBufferString(`{"value":"{\"version\":1,\"libraries\":{\"7\":{\"search\":\"tab=library\"}}}"}`),
	)
	req = withRouteParams(req, map[string]string{"key": libraryPageStateSettingKey})
	req.Header.Set(deviceIDHeader, "browser")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleSetDeviceSetting(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	entry, err := store.GetDeviceSetting(context.Background(), "profile-1", "browser", libraryPageStateSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting: %v", err)
	}
	if entry == nil || !strings.Contains(entry.Value, `"tab=library"`) {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestLibraryPageStateRejectsInvalidJSONAndUserScope(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(
		http.MethodPut,
		"/settings/device/ui.library_page_state",
		bytes.NewBufferString(`{"value":"not json"}`),
	)
	req = withRouteParams(req, map[string]string{"key": libraryPageStateSettingKey})
	req.Header.Set(deviceIDHeader, "browser")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleSetDeviceSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(
		http.MethodPut,
		"/settings/ui.library_page_state",
		bytes.NewBufferString(`{"value":"{\"version\":1,\"libraries\":{}}"}`),
	)
	req = withRouteParams(req, map[string]string{"key": libraryPageStateSettingKey})
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}))
	rec = httptest.NewRecorder()

	handler.HandleSetSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("user-scope status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRememberLibraryPageStateIsDeviceScopedBoolSetting(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(
		http.MethodPut,
		"/settings/device/ui.remember_library_page_state",
		bytes.NewBufferString(`{"value":"false"}`),
	)
	req = withRouteParams(req, map[string]string{"key": rememberLibraryPageStateSettingKey})
	req.Header.Set(deviceIDHeader, "browser")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec := httptest.NewRecorder()

	handler.HandleSetDeviceSetting(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	entry, err := store.GetDeviceSetting(context.Background(), "profile-1", "browser", rememberLibraryPageStateSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting: %v", err)
	}
	if entry == nil || entry.Value != "false" {
		t.Fatalf("entry = %#v", entry)
	}

	req = httptest.NewRequest(
		http.MethodPut,
		"/settings/device/ui.remember_library_page_state",
		bytes.NewBufferString(`{"value":"maybe"}`),
	)
	req = withRouteParams(req, map[string]string{"key": rememberLibraryPageStateSettingKey})
	req.Header.Set(deviceIDHeader, "browser")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec = httptest.NewRecorder()

	handler.HandleSetDeviceSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid bool status = %d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(
		http.MethodPut,
		"/settings/ui.remember_library_page_state",
		bytes.NewBufferString(`{"value":"false"}`),
	)
	req = withRouteParams(req, map[string]string{"key": rememberLibraryPageStateSettingKey})
	req = req.WithContext(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}))
	rec = httptest.NewRecorder()

	handler.HandleSetSetting(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("user-scope status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminCanResetSubtitleAppearanceDeviceOverrides(t *testing.T) {
	store := newProfileTestStore(t)
	for _, deviceID := range []string{"apple-tv", "iphone"} {
		if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
			ProfileID: "profile-1",
			DeviceID:  deviceID,
			Key:       subtitleAppearanceSettingKey,
			Value:     `{"fontSize":"small"}`,
		}); err != nil {
			t.Fatalf("SetDeviceSetting(%s): %v", deviceID, err)
		}
	}
	handler := &AdminHandler{storeProv: testUserStoreProvider{store: store}}

	req := httptest.NewRequest(http.MethodDelete, "/admin/users/7/profiles/profile-1/device-settings/subtitle_appearance/apple-tv", nil)
	req = withRouteParams(req, map[string]string{
		"id": "7", "profile_id": "profile-1", "key": subtitleAppearanceSettingKey, "device_id": "apple-tv",
	})
	rec := httptest.NewRecorder()
	handler.HandleDeleteUserDeviceSetting(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete one status = %d body=%s", rec.Code, rec.Body.String())
	}
	remaining, err := store.ListDeviceSettings(context.Background(), subtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("ListDeviceSettings: %v", err)
	}
	if len(remaining) != 1 || remaining[0].DeviceID != "iphone" {
		t.Fatalf("remaining = %#v", remaining)
	}

	req = httptest.NewRequest(http.MethodDelete, "/admin/users/7/device-settings/subtitle_appearance", nil)
	req = withRouteParams(req, map[string]string{"id": "7", "key": subtitleAppearanceSettingKey})
	rec = httptest.NewRecorder()
	handler.HandleDeleteUserDeviceSettingsByKey(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete all status = %d body=%s", rec.Code, rec.Body.String())
	}
	remaining, err = store.ListDeviceSettings(context.Background(), subtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("ListDeviceSettings after delete all: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining after delete all = %#v", remaining)
	}
}

func TestEffectiveSettingsAreIsolatedPerProfileOnSameDevice(t *testing.T) {
	store := newProfileTestStore(t)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-2", Name: "Guest"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID: "profile-1",
		DeviceID:  "shared-tv",
		Key:       "player.playback_speed",
		Value:     "1.5",
	}); err != nil {
		t.Fatalf("SetDeviceSetting profile-1: %v", err)
	}
	if err := store.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID: "profile-2",
		DeviceID:  "shared-tv",
		Key:       "player.playback_speed",
		Value:     "0.75",
	}); err != nil {
		t.Fatalf("SetDeviceSetting profile-2: %v", err)
	}

	handler := NewSettingsHandler(testUserStoreProvider{store: store})

	req := httptest.NewRequest(http.MethodGet, "/settings/effective?keys=player.playback_speed", nil)
	req.Header.Set(deviceIDHeader, "shared-tv")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-2"))
	rec := httptest.NewRecorder()

	handler.HandleGetEffectiveSettings(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp effectiveSettingsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Settings) != 1 {
		t.Fatalf("settings len = %d", len(resp.Settings))
	}
	if got := resp.Settings[0]; got.ProfileID != "profile-2" || got.EffectiveValue != "0.75" {
		t.Fatalf("effective setting = %#v", got)
	}
}

func TestAdminCanListAndInspectDevicesAcrossUsers(t *testing.T) {
	store1 := newIsolatedProfileTestStore(t, "one")
	store2 := newIsolatedProfileTestStore(t, "two")
	if err := store1.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID:      "profile-1",
		DeviceID:       "living-room",
		DeviceName:     "Living Room TV",
		DevicePlatform: "tvOS",
		Key:            "player.playback_speed",
		Value:          "1.25",
	}); err != nil {
		t.Fatalf("SetDeviceSetting store1: %v", err)
	}
	if err := store1.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID:      "profile-1",
		DeviceID:       "living-room",
		DeviceName:     "Living Room TV",
		DevicePlatform: "tvOS",
		Key:            "player.audio_sync_ms",
		Value:          "120",
	}); err != nil {
		t.Fatalf("SetDeviceSetting store1 second: %v", err)
	}
	store1Registry, ok := store1.(userstore.DeviceRegistry)
	if !ok {
		t.Fatalf("store1 does not support device registry")
	}
	if err := store1Registry.RegisterDevice(context.Background(), userstore.DeviceEntry{
		ProfileID:      "profile-1",
		DeviceID:       "bedroom",
		DeviceName:     "Bedroom TV",
		DevicePlatform: "Android TV",
	}); err != nil {
		t.Fatalf("RegisterDevice store1: %v", err)
	}
	if err := store2.SetDeviceSetting(context.Background(), userstore.DeviceSettingEntry{
		ProfileID:      "profile-1",
		DeviceID:       "phone",
		DeviceName:     "Travel Phone",
		DevicePlatform: "iOS",
		Key:            "player.hdr_enabled",
		Value:          "false",
	}); err != nil {
		t.Fatalf("SetDeviceSetting store2: %v", err)
	}

	handler := &AdminHandler{
		userRepo: testAdminUserRepo{
			users: map[int]*models.User{
				7:  {ID: 7, Username: "alice", Email: "alice@example.com"},
				11: {ID: 11, Username: "bob", Email: "bob@example.com"},
			},
		},
		storeProv: mappedTestUserStoreProvider{
			stores: map[int]userstore.UserStore{
				7:  store1,
				11: store2,
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/devices", nil)
	rec := httptest.NewRecorder()
	handler.HandleListDevices(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rec.Code, rec.Body.String())
	}
	var listResp adminDevicesListResponse
	if err := json.NewDecoder(rec.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Devices) != 3 {
		t.Fatalf("devices len = %d, want 3", len(listResp.Devices))
	}
	var bedroom *adminDeviceSummaryResponse
	for i := range listResp.Devices {
		if listResp.Devices[i].DeviceID == "bedroom" {
			bedroom = &listResp.Devices[i]
			break
		}
	}
	if bedroom == nil {
		t.Fatalf("registered device without overrides missing: %#v", listResp.Devices)
	}
	if bedroom.OverrideCount != 0 || bedroom.ProfileCount != 1 || bedroom.DeviceName != "Bedroom TV" {
		t.Fatalf("registered device summary = %#v", bedroom)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/devices/7/living-room", nil)
	req = withRouteParams(req, map[string]string{"user_id": "7", "device_id": "living-room"})
	rec = httptest.NewRecorder()
	handler.HandleGetDevice(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	var detailResp adminDeviceDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detailResp); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detailResp.Username != "alice" || detailResp.DeviceName != "Living Room TV" {
		t.Fatalf("detail response = %#v", detailResp)
	}
	if len(detailResp.Settings) != 2 {
		t.Fatalf("detail settings len = %d, want 2", len(detailResp.Settings))
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/devices/7/bedroom", nil)
	req = withRouteParams(req, map[string]string{"user_id": "7", "device_id": "bedroom"})
	rec = httptest.NewRecorder()
	handler.HandleGetDevice(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("registered detail status = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.NewDecoder(rec.Body).Decode(&detailResp); err != nil {
		t.Fatalf("decode registered detail: %v", err)
	}
	if detailResp.DeviceName != "Bedroom TV" || detailResp.OverrideCount != 0 {
		t.Fatalf("registered detail response = %#v", detailResp)
	}
	if len(detailResp.Settings) != 0 {
		t.Fatalf("registered detail settings len = %d, want 0", len(detailResp.Settings))
	}
	if len(detailResp.Profiles) != 1 || detailResp.Profiles[0].ProfileID != "profile-1" {
		t.Fatalf("registered detail profiles = %#v", detailResp.Profiles)
	}
}

func TestAdminCanResetAllOverridesForOneDevice(t *testing.T) {
	store := newProfileTestStore(t)
	for _, entry := range []userstore.DeviceSettingEntry{
		{ProfileID: "profile-1", DeviceID: "living-room", Key: "player.playback_speed", Value: "1.25"},
		{ProfileID: "profile-1", DeviceID: "living-room", Key: "player.audio_sync_ms", Value: "120"},
		{ProfileID: "profile-1", DeviceID: "phone", Key: "player.hdr_enabled", Value: "false"},
	} {
		if err := store.SetDeviceSetting(context.Background(), entry); err != nil {
			t.Fatalf("SetDeviceSetting: %v", err)
		}
	}

	handler := &AdminHandler{storeProv: testUserStoreProvider{store: store}}
	req := httptest.NewRequest(http.MethodDelete, "/admin/users/7/profiles/profile-1/devices/living-room/settings", nil)
	req = withRouteParams(req, map[string]string{"id": "7", "profile_id": "profile-1", "device_id": "living-room"})
	rec := httptest.NewRecorder()
	handler.HandleDeleteAllUserDeviceSettings(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}

	entries, err := store.ListAllDeviceSettings(context.Background())
	if err != nil {
		t.Fatalf("ListAllDeviceSettings: %v", err)
	}
	if len(entries) != 1 || entries[0].DeviceID != "phone" {
		t.Fatalf("entries after delete = %#v", entries)
	}
	registry, ok := store.(userstore.DeviceRegistry)
	if !ok {
		t.Fatalf("store does not support device registry")
	}
	devices, err := registry.ListDevices(context.Background())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	foundLivingRoom := false
	for _, device := range devices {
		if device.ProfileID == "profile-1" && device.DeviceID == "living-room" {
			foundLivingRoom = true
			break
		}
	}
	if !foundLivingRoom {
		t.Fatalf("registry devices after delete = %#v", devices)
	}
}

func withRouteParams(req *http.Request, params map[string]string) *http.Request {
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
