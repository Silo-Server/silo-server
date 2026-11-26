package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// deviceCases are the device a request declares: another of the person's
// devices, one the app made up, and none.
var deviceCases = []struct {
	name    string
	headers map[string]string
}{
	{"another device's id", map[string]string{"X-Silo-Device-Id": "living-room-tv", "X-Silo-Device-Name": "Living room", "X-Silo-Device-Platform": "tvOS", "X-Device-ID": "living-room-tv"}},
	{"an invented id", map[string]string{"X-Silo-Device-Id": "app-made-up", "X-Device-ID": "app-made-up"}},
	{"no id", map[string]string{}},
}

// requestHeaders is a session's request acting as p-owner with the case's
// device headers.
func requestHeaders(token string, device map[string]string) map[string]string {
	h := with(bearer(token), "X-Profile-Id", "p-owner")
	for k, v := range device {
		h[k] = v
	}
	return h
}

// TestGrantedAppDeclaresNoDeviceToPlayback: a session another app's key
// opened cannot tell the server which of the person's devices it is, so
// whatever it declares, a playback start records no device in the profile's
// device registry, and start, replan and route events key no connection
// record, under another device's id, an invented one, or none. A password
// session and a device signed in by code still declare theirs. Catches the
// gate's strip of the declared device going missing, which let the app
// rename another device's registry entry or clear its learned limit.
func TestGrantedAppDeclaresNoDeviceToPlayback(t *testing.T) {
	deps, _ := catalogDeps(t)
	fake := &fakePlaybackService{}
	data, err := os.ReadFile("../playback/testdata/protocol_v3/decision_response.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fake.response); err != nil {
		t.Fatal(err)
	}
	deps.Playback = fake
	h := newTestHandler(t, deps)
	event := map[string]any{"installation_id": playbackTestInstallation, "event_id": playbackTestEvent, "protocol_version": 3, "playback_attempt_id": "attempt-0123456789", "session_id": "11111111-1111-4111-8111-111111111111", "event": "first_frame", "diagnostics": map[string]string{"decoder_name": "synthetic"}}
	routes := []struct{ name, path, body string }{
		{"start", "/playback/start", playbackJSON(t, playbackStartFixture(t))},
		{"replan", "/playback/11111111-1111-4111-8111-111111111111/replan", playbackJSON(t, playbackReplanFixture(t))},
		{"route event", "/playback/route-events", playbackJSON(t, event)},
	}
	for _, route := range routes {
		for _, device := range deviceCases {
			for _, token := range []string{grantedAppToken, memberToken, deviceLoginToken} {
				t.Run(route.name+"/"+device.name+"/"+token, func(t *testing.T) {
					fake.caller = handlers.PlaybackCaller{}
					rec := do(t, h, http.MethodPost, Prefix+route.path, route.body, requestHeaders(token, device.headers))
					if rec.Code < 200 || rec.Code > 299 {
						t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
					}
					got := fake.caller
					want := device.headers["X-Silo-Device-Id"]
					if token == grantedAppToken {
						want = ""
					}
					if got.ConnectionDeviceID != want || got.DeviceID != device.headers["X-Device-ID"] && token != grantedAppToken || token == grantedAppToken && got.DeviceID != "" {
						t.Fatalf("connection device %q, playback device %q", got.ConnectionDeviceID, got.DeviceID)
					}
					if route.name == "start" && got.DeclaredDevice.DeviceID != want {
						t.Fatalf("registry device %q, want %q", got.DeclaredDevice.DeviceID, want)
					}
				})
			}
		}
	}
}

// deviceRecordingSettings records the device every settings read resolves
// under.
type deviceRecordingSettings struct {
	*fakeSettingValuesSeam
	devices []handlers.DeviceMetadata
}

func (f *deviceRecordingSettings) ResolveEffectiveSettings(ctx context.Context, userID int, q handlers.EffectiveSettingsQuery) ([]handlers.EffectiveSettingValueView, error) {
	f.devices = append(f.devices, q.Device)
	return f.fakeSettingValuesSeam.ResolveEffectiveSettings(ctx, userID, q)
}

func (f *deviceRecordingSettings) ResolveEffectiveSettingContexts(ctx context.Context, userID int, q handlers.EffectiveSettingsQuery, contexts []handlers.EffectiveContextRequest) ([]handlers.EffectiveSettingContextView, error) {
	f.devices = append(f.devices, q.Device)
	return f.fakeSettingValuesSeam.ResolveEffectiveSettingContexts(ctx, userID, q, contexts)
}

