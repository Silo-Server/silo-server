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
	return downloads.Capability{
		Enabled: true, DownloadAllowed: true, TranscodeEnabled: true, TranscodeUserAllowed: true,
		QualityPresets: []string{"original", "10mbps", "2mbps"},
		QualityOptions: []downloads.QualityOption{
			{Preset: "original"},
			{Preset: "10mbps", BitrateKbps: 10_000, MaxHeight: 1080},
			{Preset: "2mbps", BitrateKbps: 2_000, MaxHeight: 720},
		},
	}, f.err
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
