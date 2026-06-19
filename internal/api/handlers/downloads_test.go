package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

// fakeDownloadService is a programmable DownloadService for handler tests. It
// records the identity (user/profile/device) it was called with so tests can
// assert the handler threads header-only device authority correctly.
type fakeDownloadService struct {
	capability  downloads.Capability
	created     *downloads.Download
	createErr   error
	series      []*downloads.Download
	seriesID    string
	seriesErr   error
	list        []*downloads.Download
	listErr     error
	deleteErr   error
	patchErr    error
	serveErr    error
	directErr   error
	manifest    *downloads.OfflineManifest
	manifestErr error
	artworkErr  error
	subtitleErr error

	gotCreateReq    downloads.CreateRequest
	gotSeriesReq    downloads.CreateRequest
	gotList         identityCall
	gotServe        identityCall
	gotDelete       identityCall
	gotPatch        identityCall
	gotPatchStatus  string
	gotManifest     identityCall
	gotArtwork      identityCall
	gotArtworkKind  string
	gotSubtitle     identityCall
	gotSubtitleRef  string
	gotDirectFormat string
	gotDirectFileID int
}

type identityCall struct {
	userID     int
	profileID  string
	deviceID   string
	downloadID string
}

func (f *fakeDownloadService) Capability(context.Context, int) (downloads.Capability, error) {
	return f.capability, nil
}

func (f *fakeDownloadService) Create(_ context.Context, _ int, req downloads.CreateRequest, _ catalog.AccessFilter) (*downloads.Download, error) {
	f.gotCreateReq = req
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.created, nil
}

func (f *fakeDownloadService) CreateSeries(_ context.Context, _ int, req downloads.CreateRequest, _ catalog.AccessFilter) ([]*downloads.Download, string, error) {
	f.gotSeriesReq = req
	if f.seriesErr != nil {
		return nil, "", f.seriesErr
	}
	return f.series, f.seriesID, nil
}

func (f *fakeDownloadService) ServeDirect(_ context.Context, w http.ResponseWriter, _ *http.Request, _, fileID int, format string, _ catalog.AccessFilter) error {
	f.gotDirectFileID = fileID
	f.gotDirectFormat = format
	if f.directErr != nil {
		return f.directErr
	}
	_, _ = w.Write([]byte("served"))
	return nil
}

func (f *fakeDownloadService) ServeFile(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID string, _ catalog.AccessFilter) error {
	f.gotServe = identityCall{userID, profileID, deviceID, downloadID}
	if f.serveErr != nil {
		return f.serveErr
	}
	_, _ = w.Write([]byte("served"))
	return nil
}

func (f *fakeDownloadService) List(_ context.Context, userID int, profileID, deviceID string) ([]*downloads.Download, error) {
	f.gotList = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	return f.list, f.listErr
}

func (f *fakeDownloadService) Delete(_ context.Context, userID int, profileID, deviceID, downloadID string) error {
	f.gotDelete = identityCall{userID, profileID, deviceID, downloadID}
	return f.deleteErr
}

func (f *fakeDownloadService) PatchStatus(_ context.Context, userID int, profileID, deviceID, downloadID, status string) error {
	f.gotPatch = identityCall{userID, profileID, deviceID, downloadID}
	f.gotPatchStatus = status
	return f.patchErr
}

func (f *fakeDownloadService) BuildManifest(_ context.Context, userID int, profileID, deviceID, downloadID string, _ catalog.AccessFilter) (*downloads.OfflineManifest, error) {
	f.gotManifest = identityCall{userID, profileID, deviceID, downloadID}
	if f.manifestErr != nil {
		return nil, f.manifestErr
	}
	return f.manifest, nil
}

func (f *fakeDownloadService) ServeArtwork(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID, kind string, _ catalog.AccessFilter) error {
	f.gotArtwork = identityCall{userID, profileID, deviceID, downloadID}
	f.gotArtworkKind = kind
	if f.artworkErr != nil {
		return f.artworkErr
	}
	_, _ = w.Write([]byte("img"))
	return nil
}

