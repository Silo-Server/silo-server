package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/downloadstorage"
)

var storageFixtureAt = time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)

type fakeAdminDownloadStorage struct {
	lastFilter   downloads.StorageFileFilter
	lastDelete   []string
	includeInUse bool
	actor        int
	cleaned      string
}

func (*fakeAdminDownloadStorage) StorageOverview(context.Context) (*downloads.StorageOverview, error) {
	at := storageFixtureAt
	return &downloads.StorageOverview{
		CacheHours: 72, DiskCeilingPercent: 85, DefaultBudget: 750_000_000_000, StaleDeviceDays: 14,
		PreparingJobs: 3, FreedLast30Days: 2_700_000_000_000,
		Locations: []downloads.StorageLocationView{
			{
				Key: downloads.LocationServer, Name: "Server", Enabled: true, Online: true,
				Dir: "/srv/silo/download-artifacts", DirSource: downloads.StorageSourceSetting,
				Usage:      &downloadstorage.Usage{MeasuredAt: at, Files: 312, Bytes: 214_000_000_000, FSUsedBytes: 1_220_000_000_000, FSTotalBytes: 2_000_000_000_000, FSType: "ext4"},
				InUseFiles: 230, InUseBytes: 162_000_000_000, CachedFiles: 82, CachedBytes: 52_000_000_000,
				WaitingDownloads: 41, Budget: 750_000_000_000, BudgetSource: downloads.StorageSourceSetting, ReconciledAt: &at,
			},
			{
				Key: downloads.NodeLocationKey(9), Name: "node-gpu-1", NodeID: 9, Enabled: true, Online: true, LastHealthCheck: &at,
				Dir: "/transcode/download-artifacts", DirSource: downloads.StorageSourceDefault,
				Usage:      &downloadstorage.Usage{MeasuredAt: at, Files: 402, Bytes: 471_000_000_000, PartialFiles: 1, PartialBytes: 2_000_000_000, FSUsedBytes: 842_000_000_000, FSTotalBytes: 1_000_000_000_000, FSType: "ext4", SharesScratch: true},
				InUseFiles: 360, InUseBytes: 389_000_000_000, CachedFiles: 36, CachedBytes: 44_000_000_000,
				WaitingDownloads: 88, StaleWaitingBytes: 11_800_000_000, UntrackedFiles: 6, UntrackedBytes: 38_000_000_000, ReconciledAt: &at,
				Budget: 750_000_000_000, BudgetSource: downloads.StorageSourceSetting, CleanupBacklog: 2,
			},
		},
	}, nil
}

func (f *fakeAdminDownloadStorage) StorageFilesPage(_ context.Context, filter downloads.StorageFileFilter, _ *downloads.StorageFilePosition, _ int) ([]downloads.StorageFile, error) {
	f.lastFilter = filter
	expires := storageFixtureAt.Add(22 * time.Hour)
	seen := storageFixtureAt.Add(-19 * 24 * time.Hour)
	year := 2024
	return []downloads.StorageFile{
		{ArtifactID: "art-dune", State: downloads.StorageFileCached, Format: "transcode", Location: "node:9", LocationName: "node-gpu-1",
			MediaFileID: 42, LibraryID: 3, ContentID: "movie:dune-2", Title: "Dune: Part Two", MediaType: "movie", Year: &year,
			Container: "mp4", VideoCodec: "hevc", AudioCodec: "aac", Resolution: "1080p", BitrateKbps: 10_000, Bytes: 14_200_000_000,
			CreatedAt: storageFixtureAt.Add(-72 * time.Hour), LastUsedAt: storageFixtureAt.Add(-50 * time.Hour), ExpiresAt: &expires, Finished: 3},
		{ArtifactID: "art-oppenheimer", State: downloads.StorageFileInUse, Format: "transcode", Location: "node:9", LocationName: "node-gpu-1",
			MediaFileID: 51, ContentID: "movie:oppenheimer", Title: "Oppenheimer", MediaType: "movie",
			Container: "mp4", VideoCodec: "h264", AudioCodec: "aac", Resolution: "1080p", BitrateKbps: 8_000, Bytes: 11_800_000_000,
			CreatedAt: storageFixtureAt.Add(-20 * 24 * time.Hour), LastUsedAt: storageFixtureAt.Add(-19 * 24 * time.Hour), Waiting: 1, StaleWaiting: 1,
			OldestWaiting: &downloads.WaitingDevice{DeviceName: "Pixel 8", Username: "maya", LastSeenAt: &seen}},
	}, nil
}

