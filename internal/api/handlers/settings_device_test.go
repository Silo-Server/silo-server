package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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

// TestEffectiveSubtitleAppearanceReadsTheCanonicalSetting holds the
// subtitle-appearance route to the value clients now store:
// playback.subtitle_appearance, a device override over the profile's value.
func TestEffectiveSubtitleAppearanceReadsTheCanonicalSetting(t *testing.T) {
	const profileValue = `{"fontSize":"xlarge"}`
	const deviceValue = `{"fontSize":"small"}`
	for _, tc := range []struct {
		name          string
		profile       string
		device        string
		requestDevice string
		wantGlobal    string
		wantDevice    string
		wantEffective string
	}{
		{name: "nothing stored"},
		{name: "profile value", profile: profileValue, requestDevice: "device-1",
			wantGlobal: profileValue, wantEffective: profileValue},
		{name: "profile value without a device", profile: profileValue,
			wantGlobal: profileValue, wantEffective: profileValue},
		{name: "device override wins", profile: profileValue, device: deviceValue, requestDevice: "device-1",
			wantGlobal: profileValue, wantDevice: deviceValue, wantEffective: deviceValue},
		{name: "another device's override does not apply", profile: profileValue, device: deviceValue, requestDevice: "device-2",
			wantGlobal: profileValue, wantEffective: profileValue},
		{name: "device override alone", device: deviceValue, requestDevice: "device-1",
			wantDevice: deviceValue, wantEffective: deviceValue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values, store := newValuesTestHandler(t)
			ctx := seamCtx(t, "profile-1")
			if tc.profile != "" {
				seamSet(t, ctx, values, seamReq(settingskeys.PlaybackSubtitleAppearance, settingscontract.ScopeProfile), tc.profile)
			}
			if tc.device != "" {
				seamSet(t, ctx, values, seamReq(settingskeys.PlaybackSubtitleAppearance, settingscontract.ScopeProfileDevice), tc.device)
			}

			handler := NewSettingsHandler(testUserStoreProvider{store: store})
			got, err := handler.EffectiveSubtitleAppearance(ctx, seamUserID, "profile-1",
				DeviceMetadata{DeviceID: tc.requestDevice})
			if err != nil {
				t.Fatalf("EffectiveSubtitleAppearance: %v", err)
			}
			if got.GlobalValue != tc.wantGlobal || got.DeviceValue != tc.wantDevice || got.EffectiveValue != tc.wantEffective {
				t.Errorf("global, device, effective = %q, %q, %q; want %q, %q, %q",
					got.GlobalValue, got.DeviceValue, got.EffectiveValue, tc.wantGlobal, tc.wantDevice, tc.wantEffective)
			}
			if got.HasDeviceOverride != (tc.wantDevice != "") {
				t.Errorf("has_device_override = %v, want %v", got.HasDeviceOverride, tc.wantDevice != "")
			}
			if got.HasDeviceOverride && got.UpdatedAt == "" {
				t.Error("a device override carries no updated_at")
			}
			// device_id names the override's device and is absent without one.
			wantDeviceID := ""
			if tc.wantDevice != "" {
				wantDeviceID = tc.requestDevice
			}
			if got.DeviceID != wantDeviceID {
				t.Errorf("device_id = %q, want %q", got.DeviceID, wantDeviceID)
			}
		})
	}
}

// TestSubtitleAppearanceDeviceOverrideStoresWhatTheRouteReads: the companion
// PUT writes the override the effective route then serves, and refuses a value
// the canonical schema cannot hold rather than accepting a write no reader sees.
func TestSubtitleAppearanceDeviceOverrideStoresWhatTheRouteReads(t *testing.T) {
	_, store := newValuesTestHandler(t)
	ctx := seamCtx(t, "profile-1")
	handler := NewSettingsHandler(testUserStoreProvider{store: store})
	cmd := DeviceSettingCommand{
		UserID: seamUserID, ProfileID: "profile-1",
		Device: NewDeviceMetadata("device-1", "Living room", "iOS"), Key: SubtitleAppearanceSettingKey,
	}

	if err := handler.SetDeviceSetting(ctx, cmd, `{"fontSize":"small"}`); err != nil {
		t.Fatalf("SetDeviceSetting: %v", err)
	}
	for _, refused := range []string{`{}`, `{"fontSize":"huge"}`} {
		err := handler.SetDeviceSetting(ctx, cmd, refused)
		if apiErr := requireSettingStatus(t, err, http.StatusBadRequest, refused); apiErr.Field != "value" {
			t.Errorf("%s refused at %q, want value", refused, apiErr.Field)
		}
	}

	got, err := handler.EffectiveSubtitleAppearance(ctx, seamUserID, "profile-1", NewDeviceMetadata("device-1", "", ""))
	if err != nil {
		t.Fatalf("EffectiveSubtitleAppearance: %v", err)
	}
	if !got.HasDeviceOverride || got.EffectiveValue != `{"fontSize":"small"}` {
		t.Errorf("effective = %q (override %v), want the stored override", got.EffectiveValue, got.HasDeviceOverride)
	}
	if got.DeviceName != "Living room" || got.DevicePlatform != "iOS" {
		t.Errorf("device = %q/%q, want the name and platform the override was saved with", got.DeviceName, got.DevicePlatform)
	}
}
