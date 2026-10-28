package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

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

// TestSubtitleAppearanceDeviceOverrideRoundTrip: the device override is stored
// for the acting profile only, mirrored to its canonical profile_device row so
// both spellings agree, and cleared from both.
func TestSubtitleAppearanceDeviceOverrideRoundTrip(t *testing.T) {
	store := newProfileTestStore(t)
	ctx := context.Background()
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Guest"}); err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	handler := NewSettingsHandler(testUserStoreProvider{store: store})
	device := NewDeviceMetadata("iphone", "Example iPhone", "iOS")
	cmd := DeviceSettingCommand{UserID: 7, ProfileID: "profile-1", Device: device, Key: SubtitleAppearanceSettingKey}
	canonical := userstore.SettingIdentity{
		Key: settingskeys.PlaybackSubtitleAppearance, Scope: settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "iphone",
	}

	const appearance = `{"fontSize":"xxlarge"}`
	if err := handler.SetDeviceSetting(ctx, cmd, appearance); err != nil {
		t.Fatalf("SetDeviceSetting: %v", err)
	}
	entry, err := store.GetDeviceSetting(ctx, "profile-1", "iphone", SubtitleAppearanceSettingKey)
	if err != nil || entry == nil || entry.Value != appearance || entry.DevicePlatform != "iOS" {
		t.Fatalf("legacy entry = %#v (%v)", entry, err)
	}
	if value, err := store.GetSettingValue(ctx, canonical); err != nil || value == nil || string(value.Value) != appearance {
		t.Fatalf("canonical mirror = %+v (%v), want %s", value, err, appearance)
	}

	view, err := handler.EffectiveSubtitleAppearance(ctx, 7, "profile-1", device)
	if err != nil || !view.HasDeviceOverride || view.EffectiveValue != appearance || view.GlobalValue != "" {
		t.Fatalf("effective = %#v (%v)", view, err)
	}
	// Another profile on the same device does not see this profile's override.
	view, err = handler.EffectiveSubtitleAppearance(ctx, 7, "profile-2", device)
	if err != nil || view.HasDeviceOverride || view.EffectiveValue == appearance {
		t.Fatalf("sibling effective = %#v (%v), want no override", view, err)
	}

	if err := handler.DeleteDeviceSetting(ctx, cmd); err != nil {
		t.Fatalf("DeleteDeviceSetting: %v", err)
	}
	if entry, err := store.GetDeviceSetting(ctx, "profile-1", "iphone", SubtitleAppearanceSettingKey); err != nil || entry != nil {
		t.Fatalf("legacy entry after delete = %#v (%v), want nil", entry, err)
	}
	if value, err := store.GetSettingValue(ctx, canonical); err != nil || value != nil {
		t.Fatalf("canonical mirror after delete = %+v (%v), want nil", value, err)
	}
}

// TestAdminCanListAndInspectDevicesAcrossUsers: the admin fleet view spans
// every account, and a device that only registered and holds only canonical
// overrides still appears with its count and last change.
func TestAdminCanListAndInspectDevicesAcrossUsers(t *testing.T) {
	ctx := context.Background()
	store1 := newIsolatedProfileTestStore(t, "one")
	store2 := newIsolatedProfileTestStore(t, "two")
	for _, key := range []string{"player.playback_speed", "player.audio_sync_ms"} {
		if err := store1.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
			ProfileID: "profile-1", DeviceID: "living-room", DeviceName: "Living Room TV",
			DevicePlatform: "tvOS", Key: key, Value: "1",
		}); err != nil {
			t.Fatalf("SetDeviceSetting store1 %s: %v", key, err)
		}
	}
	if err := store1.(userstore.DeviceRegistry).RegisterDevice(ctx, userstore.DeviceEntry{
		ProfileID: "profile-1", DeviceID: "bedroom", DeviceName: "Bedroom TV", DevicePlatform: "Android TV",
	}); err != nil {
		t.Fatalf("RegisterDevice store1: %v", err)
	}
	canonicalBedroom, err := store1.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: "playback.subtitle_mode", Scope: settingscontract.ScopeProfileDevice,
		ProfileID: "profile-1", DeviceID: "bedroom",
	}, json.RawMessage(`"always"`))
	if err != nil {
		t.Fatalf("UpsertSettingValue canonical bedroom override: %v", err)
	}
	if err := store2.SetDeviceSetting(ctx, userstore.DeviceSettingEntry{
		ProfileID: "profile-1", DeviceID: "phone", DeviceName: "Travel Phone",
		DevicePlatform: "iOS", Key: "player.hdr_enabled", Value: "false",
	}); err != nil {
		t.Fatalf("SetDeviceSetting store2: %v", err)
	}

	handler := &AdminHandler{
		userRepo: testAdminUserRepo{users: map[int]*models.User{
			7:  {ID: 7, Username: "alice", Email: "alice@example.com"},
			11: {ID: 11, Username: "bob", Email: "bob@example.com"},
		}},
		storeProv: mappedTestUserStoreProvider{stores: map[int]userstore.UserStore{7: store1, 11: store2}},
	}

	devices, err := handler.ReadAdminDevices(ctx)
	if err != nil {
		t.Fatalf("ReadAdminDevices: %v", err)
	}
	if len(devices) != 3 {
		t.Fatalf("devices = %d, want 3: %#v", len(devices), devices)
	}
	var bedroom *AdminDeviceSummaryView
	for i := range devices {
		if devices[i].DeviceID == "bedroom" {
			bedroom = &devices[i]
		}
	}
	if bedroom == nil || bedroom.OverrideCount != 1 || bedroom.ProfileCount != 1 ||
		bedroom.DeviceName != "Bedroom TV" || bedroom.LastUpdated != canonicalBedroom.UpdatedAt {
		t.Fatalf("registered device summary = %#v", bedroom)
	}

	detail, err := handler.ReadAdminDevice(ctx, "7", "living-room")
	if err != nil || detail.Username != "alice" || detail.DeviceName != "Living Room TV" || len(detail.Settings) != 2 {
		t.Fatalf("living-room detail = %#v (%v)", detail, err)
	}
	detail, err = handler.ReadAdminDevice(ctx, "7", "bedroom")
	if err != nil || detail.DeviceName != "Bedroom TV" || detail.OverrideCount != 1 ||
		detail.LastUpdated != canonicalBedroom.UpdatedAt || len(detail.Settings) != 0 ||
		len(detail.Profiles) != 1 || detail.Profiles[0].ProfileID != "profile-1" {
		t.Fatalf("registered detail = %#v (%v)", detail, err)
	}
}

func withRouteParams(req *http.Request, params map[string]string) *http.Request {
	routeCtx := chi.NewRouteContext()
	for key, value := range params {
		routeCtx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}
