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
	capability     downloads.Capability
	created        *downloads.Download
	createErr      error
	series         []*downloads.Download
	seriesID       string
	seriesErr      error
	list           []*downloads.Download
	listErr        error
	deleteErr      error
	patchErr       error
	serveErr       error
	directErr      error
	manifest       *downloads.OfflineManifest
	batchManifests []*downloads.OfflineManifest
	batchSkipped   []downloads.SkippedManifest
	manifestErr    error
	artworkErr     error
	subtitleErr    error

	gotCreateReq     downloads.CreateRequest
	gotSeriesReq     downloads.CreateRequest
	gotList          identityCall
	gotServe         identityCall
	gotDelete        identityCall
	gotPatch         identityCall
	gotPatchStatus   string
	gotManifest      identityCall
	gotBatchManifest identityCall
	gotArtwork       identityCall
	gotArtworkKind   string
	gotSubtitle      identityCall
	gotSubtitleRef   string
	gotDirectFormat  string
	gotDirectFileID  int

	// Season download + series-monitoring (subscription) fakes.
	season       []*downloads.Download
	seasonID     string
	seasonErr    error
	gotSeasonReq downloads.CreateRequest
	gotSeasonNum int
	seasonCalled bool

	subResult      *downloads.SubscriptionResult
	subList        []*downloads.Subscription
	sub            *downloads.Subscription
	subErr         error
	subDeleteErr   error
	gotSubReq      downloads.SubscriptionRequest
	gotSubPatch    downloads.SubscriptionPatch
	gotSubIdent    identityCall
	syncRegistered int
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

func (f *fakeDownloadService) CreateSeries(_ context.Context, _ int, req downloads.CreateRequest, _ catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error) {
	f.gotSeriesReq = req
	if f.seriesErr != nil {
		return nil, "", nil, f.seriesErr
	}
	return f.series, f.seriesID, nil, nil
}

func (f *fakeDownloadService) CreateSeason(_ context.Context, _ int, req downloads.CreateRequest, seasonNumber int, _ catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error) {
	f.gotSeasonReq = req
	f.gotSeasonNum = seasonNumber
	f.seasonCalled = true
	if f.seasonErr != nil {
		return nil, "", nil, f.seasonErr
	}
	return f.season, f.seasonID, nil, nil
}

func (f *fakeDownloadService) CreateSubscription(_ context.Context, _ int, req downloads.SubscriptionRequest, _ catalog.AccessFilter) (*downloads.SubscriptionResult, error) {
	f.gotSubReq = req
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.subResult, nil
}

func (f *fakeDownloadService) ListSubscriptions(_ context.Context, userID int, profileID, deviceID string) ([]*downloads.Subscription, error) {
	f.gotSubIdent = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	return f.subList, f.subErr
}

func (f *fakeDownloadService) GetSubscription(_ context.Context, userID int, profileID, deviceID, id string) (*downloads.Subscription, error) {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.sub, nil
}

func (f *fakeDownloadService) UpdateSubscription(_ context.Context, userID int, profileID, deviceID, id string, patch downloads.SubscriptionPatch, _ catalog.AccessFilter) (*downloads.SubscriptionResult, error) {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	f.gotSubPatch = patch
	if f.subErr != nil {
		return nil, f.subErr
	}
	return f.subResult, nil
}

func (f *fakeDownloadService) DeleteSubscription(_ context.Context, userID int, profileID, deviceID, id string) error {
	f.gotSubIdent = identityCall{userID, profileID, deviceID, id}
	return f.subDeleteErr
}

