package apiv2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeDownloadRegistry struct {
	rows                []*downloads.Download
	user                int
	profile, device, id string
	event               downloads.StatusEvent
	after               *downloads.RegistryPosition
	limit               int
	err                 error
}

func (f *fakeDownloadRegistry) Capability(context.Context, int) (downloads.Capability, error) {
	return downloads.Capability{Enabled: true, DownloadAllowed: true, QualityPresets: []string{"original"}}, f.err
}
func (f *fakeDownloadRegistry) ListPage(_ context.Context, user int, profile, device string, after *downloads.RegistryPosition, limit int) ([]*downloads.Download, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.after = after
	f.limit = limit
	return f.rows, f.err
}
func (f *fakeDownloadRegistry) ReportStatus(_ context.Context, user int, profile, device, id string, event downloads.StatusEvent) (*downloads.Download, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	f.event = event
	if f.err != nil {
		return nil, f.err
	}
	return &downloads.Download{ID: id, UserID: user, ProfileID: profile, DeviceID: device, ContentID: "movie", MediaFileID: 42, Status: event.Status, Revision: event.Revision, StatusEventAt: &event.UpdatedAt, CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}, nil
}
func (f *fakeDownloadRegistry) Delete(_ context.Context, user int, profile, device, id string) error {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	return f.err
}
func TestDownloadRegistryTransport(t *testing.T) {
	service := &fakeDownloadRegistry{}
	deps := pilotDeps(nil, nil)
	deps.Downloads = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/entry"
	body := `{"status":"completed","updated_at":"2026-01-02T03:04:05.000Z","revision":2}`
	rec := do(t, h, "PATCH", path, body, device)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if service.user != 1 || service.profile != "p-owner" || service.device != "device-one" || service.id != "entry" || service.event.Revision != 2 || service.event.UpdatedAt.Format(time.RFC3339) != "2026-01-02T03:04:05Z" {
		t.Fatalf("%+v", service)
	}
	var row DownloadEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &row); err != nil || row.MediaFileID != "42" || row.StatusEventAt == nil {
		t.Fatalf("%+v %v", row, err)
	}
	rec = do(t, h, "PATCH", path, body, viewer)
	if rec.Code != 422 {
		t.Fatalf("device required: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "PATCH", path, `{"status":"completed","revision":2}`, device)
	if rec.Code != 422 {
		t.Fatalf("event required: %d", rec.Code)
	}
	for _, tc := range []struct {
		err  error
		want int
	}{{downloads.ErrNotFound, 404}, {downloads.ErrStatusConflict, 409}, {downloads.ErrInvalidStatusEvent, 400}} {
		service.err = tc.err
		rec = do(t, h, "PATCH", path, body, device)
		if rec.Code != tc.want {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	service.err = nil
	rec = do(t, h, "DELETE", path, "", device)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "GET", Prefix+"/capabilities/downloads", "", viewer)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var capability DownloadCapability
	if err := json.Unmarshal(rec.Body.Bytes(), &capability); err != nil || !capability.OrderedStatus || capability.Revision == "" {
		t.Fatalf("%+v %v", capability, err)
	}
}
func TestDownloadRegistryCursorBoundary(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service := &fakeDownloadRegistry{rows: []*downloads.Download{{ID: "b", CreatedAt: at}, {ID: "a", CreatedAt: at}}}
	deps := pilotDeps(nil, nil)
	deps.Downloads = service
	h := newTestHandler(t, deps)
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads"
	rec := do(t, h, "GET", path+"?limit=1", "", viewer)
	var page Collection[DownloadEntry]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || len(page.Items) != 1 || page.Page.NextCursor == "" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), err)
	}
	service.rows = nil
	rec = do(t, h, "GET", path+"?cursor="+page.Page.NextCursor, "", viewer)
	if rec.Code != 200 || service.after == nil || service.after.ID != "b" || service.limit != 51 {
		t.Fatalf("%d %+v", rec.Code, service)
	}
	rec = do(t, h, "GET", path+"?cursor="+page.Page.NextCursor, "", with(viewer, "X-Silo-Device-Id", "other-device"))
	if rec.Code != 400 {
		t.Fatalf("cursor crossed device: %d", rec.Code)
	}
	for _, limit := range []string{"0", "101"} {
		rec = do(t, h, "GET", path+"?limit="+limit, "", viewer)
		if rec.Code != 422 {
			t.Fatalf("%s: %d", limit, rec.Code)
		}
	}
	rec = do(t, h, "GET", path, "", nil)
	if rec.Code != 401 {
		t.Fatalf("auth: %d", rec.Code)
	}
}
