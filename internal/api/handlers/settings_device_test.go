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
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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

func (r testAdminUserRepo) ListPage(context.Context, int, int, string) ([]*models.User, error) {
	return nil, nil
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
	entry, err := store.GetDeviceSetting(context.Background(), "profile-1", "iphone", SubtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting: %v", err)
	}
	if entry == nil || entry.Value != `{"fontSize":"xxlarge"}` || entry.DevicePlatform != "iOS" {
		t.Fatalf("entry = %#v", entry)
	}

	req = httptest.NewRequest(http.MethodGet, "/settings/subtitle_appearance/effective", nil)
	req.Header.Set(deviceIDHeader, "iphone")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec = httptest.NewRecorder()

	handler.HandleGetEffectiveSubtitleAppearance(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp EffectiveSubtitleAppearanceView
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.HasDeviceOverride || resp.EffectiveValue != `{"fontSize":"xxlarge"}` || resp.GlobalValue != "" {
		t.Fatalf("response = %#v", resp)
	}

	req = httptest.NewRequest(http.MethodDelete, "/settings/device/subtitle_appearance", nil)
	req.Header.Set(deviceIDHeader, "iphone")
	req = req.WithContext(apimw.SetProfileID(apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
	rec = httptest.NewRecorder()
	handler.HandleDeleteSubtitleAppearanceDeviceOverride(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body=%s", rec.Code, rec.Body.String())
	}
	entry, err = store.GetDeviceSetting(context.Background(), "profile-1", "iphone", SubtitleAppearanceSettingKey)
	if err != nil {
		t.Fatalf("GetDeviceSetting after delete: %v", err)
	}
	if entry != nil {
		t.Fatalf("entry after delete = %#v, want nil", entry)
	}
}

func TestLegacyDeviceSettingsMirrorCanonicalRows(t *testing.T) {
	store := newProfileTestStore(t)
	handler := NewSettingsHandler(testUserStoreProvider{store: store})
	send := func(method, key string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/settings/device/"+key, bytes.NewReader(body))
		req = withRouteParams(req, map[string]string{"key": key})
		req.Header.Set(deviceIDHeader, "living-room")
		req = req.WithContext(apimw.SetProfileID(
			apimw.SetClaims(req.Context(), &auth.Claims{UserID: 7}), "profile-1"))
		rec := httptest.NewRecorder()
		if method == http.MethodPut {
			handler.HandleSetDeviceSetting(rec, req)
		} else {
			handler.HandleDeleteDeviceSetting(rec, req)
		}
		return rec
	}
	canonical := func(key string) *userstore.SettingValue {
		t.Helper()
		value, err := store.GetSettingValue(context.Background(), userstore.SettingIdentity{
			Key: key, Scope: settingscontract.ScopeProfileDevice,
			ProfileID: "profile-1", DeviceID: "living-room",
		})
		if err != nil {
			t.Fatalf("GetSettingValue(%s): %v", key, err)
		}
		return value
	}

	appearance := `{"fontSize":"large"}`
	body, _ := json.Marshal(setSettingRequest{Value: appearance})
	if rec := send(http.MethodPut, SubtitleAppearanceSettingKey, body); rec.Code != http.StatusNoContent {
		t.Fatalf("appearance PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if value := canonical("playback.subtitle_appearance"); value == nil || string(value.Value) != appearance {
		t.Fatalf("canonical appearance = %+v", value)
	}

	if rec := send(http.MethodPut, "playback.preferred_quality", []byte(`{"value":"1080p-high"}`)); rec.Code != http.StatusNoContent {
		t.Fatalf("quality PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if value := canonical("playback.preferred_quality"); value == nil || string(value.Value) != `"1080p"` {
		t.Fatalf("canonical quality = %+v", value)
	}
	if value := canonical("playback.max_bitrate_kbps"); value == nil || string(value.Value) != `10000` {
		t.Fatalf("canonical bitrate = %+v", value)
	}
	if rec := send(http.MethodPut, "playback.preferred_quality", []byte(`{"value":"auto"}`)); rec.Code != http.StatusNoContent {
		t.Fatalf("quality auto PUT = %d: %s", rec.Code, rec.Body.String())
	}
	if value := canonical("playback.max_bitrate_kbps"); value != nil {
		t.Fatalf("stale bitrate survived auto: %+v", value)
	}
	if rec := send(http.MethodDelete, "playback.preferred_quality", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("quality DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	if value := canonical("playback.preferred_quality"); value != nil {
		t.Fatalf("quality survived delete: %+v", value)
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
	canonicalBedroom, err := store1.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       "playback.subtitle_mode",
		Scope:     settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1",
		DeviceID:  "bedroom",
	}, json.RawMessage(`"always"`))
	if err != nil {
		t.Fatalf("UpsertSettingValue canonical bedroom override: %v", err)
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
	if bedroom.OverrideCount != 1 || bedroom.ProfileCount != 1 || bedroom.DeviceName != "Bedroom TV" ||
		bedroom.LastUpdated != canonicalBedroom.UpdatedAt {
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
	if detailResp.DeviceName != "Bedroom TV" || detailResp.OverrideCount != 1 ||
		detailResp.LastUpdated != canonicalBedroom.UpdatedAt {
		t.Fatalf("registered detail response = %#v", detailResp)
	}
	if len(detailResp.Settings) != 0 {
		t.Fatalf("registered detail settings len = %d, want 0", len(detailResp.Settings))
	}
	if len(detailResp.Profiles) != 1 || detailResp.Profiles[0].ProfileID != "profile-1" {
		t.Fatalf("registered detail profiles = %#v", detailResp.Profiles)
	}
}

func TestAdminDeviceSummaryDeduplicatesMirroredLegacyAlias(t *testing.T) {
	for name, keys := range map[string][2]string{
		"subtitle appearance": {SubtitleAppearanceSettingKey, settingskeys.PlaybackSubtitleAppearance},
		"theme":               {"ui_theme", "ui.theme"},
	} {
		t.Run(name, func(t *testing.T) {
			summaries := buildAdminDeviceSummaries(7, "user", "user@example.com",
				[]userstore.DeviceSettingEntry{{
					ProfileID: "profile-1", DeviceID: "living-room", Key: keys[0],
					UpdatedAt: "2026-07-30T01:00:00Z",
				}},
				[]userstore.SettingValue{{
					SettingIdentity: userstore.SettingIdentity{
						Key: keys[1], Scope: settingscontract.ScopeProfileDevice,
						ProfileID: "profile-1", DeviceID: "living-room",
					},
					UpdatedAt: "2026-07-30T01:00:01Z",
				}}, nil, map[string]string{"profile-1": "Main"})
			if len(summaries) != 1 || summaries[0].OverrideCount != 1 ||
				len(summaries[0].Profiles) != 1 || summaries[0].Profiles[0].OverrideCount != 1 {
				t.Fatalf("mirrored alias summaries = %#v", summaries)
			}
		})
	}
}

// TestAdminDeviceSummaryCountsAMirroredPairOnce. The mirrored pair is one
// preference stored twice for the length of the overlap window, and every fleet
// number built on this — override totals, the count filters, the anomaly
// thresholds that flag a device as unusually customized — is describing
// preferences. A device whose only override is the intro prompt must not look
// twice as configured as one whose only override is HDR.
func TestAdminDeviceSummaryCountsAMirroredPairOnce(t *testing.T) {
	summaries := buildAdminDeviceSummaries(7, "user", "user@example.com", nil,
		[]userstore.SettingValue{
			{
				SettingIdentity: userstore.SettingIdentity{
					Key: settingskeys.PlaybackIntroSkipMode, Scope: settingscontract.ScopeProfileDevice,
					ProfileID: "profile-1", DeviceID: "living-room",
				},
				UpdatedAt: "2026-08-16T01:00:00Z",
			},
			{
				SettingIdentity: userstore.SettingIdentity{
					Key: settingskeys.PlaybackAutoSkipIntro, Scope: settingscontract.ScopeProfileDevice,
					ProfileID: "profile-1", DeviceID: "living-room",
				},
				UpdatedAt: "2026-08-16T01:00:00Z",
			},
		}, nil, map[string]string{"profile-1": "Main"})

	if len(summaries) != 1 {
		t.Fatalf("summaries = %#v, want one device", summaries)
	}
	if summaries[0].OverrideCount != 1 {
		t.Errorf("override_count = %d, want 1: the mirrored intro pair is one preference",
			summaries[0].OverrideCount)
	}
	if len(summaries[0].Profiles) != 1 || summaries[0].Profiles[0].OverrideCount != 1 {
		t.Errorf("per-profile summary = %#v, want one override", summaries[0].Profiles)
	}
}

func withRouteParams(req *http.Request, params map[string]string) *http.Request {
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
