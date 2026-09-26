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
		{DeviceEntry: userstore.DeviceEntry{ProfileID: a.ProfileID, DeviceID: "d-1", LastSeenAt: "2026-01-02T03:04:05.123456Z"}, ProfileName: "Viewer", ChangedCount: 1},
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
	if len(page.Items) != 1 || !page.Items[0].IsCurrentDevice || !page.Page.HasMore || page.Page.NextCursor == "" {
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
func TestDeviceSettingsGates(t *testing.T) {
	f := &fakeDeviceSettings{}
	deps := pilotDeps(nil, nil)
	deps.DeviceSettings = f
	h := newTestHandler(t, deps)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path := "/api/v2/devices"
		if method == http.MethodDelete {
			path += "/d"
		}
		rec := do(t, h, method, path, "", nil)
		if rec.Code != 401 || f.calls != 0 {
			t.Fatalf("unauthenticated: %d", rec.Code)
		}
	}
	rec := do(t, h, http.MethodGet, "/api/v2/devices?scope=other", "", viewerHeaders())
	if rec.Code != 422 || f.calls != 0 {
		t.Fatalf("invalid scope: %d", rec.Code)
	}
}