func (f *fakeDownloadService) ServeSubtitle(_ context.Context, w http.ResponseWriter, _ *http.Request, userID int, profileID, deviceID, downloadID, ref string, _ catalog.AccessFilter) error {
	f.gotSubtitle = identityCall{userID, profileID, deviceID, downloadID}
	f.gotSubtitleRef = ref
	if f.subtitleErr != nil {
		return f.subtitleErr
	}
	_, _ = w.Write([]byte("WEBVTT"))
	return nil
}

// downloadTestRequest builds a request with auth claims and optional profile +
// device identity (as the viewer-access middleware / client headers would set).
func downloadTestRequest(method, target string, body []byte, userID int, profileID, deviceID string) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if userID != 0 {
		ctx := apimw.SetClaims(r.Context(), &auth.Claims{
			UserID:    userID,
			Role:      "user",
			TokenType: auth.TokenTypeAccess,
		})
		if profileID != "" {
			ctx = apimw.SetProfileID(ctx, profileID)
		}
		r = r.WithContext(ctx)
	}
	if deviceID != "" {
		r.Header.Set("X-Silo-Device-Id", deviceID)
	}
	return r
}

func withChiID(r *http.Request, id string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestHandleCapability(t *testing.T) {
	svc := &fakeDownloadService{capability: downloads.Capability{
		Enabled:              true,
		DownloadAllowed:      true,
		Formats:              []string{downloads.FormatOriginal},
		TranscodeEnabled:     false,
		TranscodeUserAllowed: true,
	}}
	h := NewDownloadHandler(svc)

	rec := httptest.NewRecorder()
	h.HandleCapability(rec, downloadTestRequest(http.MethodGet, "/downloads/capability", nil, 7, "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp downloadCapabilityResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Enabled || !resp.DownloadAllowed || resp.TranscodeEnabled || !resp.TranscodeUserAllowed {
		t.Fatalf("unexpected capability flags: %+v", resp)
	}
	if len(resp.Formats) != 1 || resp.Formats[0] != downloads.FormatOriginal {
		t.Fatalf("formats = %v, want [original]", resp.Formats)
	}
}

func TestHandleCapabilityUnauthorized(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	rec := httptest.NewRecorder()
	h.HandleCapability(rec, httptest.NewRequest(http.MethodGet, "/downloads/capability", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleCapabilityNilService(t *testing.T) {
	h := NewDownloadHandler(nil)
	rec := httptest.NewRecorder()
	h.HandleCapability(rec, downloadTestRequest(http.MethodGet, "/downloads/capability", nil, 7, "", ""))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleCreateDownloadThreadsFormat(t *testing.T) {
	svc := &fakeDownloadService{created: &downloads.Download{
		ID: "dl1", ContentID: "c1", Status: downloads.StatusQueued, Format: downloads.FormatTranscode,
	}}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "c1", Format: downloads.FormatTranscode})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotCreateReq.Format != downloads.FormatTranscode {
		t.Fatalf("service received format %q, want transcode", svc.gotCreateReq.Format)
	}
	var resp downloadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Format != downloads.FormatTranscode {
		t.Fatalf("response format = %q, want transcode", resp.Format)
	}
}

func TestHandleCreateDownloadSeriesThreadsFormat(t *testing.T) {
	svc := &fakeDownloadService{
		series:   []*downloads.Download{{ID: "dl1", ContentID: "s1", Format: downloads.FormatOriginal}},
		seriesID: "batch1",
	}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "s1", Series: true, Format: downloads.FormatRemux})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSeriesReq.Format != downloads.FormatRemux {
		t.Fatalf("series service received format %q, want remux", svc.gotSeriesReq.Format)
	}
}

func TestHandleCreateDownloadMissingContentID(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	body, _ := json.Marshal(downloadRequest{Format: downloads.FormatOriginal})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateDownloadErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"feature disabled", downloads.ErrFeatureDisabled, http.StatusForbidden},
		{"not allowed", downloads.ErrDownloadNotAllowed, http.StatusForbidden},
		{"transcode disabled", downloads.ErrTranscodeDisabled, http.StatusForbidden},
		{"invalid format", downloads.ErrInvalidFormat, http.StatusBadRequest},
		{"format unavailable", downloads.ErrFormatUnavailable, http.StatusNotImplemented},
		{"profile required", downloads.ErrProfileRequired, http.StatusBadRequest},
		{"concurrent limit", downloads.ErrConcurrentLimitReached, http.StatusTooManyRequests},
		{"period limit", downloads.ErrPeriodLimitReached, http.StatusTooManyRequests},
		{"item not found", catalog.ErrItemNotFound, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDownloadHandler(&fakeDownloadService{createErr: tc.err})
			body, _ := json.Marshal(downloadRequest{ContentID: "c1"})
			rec := httptest.NewRecorder()
			h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

// TestManagedCreateUsesHeaderDeviceNotBody verifies invariant 2's "device_id
// authority is the header only": a device_id placed in the JSON body is ignored,
// and the service receives the X-Silo-Device-Id header value + the profile.
func TestManagedCreateUsesHeaderDeviceNotBody(t *testing.T) {
	svc := &fakeDownloadService{created: &downloads.Download{ID: "dl1", ContentID: "c1", DeviceID: "devA"}}
	h := NewDownloadHandler(svc)

	// Body smuggles a device_id; it is not a field of downloadRequest, so it is
	// structurally dropped and must never reach the service.
	body := []byte(`{"content_id":"c1","device_id":"EVIL","profile_id":"EVIL"}`)
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "pA", "devA"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotCreateReq.DeviceID != "devA" {
		t.Fatalf("service device id = %q, want header value devA", svc.gotCreateReq.DeviceID)
	}
	if svc.gotCreateReq.ProfileID != "pA" {
		t.Fatalf("service profile id = %q, want context value pA", svc.gotCreateReq.ProfileID)
	}
}

func TestManagedFileThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	req := withChiID(downloadTestRequest(http.MethodGet, "/downloads/dl1/file", nil, 7, "pA", "devA"), "dl1")
	rec := httptest.NewRecorder()
	h.HandleDownloadFile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotServe != (identityCall{7, "pA", "devA", "dl1"}) {
		t.Fatalf("serve identity = %+v, want {7 pA devA dl1}", svc.gotServe)
	}
}

func TestManagedListThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	rec := httptest.NewRecorder()
	h.HandleListDownloads(rec, downloadTestRequest(http.MethodGet, "/downloads", nil, 7, "pA", "devA"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if svc.gotList.deviceID != "devA" || svc.gotList.profileID != "pA" {
		t.Fatalf("list identity = %+v, want profile pA device devA", svc.gotList)
	}
}

func TestManagedDeleteThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	req := withChiID(downloadTestRequest(http.MethodDelete, "/downloads/dl1", nil, 7, "pA", "devA"), "dl1")
	rec := httptest.NewRecorder()
	h.HandleDeleteDownload(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotDelete != (identityCall{7, "pA", "devA", "dl1"}) {
		t.Fatalf("delete identity = %+v, want {7 pA devA dl1}", svc.gotDelete)
	}
}

func TestHandleDeleteDownloadNotFound(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{deleteErr: downloads.ErrNotFound})
	rec := httptest.NewRecorder()
	req := withChiID(downloadTestRequest(http.MethodDelete, "/downloads/dl1", nil, 7, "", ""), "dl1")
	h.HandleDeleteDownload(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestManagedPatchRequiresDeviceHeader(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	// No device header → 400 device_id_required, even with a profile present.
	req := withChiID(downloadTestRequest(http.MethodPatch, "/downloads/dl1", []byte(`{"status":"completed"}`), 7, "pA", ""), "dl1")
	rec := httptest.NewRecorder()
	h.HandlePatchDownload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 device_id_required (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestManagedPatchThreadsIdentityAndStatus(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	req := withChiID(downloadTestRequest(http.MethodPatch, "/downloads/dl1", []byte(`{"status":"completed"}`), 7, "pA", "devA"), "dl1")
	rec := httptest.NewRecorder()
	h.HandlePatchDownload(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotPatch != (identityCall{7, "pA", "devA", "dl1"}) {
		t.Fatalf("patch identity = %+v, want {7 pA devA dl1}", svc.gotPatch)
	}
	if svc.gotPatchStatus != downloads.StatusCompleted {
		t.Fatalf("patch status = %q, want completed", svc.gotPatchStatus)
	}
}

func TestHandleDirectDownloadThreadsFormat(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	rec := httptest.NewRecorder()
	h.HandleDirectDownload(rec, downloadTestRequest(http.MethodGet, "/direct-download?file_id=42&format=remux", nil, 7, "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotDirectFileID != 42 {
		t.Fatalf("file id = %d, want 42", svc.gotDirectFileID)
	}
	if svc.gotDirectFormat != downloads.FormatRemux {
		t.Fatalf("direct format = %q, want remux", svc.gotDirectFormat)
	}
}

func TestHandleDirectDownloadMissingFileID(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	rec := httptest.NewRecorder()
	h.HandleDirectDownload(rec, downloadTestRequest(http.MethodGet, "/direct-download", nil, 7, "", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func withChiParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestManagedManifestRequiresDeviceHeader(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	// Profile present but no device header → 400 device_id_required.
	req := withChiID(downloadTestRequest(http.MethodGet, "/downloads/dl1/manifest", nil, 7, "pA", ""), "dl1")
	rec := httptest.NewRecorder()
	h.HandleManifest(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestManagedManifestThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{manifest: &downloads.OfflineManifest{DownloadID: "dl1", Title: "Movie"}}
	h := NewDownloadHandler(svc)
	req := withChiID(downloadTestRequest(http.MethodGet, "/downloads/dl1/manifest", nil, 7, "pA", "devA"), "dl1")
	rec := httptest.NewRecorder()
	h.HandleManifest(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotManifest != (identityCall{7, "pA", "devA", "dl1"}) {
		t.Fatalf("manifest identity = %+v, want {7 pA devA dl1}", svc.gotManifest)
	}
}

// TestManagedAssetsDenyRestrictedProfile is the Phase 2 acceptance test: a
// profile denied content access (the service returns ErrItemNotFound) gets a
// 404 from manifest, artwork, and subtitle — a download id never reveals
// out-of-scope content.
func TestManagedAssetsDenyRestrictedProfile(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		h := NewDownloadHandler(&fakeDownloadService{manifestErr: catalog.ErrItemNotFound})
		req := withChiID(downloadTestRequest(http.MethodGet, "/downloads/dl1/manifest", nil, 7, "child", "devC"), "dl1")
		rec := httptest.NewRecorder()
		h.HandleManifest(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("manifest status = %d, want 404", rec.Code)
		}
	})
	t.Run("artwork", func(t *testing.T) {
		h := NewDownloadHandler(&fakeDownloadService{artworkErr: catalog.ErrItemNotFound})
		req := withChiParams(downloadTestRequest(http.MethodGet, "/downloads/dl1/artwork/poster", nil, 7, "child", "devC"),
			map[string]string{"id": "dl1", "kind": "poster"})
		rec := httptest.NewRecorder()
		h.HandleArtwork(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("artwork status = %d, want 404", rec.Code)
		}
	})
	t.Run("subtitle", func(t *testing.T) {
		h := NewDownloadHandler(&fakeDownloadService{subtitleErr: catalog.ErrItemNotFound})
		req := withChiParams(downloadTestRequest(http.MethodGet, "/downloads/dl1/subtitles/external:0", nil, 7, "child", "devC"),
			map[string]string{"id": "dl1", "ref": "external:0"})
		rec := httptest.NewRecorder()
		h.HandleSubtitle(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("subtitle status = %d, want 404", rec.Code)
		}
	})
}

func TestManagedArtworkThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	req := withChiParams(downloadTestRequest(http.MethodGet, "/downloads/dl1/artwork/backdrop", nil, 7, "pA", "devA"),
		map[string]string{"id": "dl1", "kind": "backdrop"})
	rec := httptest.NewRecorder()
	h.HandleArtwork(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotArtwork != (identityCall{7, "pA", "devA", "dl1"}) || svc.gotArtworkKind != "backdrop" {
		t.Fatalf("artwork identity = %+v kind = %q", svc.gotArtwork, svc.gotArtworkKind)
	}
}

func TestManagedSubtitleInvalidRef(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{subtitleErr: downloads.ErrInvalidSubtitleRef})
	req := withChiParams(downloadTestRequest(http.MethodGet, "/downloads/dl1/subtitles/bogus", nil, 7, "pA", "devA"),
		map[string]string{"id": "dl1", "ref": "bogus"})
	rec := httptest.NewRecorder()
	h.HandleSubtitle(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}