type deviceRecordingAppearance struct {
	*fakeSettingsSeam
	devices []handlers.DeviceMetadata
}

func (f *deviceRecordingAppearance) EffectiveSubtitleAppearance(ctx context.Context, userID int, profileID string, device handlers.DeviceMetadata) (handlers.EffectiveSubtitleAppearanceView, error) {
	f.devices = append(f.devices, device)
	return f.fakeSettingsSeam.EffectiveSubtitleAppearance(ctx, userID, profileID, device)
}

// TestGrantedAppDeclaresNoDeviceToSettings: the settings reads register the
// declared device and resolve its device-scoped values, so for a session
// another app's key opened every one of them resolves under no device,
// whatever it declares, and naming another device or profile by parameter is
// refused alike for one that exists and one that doesn't. A password session
// and a device signed in by code resolve under theirs. Catches the strip
// going missing, which let the app read another device's settings and
// register itself in the device registry.
func TestGrantedAppDeclaresNoDeviceToSettings(t *testing.T) {
	deps := pilotDeps(nil, nil)
	values := &deviceRecordingSettings{fakeSettingValuesSeam: deps.SettingValues.(*fakeSettingValuesSeam)}
	appearance := &deviceRecordingAppearance{fakeSettingsSeam: deps.Settings.(*fakeSettingsSeam)}
	deps.SettingValues, deps.Settings = values, appearance
	h := newTestHandler(t, deps)
	reads := []struct {
		name, method, path, body string
		devices                  *[]handlers.DeviceMetadata
	}{
		{"effective settings", http.MethodGet, "/settings/values/effective?keys=ui.theme", "", &values.devices},
		{"resolved settings", http.MethodPost, "/settings/values/effective", `{"keys":["ui.theme"],"contexts":[{"context_id":"row-1","series_id":"tv:12345"}]}`, &values.devices},
		{"subtitle appearance", http.MethodGet, "/settings/subtitle-appearance/effective", "", &appearance.devices},
	}
	for _, read := range reads {
		for _, device := range deviceCases {
			for _, token := range []string{grantedAppToken, memberToken, deviceLoginToken} {
				t.Run(read.name+"/"+device.name+"/"+token, func(t *testing.T) {
					*read.devices = nil
					rec := do(t, h, read.method, Prefix+read.path, read.body, requestHeaders(token, device.headers))
					if rec.Code != http.StatusOK || len(*read.devices) != 1 {
						t.Fatalf("status %d, %d reads: %s", rec.Code, len(*read.devices), rec.Body.String())
					}
					want := device.headers["X-Silo-Device-Id"]
					if token == grantedAppToken {
						want = ""
					}
					if got := (*read.devices)[0]; got.DeviceID != want || token == grantedAppToken && (got.DeviceName != "" || got.DevicePlatform != "") {
						t.Fatalf("resolved under %+v, want device %q", got, want)
					}
				})
			}
		}
	}
	// Naming another device or profile: the same refusal for one that
	// exists and one that doesn't, and the person's own sessions unchanged.
	for _, query := range []string{"device_id=tv-1", "device_id=no-such-device", "profile_id=p-kid", "profile_id=999"} {
		for _, path := range []string{"/settings/values/effective?keys=ui.theme&", "/settings/values?keys=ui.theme&scope=profile&", "/settings/values/ui.theme?scope=profile&"} {
			t.Run("named "+query+" "+path, func(t *testing.T) {
				p := requireProblem(t, do(t, h, http.MethodGet, Prefix+path+query, "", requestHeaders(grantedAppToken, nil)), TypePermissionDenied)
				if p.Detail != handlers.SessionNotAllowedDetail {
					t.Fatalf("detail %q", p.Detail)
				}
				if rec := do(t, h, http.MethodGet, Prefix+path+query, "", requestHeaders(memberToken, nil)); rec.Code == http.StatusForbidden {
					var own problemDoc
					if json.Unmarshal(rec.Body.Bytes(), &own) == nil && own.Detail == handlers.SessionNotAllowedDetail {
						t.Fatalf("password session refused as another app: %s", rec.Body.String())
					}
				}
			})
		}
	}
}
