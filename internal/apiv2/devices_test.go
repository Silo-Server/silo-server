package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fakeDeviceSettings struct {
	calls                       int
	actor                       handlers.DeviceSettingsActor
	household                   bool
	options                     userstore.DevicePageOptions
	targetProfile, targetDevice string
	forget                      bool
	update                      handlers.DeviceUpdate
}

func (f *fakeDeviceSettings) DeviceSettingsPage(_ context.Context, a handlers.DeviceSettingsActor, h bool, o userstore.DevicePageOptions) ([]userstore.DeviceSettingsEntry, error) {
	f.calls++
	f.actor = a
	f.household = h
	f.options = o
	if o.After != nil {
		return []userstore.DeviceSettingsEntry{}, nil
	}
	return []userstore.DeviceSettingsEntry{
		{DeviceEntry: userstore.DeviceEntry{ProfileID: a.ProfileID, DeviceID: "d-1", DeviceName: "Apple TV", LastSeenAt: "2026-01-02T03:04:05.123456Z"}, CustomName: "Living Room", ProfileName: "Viewer", ChangedCount: 1},
		{DeviceEntry: userstore.DeviceEntry{ProfileID: a.ProfileID, DeviceID: "d-2", LastSeenAt: "2026-01-02T03:04:05.123456Z"}},
	}, nil
}
func (f *fakeDeviceSettings) RemoveDeviceSettings(_ context.Context, a handlers.DeviceSettingsActor, p, d string, forget bool) error {
	f.calls++
	f.actor = a
	f.targetProfile = p
	f.targetDevice = d
	f.forget = forget
	if d == "missing" {
		return &handlers.APIError{Status: 404, Code: "not_found", Message: "Device not found"}
	}
	return nil
}
func (f *fakeDeviceSettings) UpdateDevice(_ context.Context, a handlers.DeviceSettingsActor, p, d string, u handlers.DeviceUpdate) (userstore.DeviceSettingsEntry, error) {
	f.calls++
	f.actor = a
	f.targetProfile = p
	f.targetDevice = d
	f.update = u
	if d == "missing" {
		return userstore.DeviceSettingsEntry{}, &handlers.APIError{Status: 404, Code: "not_found", Message: "Device not found"}
	}
	entry := userstore.DeviceSettingsEntry{DeviceEntry: userstore.DeviceEntry{ProfileID: p, DeviceID: d, DeviceName: "Apple TV", DevicePlatform: "tvOS", LastSeenAt: "2026-01-02T03:04:05.123456Z"}, ProfileName: "Family"}
	if u.Rename && u.CustomName != nil {
		entry.CustomName = *u.CustomName
	}
	return entry, nil
}
func TestDeviceSettingsWire(t *testing.T) {
	f := &fakeDeviceSettings{}
	deps := pilotDeps(nil, nil)
	deps.DeviceSettings = f
	h := newTestHandler(t, deps)
	headers := viewerHeaders()
	headers["X-Silo-Device-Id"] = "d-1"
	rec := do(t, h, http.MethodGet, "/api/v2/devices?limit=1", "", headers)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var page DeviceSettingsCollection
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || !page.Items[0].IsCurrentDevice || !page.Page.HasMore || page.Page.NextCursor == "" ||
		page.Items[0].DeviceName != "Apple TV" || page.Items[0].CustomName != "Living Room" {
		t.Fatalf("page: %s", rec.Body.String())
	}
	if f.options.Limit != 2 || f.actor.ProfileID != "p-owner" || f.actor.UserID != 1 {
		t.Fatalf("seam: %+v", f)
	}
	rec = do(t, h, http.MethodGet, "/api/v2/devices?limit=1&cursor="+page.Page.NextCursor, "", headers)
	if rec.Code != 200 || f.options.After.LastSeenAt != "2026-01-02T03:04:05.123456Z" {
		t.Fatalf("precision: %d %+v", rec.Code, f.options)
	}
	before := f.calls
	rec = do(t, h, http.MethodGet, "/api/v2/devices?scope=household&cursor="+page.Page.NextCursor, "", headers)
	if rec.Code != 400 || f.calls != before {
		t.Fatalf("cursor scope: %d", rec.Code)
	}
	for _, suffix := range []string{"", "/settings"} {
		rec = do(t, h, http.MethodDelete, "/api/v2/devices/d-1"+suffix+"?profile_id=p-family", "", headers)
		if rec.Code != 204 || f.targetProfile != "p-family" || f.forget != (suffix == "") {
			t.Fatalf("mutation: %d %+v %s", rec.Code, f, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodDelete, "/api/v2/devices/missing", "", headers)
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
}

// updateDevice carries the three PATCH states of custom_name to the seam:
// a value renames, null clears, and omission changes nothing.
func TestUpdateDeviceWire(t *testing.T) {
	f := &fakeDeviceSettings{}
	deps := pilotDeps(nil, nil)
	deps.DeviceSettings = f
	h := newTestHandler(t, deps)
	headers := viewerHeaders()
	name := func(u handlers.DeviceUpdate) string {
		if !u.Rename {
			return "<unchanged>"
		}
		if u.CustomName == nil {
			return "<cleared>"
		}
		return *u.CustomName
	}
	for _, c := range []struct{ body, want string }{
		{`{"custom_name":"Living Room"}`, "Living Room"},
		{`{"custom_name":null}`, "<cleared>"},
		{`{}`, "<unchanged>"},
	} {
		rec := do(t, h, http.MethodPatch, "/api/v2/devices/d-1?profile_id=p-family", c.body, headers)
		if rec.Code != 200 || f.targetProfile != "p-family" || f.targetDevice != "d-1" || name(f.update) != c.want {
			t.Fatalf("%s: %d %q %s", c.body, rec.Code, name(f.update), rec.Body.String())
		}
		var got DeviceSettings
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.DeviceID != "d-1" || got.DeviceName != "Apple TV" || got.ProfileID != "p-family" || (c.want == "Living Room") != (got.CustomName == "Living Room") {
			t.Fatalf("%s: representation %s", c.body, rec.Body.String())
		}
	}
	before := f.calls
	for _, body := range []string{`{"custom_name":""}`, `{"custom_name":"` + strings.Repeat("é", 121) + `"}`} {
		if rec := do(t, h, http.MethodPatch, "/api/v2/devices/d-1", body, headers); rec.Code != 422 || f.calls != before {
			t.Fatalf("invalid name reached the seam: %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodPatch, "/api/v2/devices/d-1", `{"custom_name":"`+strings.Repeat("é", 120)+`"}`, headers); rec.Code != 200 {
		t.Fatalf("120 runes: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPatch, "/api/v2/devices/missing", `{"custom_name":"x"}`, headers); rec.Code != 404 {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
}
func TestDeviceSettingsGates(t *testing.T) {
	f := &fakeDeviceSettings{}
	deps := pilotDeps(nil, nil)
	deps.DeviceSettings = f
	h := newTestHandler(t, deps)
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPatch} {
		path := "/api/v2/devices"
		body := ""
		if method != http.MethodGet {
			path += "/d"
		}
		if method == http.MethodPatch {
			body = `{"custom_name":"x"}`
		}
		rec := do(t, h, method, path, body, nil)
		if rec.Code != 401 || f.calls != 0 {
			t.Fatalf("unauthenticated: %d", rec.Code)
		}
	}
	rec := do(t, h, http.MethodGet, "/api/v2/devices?scope=other", "", viewerHeaders())
	if rec.Code != 422 || f.calls != 0 {
		t.Fatalf("invalid scope: %d", rec.Code)
	}
}
