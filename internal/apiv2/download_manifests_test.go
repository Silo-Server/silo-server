package apiv2

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeDownloadManifests struct {
	row                 *downloads.OfflineManifest
	page                downloads.ManifestPage
	user                int
	profile, device, id string
	limit               int
	after               *downloads.RegistryPosition
	err                 error
}

func (f *fakeDownloadManifests) BuildManifest(_ context.Context, user int, profile, device, id string, _ catalogpkg.AccessFilter) (*downloads.OfflineManifest, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	return f.row, f.err
}
func (f *fakeDownloadManifests) PageBatchManifests(_ context.Context, user int, profile, device, id string, after *downloads.RegistryPosition, limit int, _ catalogpkg.AccessFilter) (downloads.ManifestPage, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	f.limit = limit
	f.after = after
	return f.page, f.err
}
func syntheticDownloadManifest() *downloads.OfflineManifest {
	row := &downloads.OfflineManifest{DownloadID: "entry", ContentID: "movie", MediaFileID: 42, Revision: 2, Title: "Synthetic film", Quality: "original", EffectiveQuality: "original", DeliveryFormat: "original", FileSize: 1024, GeneratedAt: "2026-01-02T03:04:05Z", ManifestVersion: 2}
	row.ArtworkURLs.Poster = "/api/v2/downloads/entry/artwork/poster"
	row.Subtitles = []downloads.OfflineSubtitle{{Language: "en", Format: "vtt", FetchURL: "/api/v2/downloads/entry/subtitles/external:0"}}
	row.StableIdentity = downloads.OfflineIdentity{StableType: "movie", ProviderIDs: map[string]string{"tmdb": "42"}}
	row.Chapters = []downloads.OfflineChapter{{Index: 0, Title: "Chapter", StartSeconds: 0, EndSeconds: 30}}
	return row
}
func TestDownloadManifestProjectionAndBound(t *testing.T) {
	row := syntheticDownloadManifest()
	out, err := downloadManifestOf(row)
	if err != nil || out.MediaFileID != "42" || out.ManifestVersion != 3 || out.Title != row.Title || out.Revision != 2 || len(out.Chapters) != 1 || out.StableIdentity.ProviderIDs["tmdb"] != "42" {
		t.Fatalf("%+v %v", out, err)
	}
	if out.ArtworkURLs.Poster != "/api/v2/downloads/entry/artwork/poster" || out.Subtitles[0].FetchURL != "/api/v2/downloads/entry/subtitles/external:0" {
		t.Fatalf("%+v", out)
	}
	if row.Subtitles[0].FetchURL != "/api/v2/downloads/entry/subtitles/external:0" {
		t.Fatal("changed bridge manifest")
	}
	row.Subtitles[0].FetchURL = "https://unexpected.invalid/subtitle"
	if _, err := downloadManifestOf(row); err == nil {
		t.Fatal("unexpected remote reference propagated")
	}
	row = syntheticDownloadManifest()
	row.Overview = strings.Repeat("x", maxDownloadManifestBytes)
	if _, err := downloadManifestOf(row); err == nil || manifestProjectionProblem(err).Status != 413 {
		t.Fatalf("bound: %v", err)
	}
}
func TestDownloadManifestTransport(t *testing.T) {
	service := &fakeDownloadManifests{row: syntheticDownloadManifest()}
	deps := pilotDeps(nil, nil)
	deps.DownloadManifests = service
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/entry/manifest"
	rec := do(t, h, "GET", path, "", device)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if service.user != 1 || service.profile != "p-owner" || service.device != "device-one" || service.id != "entry" {
		t.Fatalf("%+v", service)
	}
	rec = do(t, h, "GET", path, "", viewer)
	if rec.Code != 422 {
		t.Fatalf("device: %d", rec.Code)
	}
	service.row.Overview = strings.Repeat("x", maxDownloadManifestBytes)
	rec = do(t, h, "GET", path, "", device)
	if rec.Code != 413 {
		t.Fatalf("bound: %d %s", rec.Code, rec.Body.String())
	}
	service.err = catalogpkg.ErrItemNotFound
	rec = do(t, h, "GET", path, "", device)
	if rec.Code != 404 {
		t.Fatalf("hidden: %d", rec.Code)
	}
}
func TestDownloadManifestSkippedPageCursor(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	service := &fakeDownloadManifests{page: downloads.ManifestPage{Skipped: []downloads.SkippedManifest{{DownloadID: "revoked", Reason: "revoked"}}, Next: &downloads.RegistryPosition{CreatedAt: at, ID: "revoked"}}}
	deps := pilotDeps(nil, nil)
	deps.DownloadManifests = service
	h := newTestHandler(t, deps)
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/batches/batch/manifests"
	rec := do(t, h, "GET", path+"?limit=1", "", viewer)
	var page DownloadManifestPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || len(page.Items) != 0 || len(page.Skipped) != 1 || !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("%d %s %v", rec.Code, rec.Body.String(), err)
	}
	oversized := syntheticDownloadManifest()
	oversized.Overview = strings.Repeat("x", maxDownloadManifestBytes)
	service.page = downloads.ManifestPage{Items: []*downloads.OfflineManifest{oversized}}
	rec = do(t, h, "GET", path+"?cursor="+page.Page.NextCursor, "", viewer)
	if rec.Code != 200 || service.after == nil || service.after.ID != "revoked" || !strings.Contains(rec.Body.String(), `"reason":"too_large"`) {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), service.after)
	}
	rec = do(t, h, "GET", Prefix+"/downloads/batches/other/manifests?cursor="+page.Page.NextCursor, "", viewer)
	if rec.Code != 400 {
		t.Fatalf("foreign batch: %d", rec.Code)
	}
	rec = do(t, h, "GET", path+"?limit=11", "", viewer)
	if rec.Code != 422 {
		t.Fatalf("page cap: %d", rec.Code)
	}
}
