package apiv2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeDownloadCreation struct {
	row                *downloads.Download
	page               downloads.CreatePage
	req                downloads.CreateRequest
	user, limit, calls int
	season             *int
	after              *catalogpkg.EpisodePagePosition
	err                error
}

func (f *fakeDownloadCreation) Create(_ context.Context, user int, req downloads.CreateRequest, _ catalogpkg.AccessFilter) (*downloads.Download, error) {
	f.calls++
	f.user = user
	f.req = req
	return f.row, f.err
}
func (f *fakeDownloadCreation) CreateSeriesPage(_ context.Context, user int, req downloads.CreateRequest, season *int, after *catalogpkg.EpisodePagePosition, limit int, _ catalogpkg.AccessFilter) (downloads.CreatePage, error) {
	f.calls++
	f.user = user
	f.req = req
	f.season = season
	f.after = after
	f.limit = limit
	return f.page, f.err
}
func TestDownloadCreateTransport(t *testing.T) {
	svc := &fakeDownloadCreation{row: &downloads.Download{ID: "entry", ContentID: "movie", MediaFileID: 42, Revision: 1, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, page: downloads.CreatePage{BatchID: "intent", Skipped: []downloads.SkippedDownload{{EpisodeID: "no-file", Reason: "no_file"}}, Next: &catalogpkg.EpisodePagePosition{SeasonNumber: 0, EpisodeNumber: 1, ContentID: "no-file"}}}
	deps := pilotDeps(nil, nil)
	deps.DownloadCreation = svc
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads"
	rec := do(t, h, "POST", path, `{"content_id":"movie","media_file_id":"42","expected_revision":0}`, device)
	if rec.Code != 202 || svc.user != 1 || svc.req.ProfileID != "p-owner" || svc.req.DeviceID != "device-one" || svc.req.FileID != 42 || svc.req.ExpectedRevision == nil || *svc.req.ExpectedRevision != 0 || !svc.req.StrictIdentity {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), svc)
	}
	rec = do(t, h, "POST", path, `{"content_id":"movie","media_file_id":"42","expected_revision":0,"quality":"1mbps"}`, device)
	if rec.Code != 202 || svc.req.Quality != "1mbps" {
		t.Fatalf("quality %d %s", rec.Code, rec.Body.String())
	}
	var single DownloadCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &single); err != nil {
		t.Fatal(err)
	}
	if len(single.Items) != 1 || single.Items[0].MediaFileID != "42" {
		t.Fatal(single)
	}
	body := `{"content_id":"series","series":true,"season_number":0,"batch_id":"intent"}`
	rec = do(t, h, "POST", path+"?limit=1", body, device)
	var page DownloadCreated
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 202 || len(page.Items) != 0 || len(page.Skipped) != 1 || !page.Page.HasMore || svc.season == nil || *svc.season != 0 || svc.limit != 1 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), svc)
	}
	rec = do(t, h, "POST", path+"?cursor="+page.Page.NextCursor, body, device)
	if rec.Code != 202 || svc.after == nil || svc.after.ContentID != "no-file" {
		t.Fatalf("cursor %d %+v", rec.Code, svc)
	}
	rec = do(t, h, "POST", path+"?cursor="+page.Page.NextCursor, `{"content_id":"other","series":true,"batch_id":"intent"}`, device)
	if rec.Code != 400 {
		t.Fatalf("scope %d", rec.Code)
	}
	for _, body := range []string{`{"content_id":"movie","expected_revision":1}`, `{"content_id":"movie","expected_revision":0,"expected_download_id":"old"}`, `{"content_id":"series","series":true,"batch_id":"intent","expected_entries":{"episode":{"revision":1}}}`, `{"content_id":"movie"}`, `{"content_id":"movie","media_file_id":"042","expected_revision":0}`, `{"content_id":"movie","expected_revision":0,"season_number":0}`, `{"content_id":"series","series":true}`, `{"content_id":"movie","expected_revision":0,"caps":{"video_evidence":"typo","codecs_video":[],"codecs_audio":[],"containers":[],"max_resolution":"1080p","hdr":false}}`} {
		before := svc.calls
		rec = do(t, h, "POST", path, body, device)
		if rec.Code != 400 || svc.calls != before {
			t.Fatalf("invalid %s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, "POST", path, `{"content_id":"movie"}`, viewer)
	if rec.Code != 202 || svc.req.DeviceID != "" {
		t.Fatalf("ephemeral %d", rec.Code)
	}
	svc.err = downloads.ErrStatusConflict
	rec = do(t, h, "POST", path, `{"content_id":"movie","expected_revision":1,"expected_download_id":"entry"}`, device)
	if rec.Code != 409 {
		t.Fatalf("conflict %d", rec.Code)
	}
	svc.err = downloads.ErrPeriodLimitReached
	rec = do(t, h, "POST", path, `{"content_id":"movie","expected_revision":0}`, device)
	if rec.Code != 429 {
		t.Fatalf("quota %d", rec.Code)
	}
}

func TestDownloadCreateRejectsEmptyBatchGuardsOnSingle(t *testing.T) {
	svc := &fakeDownloadCreation{err: downloads.ErrStatusConflict}
	deps := pilotDeps(nil, nil)
	deps.DownloadCreation = svc
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	for _, tc := range []struct {
		name, body string
		managed    bool
	}{
		{"absence", `{"content_id":"movie","expected_revision":0,"expected_entries":{}}`, true},
		{"replacement", `{"content_id":"movie","expected_revision":1,"expected_download_id":"old","expected_entries":{}}`, true},
		{"ephemeral", `{"content_id":"movie","expected_entries":{}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := viewer
			if tc.managed {
				headers = device
			}
			before := svc.calls
			rec := do(t, h, "POST", Prefix+"/downloads", tc.body, headers)
			if rec.Code != 400 || svc.calls != before {
				t.Fatalf("batch guards reached single creation: status=%d calls=%d body=%s", rec.Code, svc.calls-before, rec.Body.String())
			}
		})
	}
	// Without batch guards, preserve the service's single-entry conflict result.
	rec := do(t, h, "POST", Prefix+"/downloads", `{"content_id":"movie","expected_revision":0}`, device)
	if rec.Code != 409 || svc.req.ExpectedEntries != nil {
		t.Fatalf("single guard dispatch: status=%d request=%+v", rec.Code, svc.req)
	}
	// An explicit empty object remains valid for bounded series creation.
	svc.err = nil
	rec = do(t, h, "POST", Prefix+"/downloads", `{"content_id":"series","series":true,"batch_id":"intent","expected_entries":{}}`, device)
	if rec.Code != 202 || svc.req.ExpectedEntries == nil {
		t.Fatalf("series guard dispatch: status=%d request=%+v body=%s", rec.Code, svc.req, rec.Body.String())
	}
}