func (*fakeAdminDownloadStorage) StorageEventsPage(context.Context, downloads.StorageEventFilter, *downloads.StorageEventPosition, int) ([]downloads.StorageEventBatch, error) {
	actor, account := 1, 7
	return []downloads.StorageEventBatch{
		{BatchID: "b-expired", Reason: downloads.StorageReasonCacheExpired, Location: "node:9", LocationName: "node-gpu-1", OccurredAt: storageFixtureAt, Count: 1, Bytes: 14_200_000_000, Titles: []string{"Dune: Part Two"}},
		{BatchID: "b-revoked", Reason: downloads.StorageReasonRevoked, Location: downloads.LocationDevice, LocationName: "Pixel 8", OccurredAt: storageFixtureAt.Add(-time.Hour), Count: 12, Bytes: 34_100_000_000,
			Titles: []string{"The Last of Us · S2 E1", "The Last of Us · S2 E2", "Oppenheimer"}, Detail: "Lost phone", ActorUserID: &actor, ActorName: "alex", AccountUserID: &account, AccountName: "maya"},
	}, nil
}

func (f *fakeAdminDownloadStorage) DeleteStorageFiles(_ context.Context, ids []string, includeInUse bool, actor int) ([]downloads.StorageDeleteResult, error) {
	f.lastDelete, f.includeInUse, f.actor = ids, includeInUse, actor
	out := make([]downloads.StorageDeleteResult, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		switch id {
		case "art-dune":
			out = append(out, downloads.StorageDeleteResult{ArtifactID: id, Outcome: downloads.StorageDeleteDeleted, Bytes: 14_200_000_000})
		case "art-oppenheimer":
			outcome := downloads.StorageDeleteInUse
			if includeInUse {
				outcome = downloads.StorageDeleteRequeued
			}
			out = append(out, downloads.StorageDeleteResult{ArtifactID: id, Outcome: outcome, Bytes: 11_800_000_000})
		default:
			out = append(out, downloads.StorageDeleteResult{ArtifactID: id, Outcome: downloads.StorageDeleteNotFound})
		}
	}
	return out, nil
}

func (f *fakeAdminDownloadStorage) CleanupLocation(_ context.Context, location string) (int64, error) {
	if location == "node:404" {
		return 0, downloads.ErrStorageLocationNotFound
	}
	f.cleaned = location
	return 41_700_000_000, nil
}

func (*fakeAdminDownloadStorage) DeleteUntrackedFiles(_ context.Context, location string, _ int) (downloads.UntrackedDeleteResult, error) {
	switch location {
	case "node:404":
		return downloads.UntrackedDeleteResult{}, downloads.ErrStorageLocationNotFound
	case "node:503":
		return downloads.UntrackedDeleteResult{}, fmt.Errorf("%w: node unreachable", downloads.ErrStorageListingUnavailable)
	case "node:500":
		return downloads.UntrackedDeleteResult{}, errors.New("database unavailable")
	}
	return downloads.UntrackedDeleteResult{Files: 6, Bytes: 38_000_000_000}, nil
}

type fakeAdminDownloadDevices struct{ lastRevoke downloads.RevokeRequest }

func (*fakeAdminDownloadDevices) AdminListDevicesPage(context.Context, downloads.AdminDeviceFilter, *downloads.AdminDevicePosition, int) ([]downloads.AdminDeviceRow, error) {
	seen := storageFixtureAt.Add(-19 * 24 * time.Hour)
	return []downloads.AdminDeviceRow{{
		UserID: 7, Username: "maya", ProfileID: "p-maya", ProfileName: "Maya", DeviceID: "dev-pixel", DeviceName: "Pixel 8", Platform: "android",
		LastSeenAt: &seen, Copies: 12, Finished: 7, Waiting: 5, BytesOnDevice: 34_100_000_000, Monitors: 1, Stale: true,
	}}, nil
}

func (*fakeAdminDownloadDevices) AdminListEntriesPage(context.Context, downloads.AdminEntryFilter, *downloads.RegistryPosition, int) ([]downloads.AdminEntryRow, error) {
	season, episode := 2, 1
	return []downloads.AdminEntryRow{{
		AdminDownloadRow: downloads.AdminDownloadRow{
			Download: downloads.Download{ID: "dl-1", UserID: 7, ProfileID: "p-maya", DeviceID: "dev-pixel", MediaFileID: 61, ContentID: "series:tlou",
				EpisodeID: "episode:tlou-s02e01", Status: downloads.StatusCompleted, Format: downloads.FormatTranscode, Quality: "10mbps", EffectiveQuality: "10mbps",
				TargetBitrateKbps: 10_000, Revision: 1, ArtifactID: "art-tlou", FileSize: 2_100_000_000, CreatedAt: storageFixtureAt, UpdatedAt: storageFixtureAt},
			Title: "The Last of Us", MediaType: "series", SeasonNumber: &season, EpisodeNumber: &episode, EpisodeTitle: "Future Days",
		},
		Username: "maya", DeviceName: "Pixel 8", Location: "node:9", LocationName: "node-gpu-1",
	}}, nil
}

