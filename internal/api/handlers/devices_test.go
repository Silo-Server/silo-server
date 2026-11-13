package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
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

// TestDeviceSettingsPage_CountsAMirroredPairOnce. The intro-skip preference
// is stored under two keys while old clients are in the field, and the badge
// on the device list is meant to tell a household how much this device does
// differently, not how many rows the compatibility mirror needed to say it.
// Counting rows would also make the badge drop by one on every affected
// device the day the mirror is retired, which reads as a change nobody made.
func TestDeviceSettingsPage_CountsAMirroredPairOnce(t *testing.T) {
	handler, store := newDevicesTestHandler(t)
	seedDevice(t, store, "profile-1", "device-1", "Laptop")
	seedDeviceValue(t, store, "profile-1", "device-1", settingskeys.PlaybackIntroSkipMode, `"never"`)
	seedDeviceValue(t, store, "profile-1", "device-1", settingskeys.PlaybackAutoSkipIntro, `false`)
	seedDeviceValue(t, store, "profile-1", "device-1", "player.hdr_enabled", `false`)

	ctx := devicesRequest(http.MethodGet, "/devices", "profile-1").Context()
	page, err := handler.DeviceSettingsPage(ctx, DeviceSettingsActor{UserID: 1, ProfileID: "profile-1"}, false, userstore.DevicePageOptions{Limit: 10})
	if err != nil {
		t.Fatalf("DeviceSettingsPage: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("returned %d devices, want 1: %+v", len(page), page)
	}
	if page[0].ChangedCount != 2 {
		t.Errorf("changed_count = %d, want 2: the mirrored intro pair is one preference", page[0].ChangedCount)
	}
}