func (f *fakeDownloadService) SyncSubscriptions(_ context.Context, userID int, profileID, deviceID string, _ catalog.AccessFilter) (int, error) {
	f.gotSubIdent = identityCall{userID: userID, profileID: profileID, deviceID: deviceID}
	if f.subErr != nil {
		return 0, f.subErr
	}
	return f.syncRegistered, nil
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

func (f *fakeDownloadService) BuildBatchManifests(_ context.Context, userID int, profileID, deviceID, batchID string, _ catalog.AccessFilter) ([]*downloads.OfflineManifest, []downloads.SkippedManifest, error) {
	f.gotBatchManifest = identityCall{userID, profileID, deviceID, batchID}
	if f.manifestErr != nil {
		return nil, nil, f.manifestErr
	}
	return f.batchManifests, f.batchSkipped, nil
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
		QualityPresets:       []string{downloads.QualityOriginal},
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
	if len(resp.QualityPresets) != 1 || resp.QualityPresets[0] != downloads.QualityOriginal {
		t.Fatalf("quality presets = %v, want [original]", resp.QualityPresets)
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

func TestHandleCreateDownloadThreadsQuality(t *testing.T) {
	svc := &fakeDownloadService{created: &downloads.Download{
		ID: "dl1", ContentID: "c1", Status: downloads.StatusQueued, Format: downloads.FormatTranscode,
		Quality: downloads.Quality5Mbps, EffectiveQuality: downloads.Quality5Mbps, TargetBitrateKbps: 5000, Revision: 2,
	}}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "c1", Quality: downloads.Quality5Mbps})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotCreateReq.Quality != downloads.Quality5Mbps {
		t.Fatalf("service received quality %q, want 5mbps", svc.gotCreateReq.Quality)
	}
	var resp downloadResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Quality != downloads.Quality5Mbps || resp.DeliveryFormat != downloads.FormatTranscode ||
		resp.TargetBitrateKbps != 5000 || resp.Revision != 2 {
		t.Fatalf("response = %+v, want quality/delivery/bitrate/revision", resp)
	}
}

func TestHandleCreateDownloadSeriesThreadsQuality(t *testing.T) {
	svc := &fakeDownloadService{
		series:   []*downloads.Download{{ID: "dl1", ContentID: "s1", Format: downloads.FormatOriginal, Quality: downloads.QualityOriginal, EffectiveQuality: downloads.QualityOriginal, Revision: 1}},
		seriesID: "batch1",
	}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "s1", Series: true, Quality: downloads.QualityOriginal})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "", ""))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSeriesReq.Quality != downloads.QualityOriginal {
		t.Fatalf("series service received quality %q, want original", svc.gotSeriesReq.Quality)
	}
}