func (f *fakeAdminDownloadDevices) RevokeDeviceDownloads(_ context.Context, req downloads.RevokeRequest) (*downloads.RevokeResult, error) {
	f.lastRevoke = req
	if len(req.IDs) == 0 && req.DeviceID == "" {
		return nil, downloads.ErrInvalidRevoke
	}
	return &downloads.RevokeResult{Revoked: []*downloads.Download{{ID: "dl-1"}, {ID: "dl-2"}}, Bytes: 4_200_000_000, PausedMonitors: 1}, nil
}

type fakeDownloadPrepareAgain struct{}

func (fakeDownloadPrepareAgain) PrepareAgain(_ context.Context, _ int, _, _, id string, _ catalogpkg.AccessFilter) (*downloads.Download, error) {
	if id == "entry-gone" {
		return nil, downloads.ErrNotFound
	}
	return &downloads.Download{ID: id, ContentID: "movie", MediaFileID: 42, Revision: 1, CreatedAt: storageFixtureAt, Status: downloads.StatusPreparing,
		Quality: "10mbps", EffectiveQuality: "10mbps", Format: downloads.FormatTranscode, DeviceID: "device-one", ArtifactID: "art-1"}, nil
}

func adminDownloadStorageFixtureCases() []fixtureCase {
	admin := bearer(adminToken)
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "admin_download_storage_capabilities", operationID: "getAdminDownloadStorageCapabilities", method: http.MethodGet, path: Prefix + "/admin/downloads/storage/capabilities", headers: admin, status: 200, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/AdminDownloadStorageCapabilitiesOutputBody", scenario: "Administrator discovery names the realtime channel and whether device copies can be listed and revoked."},
		{name: "admin_download_storage", operationID: "getAdminDownloadStorage", method: http.MethodGet, path: Prefix + "/admin/downloads/storage", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadStorage", scenario: "The server and one transcode node: measured disk use, Silo's in-use and cached totals, the node's untracked files, and that its files share the transcode scratch disk."},
		{name: "admin_download_storage_files", operationID: "listAdminDownloadStorageFiles", method: http.MethodGet, path: Prefix + "/admin/downloads/storage/files?location=node:9&sort=size", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionAdminDownloadStorageFile", scenario: "A cached file with the time its cache period ends, and an in-use file kept only for a device not seen in 19 days."},
		{name: "admin_download_storage_delete", operationID: "deleteAdminDownloadStorageFiles", method: http.MethodPost, path: Prefix + "/admin/downloads/storage/files/delete", headers: admin, body: `{"ids":["art-dune","art-oppenheimer","art-gone","art-dune"]}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadStorageDeleteOutputBody", scenario: "One outcome per distinct file: a cached file is deleted, an in-use file is refused without include_in_use, and an unknown one is not found."},
		{name: "admin_download_storage_delete_invalid_id", operationID: "deleteAdminDownloadStorageFiles", method: http.MethodPost, path: Prefix + "/admin/downloads/storage/files/delete", headers: admin, body: `{"ids":["../etc"]}`, status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem, scenario: "A file id outside the artifact id alphabet is rejected before any file is touched."},
		{name: "admin_download_storage_cleanup", operationID: "cleanUpAdminDownloadStorageLocation", method: http.MethodPost, path: Prefix + "/admin/downloads/storage/locations/node:9/cleanup", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadStorageCleanupOutputBody", scenario: "Clean-up at one node frees what is past its cache period or over the budget or disk ceiling."},
		{name: "admin_download_storage_untracked_delete", operationID: "deleteAdminDownloadStorageUntrackedFiles", method: http.MethodPost, path: Prefix + "/admin/downloads/storage/locations/node:9/untracked/delete", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadStorageUntrackedOutputBody", scenario: "Deleting untracked files reports how many files and bytes were removed."},
		{name: "admin_download_storage_events", operationID: "listAdminDownloadStorageEvents", method: http.MethodGet, path: Prefix + "/admin/downloads/storage/events?days=30", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionAdminDownloadStorageEvent", scenario: "History lists an automatic cache expiry and an administrator's revocation of a device's downloads, one batch per row."},
		{name: "admin_download_devices", operationID: "listAdminDownloadDevices", method: http.MethodGet, path: Prefix + "/admin/downloads/devices?stale=true", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionAdminDownloadDevice", scenario: "A stale device with finished and waiting downloads and a series monitor."},
		{name: "admin_download_entries", operationID: "listAdminDownloadEntries", method: http.MethodGet, path: Prefix + "/admin/downloads/entries?user_id=7&device_id=dev-pixel", headers: admin, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/CollectionAdminDownloadEntry", scenario: "One device's downloads with the account, device name, and where each prepared file is."},
		{name: "admin_download_revoke_device", operationID: "revokeAdminDownloads", method: http.MethodPost, path: Prefix + "/admin/downloads/revoke", headers: admin, body: `{"user_id":"7","profile_id":"p-maya","device_id":"dev-pixel","pause_monitors":true,"reason":"Lost phone"}`, status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/AdminDownloadRevokeOutputBody", scenario: "Revoking every download on one device, pausing its series monitors."},
		{name: "admin_download_revoke_mixed", operationID: "revokeAdminDownloads", method: http.MethodPost, path: Prefix + "/admin/downloads/revoke", headers: admin, body: `{"ids":["dl-1"],"device_id":"dev-pixel"}`, status: http.StatusUnprocessableEntity, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem, scenario: "A revoke names download ids or one device, not both."},
		{name: "prepare_download_again", operationID: "prepareDownloadAgain", method: http.MethodPost, path: Prefix + "/downloads/entry-1/prepare", headers: with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one"), status: 200, assertHeaders: []string{"Content-Type"}, schema: "#/components/schemas/DownloadEntry", scenario: "A finished download whose server file expired returns to preparing with the same revision."},
	}
}

func TestAdminDownloadStorageDeleteForwardsActorAndInUse(t *testing.T) {
	storage := new(fakeAdminDownloadStorage)
	deps := fixtureDeps()
	deps.AdminDownloadStorage = storage
	h := newTestHandler(t, deps)
	req := httptest.NewRequest(http.MethodPost, Prefix+"/admin/downloads/storage/files/delete", strings.NewReader(`{"ids":["art-oppenheimer"],"include_in_use":true}`))
	for k, v := range bearer(adminToken) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body AdminDownloadStorageDeleteOutput
	if err := json.Unmarshal(rec.Body.Bytes(), &body.Body); err != nil {
		t.Fatal(err)
	}
	if !storage.includeInUse || storage.actor == 0 || len(body.Body.Results) != 1 || body.Body.Results[0].Outcome != downloads.StorageDeleteRequeued {
		t.Fatalf("delete = %+v, include_in_use=%v actor=%d", body.Body.Results, storage.includeInUse, storage.actor)
	}
}

func TestAdminDownloadStorageRefusesUnknownLocationsAndOversizedIDs(t *testing.T) {
	deps := fixtureDeps()
	deps.AdminDownloadStorage = new(fakeAdminDownloadStorage)
	h := newTestHandler(t, deps)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/admin/downloads/storage/locations/node:404/untracked/delete", http.StatusNotFound},
		// Only a listing failure asks the administrator to retry.
		{http.MethodPost, "/admin/downloads/storage/locations/node:503/untracked/delete", http.StatusServiceUnavailable},
		{http.MethodPost, "/admin/downloads/storage/locations/node:500/untracked/delete", http.StatusInternalServerError},
		{http.MethodPost, "/admin/downloads/storage/locations/node:404/cleanup", http.StatusNotFound},
		// Ten digits pass the pattern but not int4.
		{http.MethodGet, "/admin/downloads/storage/files?location=node:9999999999", http.StatusUnprocessableEntity},
		{http.MethodGet, "/admin/downloads/storage/files?library_id=9999999999", http.StatusUnprocessableEntity},
		{http.MethodGet, "/admin/downloads/storage/events?location=node:9999999999", http.StatusUnprocessableEntity},
		{http.MethodGet, "/admin/downloads/entries?user_id=9999999999", http.StatusUnprocessableEntity},
	} {
		req := httptest.NewRequest(tc.method, Prefix+tc.path, nil)
		for k, v := range bearer(adminToken) {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("%s %s = %d, want %d: %s", tc.method, tc.path, rec.Code, tc.status, rec.Body.String())
		}
	}
}

func TestPreparedFileExpiredMapsToItsOwnProblem(t *testing.T) {
	p := downloadProblem(errors.Join(downloads.ErrDownloadNotActive, downloads.ErrPreparedFileExpired))
	if p.Status != http.StatusConflict || !strings.HasSuffix(p.Type, "/prepared_file_expired") {
		t.Fatalf("problem = %+v", p)
	}
}