func TestHandleCreateDownloadMissingContentID(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	body, _ := json.Marshal(downloadRequest{Quality: downloads.QualityOriginal})
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
		{"invalid quality", downloads.ErrInvalidQuality, http.StatusBadRequest},
		{"quality unavailable", downloads.ErrQualityUnavailable, http.StatusNotImplemented},
		{"bulk quality unavailable", downloads.ErrBulkQualityUnavailable, http.StatusNotImplemented},
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

func TestHandleDirectDownloadThreadsOriginalFormat(t *testing.T) {
	svc := &fakeDownloadService{}
	h := NewDownloadHandler(svc)
	rec := httptest.NewRecorder()
	h.HandleDirectDownload(rec, downloadTestRequest(http.MethodGet, "/direct-download?file_id=42&format=original", nil, 7, "", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotDirectFileID != 42 {
		t.Fatalf("file id = %d, want 42", svc.gotDirectFileID)
	}
	if svc.gotDirectFormat != downloads.FormatOriginal {
		t.Fatalf("direct format = %q, want original", svc.gotDirectFormat)
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

func TestManagedBatchManifestsThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{batchManifests: []*downloads.OfflineManifest{
		{DownloadID: "dl1", Title: "Episode 1"},
	}}
	h := NewDownloadHandler(svc)
	req := withChiParams(downloadTestRequest(http.MethodGet, "/downloads/batches/b1/manifests", nil, 7, "pA", "devA"),
		map[string]string{"batch_id": "b1"})
	rec := httptest.NewRecorder()
	h.HandleBatchManifests(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotBatchManifest != (identityCall{7, "pA", "devA", "b1"}) {
		t.Fatalf("batch manifest identity = %+v, want {7 pA devA b1}", svc.gotBatchManifest)
	}
	var resp batchManifestsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Manifests) != 1 || resp.Manifests[0].DownloadID != "dl1" {
		t.Fatalf("manifests = %+v, want one dl1 manifest", resp.Manifests)
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

func intPtr(i int) *int { return &i }

// TestHandleCreateDownloadSeasonRoutes verifies that series=true + a season
// number routes to CreateSeason (not CreateSeries) with the season threaded.
func TestHandleCreateDownloadSeasonRoutes(t *testing.T) {
	svc := &fakeDownloadService{
		season:   []*downloads.Download{{ID: "dl1", ContentID: "s1", EpisodeID: "e1", Format: downloads.FormatOriginal}},
		seasonID: "batch1",
	}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "s1", Series: true, Season: intPtr(2)})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "pA", "devA"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSeasonNum != 2 {
		t.Fatalf("season number = %d, want 2", svc.gotSeasonNum)
	}
	if svc.gotSeasonReq.ContentID != "s1" {
		t.Fatalf("season content id = %q, want s1", svc.gotSeasonReq.ContentID)
	}
}

// TestHandleCreateDownloadSpecialsSeason pins the Specials boundary: season 0
// must route to CreateSeason(0), not silently broaden to a full-series
// download, and negative seasons are rejected.
func TestHandleCreateDownloadSpecialsSeason(t *testing.T) {
	svc := &fakeDownloadService{
		season:   []*downloads.Download{{ID: "dl1", ContentID: "s1", EpisodeID: "e1", Format: downloads.FormatOriginal}},
		seasonID: "batch1",
	}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(downloadRequest{ContentID: "s1", Series: true, Season: intPtr(0)})
	rec := httptest.NewRecorder()
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "pA", "devA"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if !svc.seasonCalled || svc.gotSeasonNum != 0 {
		t.Fatalf("seasonCalled=%v num=%d, want CreateSeason(0)", svc.seasonCalled, svc.gotSeasonNum)
	}
	if svc.gotSeriesReq.ContentID != "" {
		t.Fatal("season 0 must not fall through to CreateSeries")
	}

	rec = httptest.NewRecorder()
	body, _ = json.Marshal(downloadRequest{ContentID: "s1", Series: true, Season: intPtr(-1)})
	h.HandleCreateDownload(rec, downloadTestRequest(http.MethodPost, "/downloads", body, 7, "pA", "devA"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative season status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateSubscriptionThreadsRequest(t *testing.T) {
	svc := &fakeDownloadService{subResult: &downloads.SubscriptionResult{
		Subscription: &downloads.Subscription{ID: "sub1", SeriesID: "s1", Mode: downloads.SubModeLatestSeason, Active: true},
		Registered:   3,
	}}
	h := NewDownloadHandler(svc)

	body, _ := json.Marshal(subscriptionRequest{SeriesID: "s1", Mode: downloads.SubModeLatestSeason, DeleteWatched: true, MaxStorageBytes: 1024})
	rec := httptest.NewRecorder()
	h.HandleCreateSubscription(rec, downloadTestRequest(http.MethodPost, "/downloads/subscriptions", body, 7, "pA", "devA"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSubReq.SeriesID != "s1" || svc.gotSubReq.Mode != downloads.SubModeLatestSeason {
		t.Fatalf("subscription request = %+v", svc.gotSubReq)
	}
	if svc.gotSubReq.DeviceID != "devA" || svc.gotSubReq.ProfileID != "pA" {
		t.Fatalf("subscription identity = profile %q device %q, want pA/devA", svc.gotSubReq.ProfileID, svc.gotSubReq.DeviceID)
	}
	if !svc.gotSubReq.DeleteWatched || svc.gotSubReq.MaxStorageBytes != 1024 {
		t.Fatalf("subscription options not threaded: %+v", svc.gotSubReq)
	}
	var resp subscriptionResultResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Registered != 3 || resp.Subscription.ID != "sub1" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestHandleCreateSubscriptionRequiresDevice enforces that monitoring is
// device-scoped: a missing X-Silo-Device-Id header is a 400 even with a profile.
func TestHandleCreateSubscriptionRequiresDevice(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	body, _ := json.Marshal(subscriptionRequest{SeriesID: "s1", Mode: downloads.SubModeAll})
	rec := httptest.NewRecorder()
	h.HandleCreateSubscription(rec, downloadTestRequest(http.MethodPost, "/downloads/subscriptions", body, 7, "pA", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}

func TestHandleListSubscriptionsThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{subList: []*downloads.Subscription{{ID: "sub1", SeriesID: "s1", Mode: downloads.SubModeAll, Active: true}}}
	h := NewDownloadHandler(svc)
	rec := httptest.NewRecorder()
	h.HandleListSubscriptions(rec, downloadTestRequest(http.MethodGet, "/downloads/subscriptions", nil, 7, "pA", "devA"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSubIdent.profileID != "pA" || svc.gotSubIdent.deviceID != "devA" {
		t.Fatalf("list identity = %+v, want profile pA device devA", svc.gotSubIdent)
	}
	var resp subscriptionsListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Subscriptions) != 1 || resp.Subscriptions[0].ID != "sub1" {
		t.Fatalf("unexpected subscriptions: %+v", resp.Subscriptions)
	}
}

func TestHandleSubscriptionErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"not found", downloads.ErrSubscriptionNotFound, http.StatusNotFound},
		{"unavailable", downloads.ErrSubscriptionsUnavailable, http.StatusServiceUnavailable},
		{"invalid mode", downloads.ErrInvalidSubscriptionMode, http.StatusBadRequest},
		{"seasons required", downloads.ErrSeasonsRequired, http.StatusBadRequest},
		{"not series", downloads.ErrNotSeries, http.StatusBadRequest},
		{"not allowed", downloads.ErrDownloadNotAllowed, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewDownloadHandler(&fakeDownloadService{subErr: tc.err})
			body, _ := json.Marshal(subscriptionRequest{SeriesID: "s1", Mode: downloads.SubModeAll})
			rec := httptest.NewRecorder()
			h.HandleCreateSubscription(rec, downloadTestRequest(http.MethodPost, "/downloads/subscriptions", body, 7, "pA", "devA"))
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantCode, rec.Body.String())
			}
		})
	}
}

func TestHandleSyncSubscriptionsThreadsIdentity(t *testing.T) {
	svc := &fakeDownloadService{syncRegistered: 5}
	h := NewDownloadHandler(svc)
	rec := httptest.NewRecorder()
	h.HandleSyncSubscriptions(rec, downloadTestRequest(http.MethodPost, "/downloads/subscriptions/sync", nil, 7, "pA", "devA"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if svc.gotSubIdent.profileID != "pA" || svc.gotSubIdent.deviceID != "devA" {
		t.Fatalf("sync identity = %+v, want profile pA device devA", svc.gotSubIdent)
	}
	var resp subscriptionSyncResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Registered != 5 {
		t.Fatalf("registered = %d, want 5", resp.Registered)
	}
}

func TestHandleSyncSubscriptionsRequiresDevice(t *testing.T) {
	h := NewDownloadHandler(&fakeDownloadService{})
	rec := httptest.NewRecorder()
	// Profile present, no device header → 400 device_id_required.
	h.HandleSyncSubscriptions(rec, downloadTestRequest(http.MethodPost, "/downloads/subscriptions/sync", nil, 7, "pA", ""))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body: %s)", rec.Code, rec.Body.String())
	}
}
