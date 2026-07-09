package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// DownloadService is the interface that the download handler depends on. A
// non-empty deviceID (from the X-Silo-Device-Id header) selects the managed
// device-library lifecycle; empty is the ephemeral/account-level path.
type DownloadService interface {
	Capability(ctx context.Context, userID int) (downloads.Capability, error)
	Create(ctx context.Context, userID int, req downloads.CreateRequest, filter catalog.AccessFilter) (*downloads.Download, error)
	CreateSeries(ctx context.Context, userID int, req downloads.CreateRequest, filter catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error)
	CreateSeason(ctx context.Context, userID int, req downloads.CreateRequest, seasonNumber int, filter catalog.AccessFilter) ([]*downloads.Download, string, []downloads.SkippedDownload, error)
	CreateSubscription(ctx context.Context, userID int, req downloads.SubscriptionRequest, filter catalog.AccessFilter) (*downloads.SubscriptionResult, error)
	ListSubscriptions(ctx context.Context, userID int, profileID, deviceID string) ([]*downloads.Subscription, error)
	GetSubscription(ctx context.Context, userID int, profileID, deviceID, id string) (*downloads.Subscription, error)
	UpdateSubscription(ctx context.Context, userID int, profileID, deviceID, id string, patch downloads.SubscriptionPatch, filter catalog.AccessFilter) (*downloads.SubscriptionResult, error)
	DeleteSubscription(ctx context.Context, userID int, profileID, deviceID, id string) error
	SyncSubscriptions(ctx context.Context, userID int, profileID, deviceID string, filter catalog.AccessFilter) (int, error)
	ServeDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fileID int, format string, filter catalog.AccessFilter) error
	ServeFile(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) error
	List(ctx context.Context, userID int, profileID, deviceID string) ([]*downloads.Download, error)
	Delete(ctx context.Context, userID int, profileID, deviceID, downloadID string) error
	PatchStatus(ctx context.Context, userID int, profileID, deviceID, downloadID, status string) error
	BuildManifest(ctx context.Context, userID int, profileID, deviceID, downloadID string, filter catalog.AccessFilter) (*downloads.OfflineManifest, error)
	BuildBatchManifests(ctx context.Context, userID int, profileID, deviceID, batchID string, filter catalog.AccessFilter) ([]*downloads.OfflineManifest, []downloads.SkippedManifest, error)
	ServeArtwork(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID, kind string, filter catalog.AccessFilter) error
	ServeSubtitle(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int, profileID, deviceID, downloadID, ref string, filter catalog.AccessFilter) error
}

// DownloadHandler handles download endpoints.
type DownloadHandler struct {
	svc DownloadService
}

// NewDownloadHandler creates a new DownloadHandler.
func NewDownloadHandler(svc DownloadService) *DownloadHandler {
	return &DownloadHandler{svc: svc}
}

// downloadRequest represents the JSON body for POST /downloads.
type downloadRequest struct {
	ContentID string        `json:"content_id"`
	EpisodeID string        `json:"episode_id,omitempty"`
	FileID    int           `json:"file_id,omitempty"`
	Quality   string        `json:"quality,omitempty"`       // original (default) | 20mbps | 10mbps | 5mbps | 2mbps | 1mbps
	Series    bool          `json:"series,omitempty"`        // if true, downloads all episodes
	Season    *int          `json:"season_number,omitempty"` // with series=true, downloads only this season (0 = Specials)
	Caps      *downloadCaps `json:"caps,omitempty"`          // device decode capability (original fallback / transcode target)
}

// downloadCaps mirrors playback.ClientCapabilities for the request body.
type downloadCaps struct {
	CodecsVideo            []string `json:"codecs_video,omitempty"`
	CodecsAudio            []string `json:"codecs_audio,omitempty"`
	AudioPassthroughCodecs []string `json:"audio_passthrough_codecs,omitempty"`
	Containers             []string `json:"containers,omitempty"`
	MaxResolution          string   `json:"max_resolution,omitempty"`
	HDR                    bool     `json:"hdr,omitempty"`
}

// patchDownloadRequest is the JSON body for PATCH /downloads/{id}.
type patchDownloadRequest struct {
	Status string `json:"status"`
}

// downloadResponse represents a download entry in API responses.
type downloadResponse struct {
	ID                string  `json:"id"`
	ContentID         string  `json:"content_id"`
	EpisodeID         string  `json:"episode_id,omitempty"`
	BatchID           string  `json:"batch_id,omitempty"`
	DeviceID          string  `json:"device_id,omitempty"`
	MediaFileID       int     `json:"media_file_id"`
	FileSize          int64   `json:"file_size"`
	BytesSent         int64   `json:"bytes_sent"`
	Kind              string  `json:"kind"`
	Status            string  `json:"status"`
	Quality           string  `json:"quality"`
	EffectiveQuality  string  `json:"effective_quality"`
	DeliveryFormat    string  `json:"delivery_format"`
	TargetBitrateKbps int     `json:"target_bitrate_kbps"`
	Revision          int     `json:"revision"`
	CreatedAt         string  `json:"created_at"`
	CompletedAt       *string `json:"completed_at,omitempty"`
}

// downloadsListResponse wraps the downloads list for JSON serialization.
type downloadsListResponse struct {
	Downloads []downloadResponse          `json:"downloads"`
	Skipped   []downloads.SkippedDownload `json:"skipped,omitempty"`
}

type batchManifestsResponse struct {
	Manifests []*downloads.OfflineManifest `json:"manifests"`
	// Skipped lists batch entries whose manifest could not be built (revoked,
	// deleted from the catalog, access-filtered); the rest of the batch is
	// still delivered.
	Skipped []downloads.SkippedManifest `json:"skipped,omitempty"`
}

// downloadCapabilityResponse is the GET /downloads/capability payload clients
// use for feature detection instead of introspecting admin settings.
type downloadCapabilityResponse struct {
	Enabled              bool     `json:"enabled"`
	DownloadAllowed      bool     `json:"download_allowed"`
	QualityPresets       []string `json:"quality_presets"`
	TranscodeEnabled     bool     `json:"transcode_enabled"`
	TranscodeUserAllowed bool     `json:"transcode_user_allowed"`
	SeasonDownload       bool     `json:"season_download"`
	SeriesMonitoring     bool     `json:"series_monitoring"`
	MonitoringModes      []string `json:"monitoring_modes,omitempty"`
}

func toDownloadResponse(d *downloads.Download) downloadResponse {
	resp := downloadResponse{
		ID:                d.ID,
		ContentID:         d.ContentID,
		EpisodeID:         d.EpisodeID,
		BatchID:           d.BatchID,
		DeviceID:          d.DeviceID,
		MediaFileID:       d.MediaFileID,
		FileSize:          d.FileSize,
		BytesSent:         d.BytesSent,
		Kind:              d.Kind,
		Status:            d.Status,
		Quality:           d.Quality,
		EffectiveQuality:  d.EffectiveQuality,
		DeliveryFormat:    d.Format,
		TargetBitrateKbps: d.TargetBitrateKbps,
		Revision:          d.Revision,
		CreatedAt:         d.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if d.CompletedAt != nil {
		s := d.CompletedAt.Format("2006-01-02T15:04:05Z07:00")
		resp.CompletedAt = &s
	}
	return resp
}

// managedIdentity returns the (profileID, deviceID) the request is acting as.
// deviceID comes ONLY from the X-Silo-Device-Id header (never the body/query);
// profileID is resolved by the viewer-access middleware from X-Profile-Id.
func managedIdentity(r *http.Request) (profileID, deviceID, deviceName, devicePlatform string) {
	device := deviceMetadataFromRequest(r)
	return apimw.GetProfileID(r.Context()), device.DeviceID, device.DeviceName, device.DevicePlatform
}

// HandleCapability handles GET /downloads/capability.
func (h *DownloadHandler) HandleCapability(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	capInfo, err := h.svc.Capability(r.Context(), userID)
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to load download capability", "component", "api", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to load download capability")
		return
	}

	writeJSON(w, http.StatusOK, downloadCapabilityResponse{
		Enabled:              capInfo.Enabled,
		DownloadAllowed:      capInfo.DownloadAllowed,
		QualityPresets:       capInfo.QualityPresets,
		TranscodeEnabled:     capInfo.TranscodeEnabled,
		TranscodeUserAllowed: capInfo.TranscodeUserAllowed,
		SeasonDownload:       capInfo.SeasonDownload,
		SeriesMonitoring:     capInfo.SeriesMonitoring,
		MonitoringModes:      capInfo.MonitoringModes,
	})
}

// HandleCreateDownload handles POST /downloads. The X-Silo-Device-Id header
// (if present) makes this a managed device entry; otherwise it is ephemeral.
func (h *DownloadHandler) HandleCreateDownload(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}
	if req.ContentID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "content_id is required")
		return
	}

	profileID, deviceID, deviceName, devicePlatform := managedIdentity(r)
	filter := requestAccessFilter(r)
	createReq := downloads.CreateRequest{
		ContentID:      req.ContentID,
		EpisodeID:      req.EpisodeID,
		FileID:         req.FileID,
		Quality:        req.Quality,
		ProfileID:      profileID,
		DeviceID:       deviceID,
		DeviceName:     deviceName,
		DevicePlatform: devicePlatform,
	}
	if req.Caps != nil {
		createReq.Caps = playback.ClientCapabilities{
			CodecsVideo:            req.Caps.CodecsVideo,
			CodecsAudio:            req.Caps.CodecsAudio,
			AudioPassthroughCodecs: req.Caps.AudioPassthroughCodecs,
			Containers:             req.Caps.Containers,
			MaxResolution:          req.Caps.MaxResolution,
			HDR:                    req.Caps.HDR,
		}
	}

	if req.Series {
		var (
			list    []*downloads.Download
			batchID string
			skipped []downloads.SkippedDownload
			err     error
		)
		// Dispatch on presence, not value: season 0 is the Specials season.
		if req.Season != nil {
			if *req.Season < 0 {
				writeError(w, http.StatusBadRequest, "bad_request", "season_number must be >= 0")
				return
			}
			list, batchID, skipped, err = h.svc.CreateSeason(r.Context(), userID, createReq, *req.Season, filter)
		} else {
			list, batchID, skipped, err = h.svc.CreateSeries(r.Context(), userID, createReq, filter)
		}
		if err != nil {
			h.writeDownloadError(w, err)
			return
		}
		responses := make([]downloadResponse, 0, len(list))
		for _, d := range list {
			resp := toDownloadResponse(d)
			if resp.BatchID == "" {
				resp.BatchID = batchID
			}
			responses = append(responses, resp)
		}
		writeJSON(w, http.StatusAccepted, downloadsListResponse{Downloads: responses, Skipped: skipped})
		return
	}

	dl, err := h.svc.Create(r.Context(), userID, createReq, filter)
	if err != nil {
		h.writeDownloadError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, toDownloadResponse(dl))
}

// HandleListDownloads handles GET /downloads. With a device header it returns
// the calling device's managed entries; otherwise the user's ephemeral rows.
func (h *DownloadHandler) HandleListDownloads(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	profileID, deviceID, _, _ := managedIdentity(r)
	list, err := h.svc.List(r.Context(), userID, profileID, deviceID)
	if err != nil {
		if errors.Is(err, downloads.ErrProfileRequired) {
			writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
			return
		}
		slog.ErrorContext(r.Context(), "failed to list downloads", "component", "api", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to list downloads")
		return
	}

	responses := make([]downloadResponse, 0, len(list))
	for _, d := range list {
		responses = append(responses, toDownloadResponse(d))
	}
	writeJSON(w, http.StatusOK, downloadsListResponse{Downloads: responses})
}

// HandleDeleteDownload handles DELETE /downloads/{id}.
func (h *DownloadHandler) HandleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Download ID is required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	profileID, deviceID, _, _ := managedIdentity(r)
	if err := h.svc.Delete(r.Context(), userID, profileID, deviceID, id); err != nil {
		if errors.Is(err, downloads.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Download not found")
			return
		}
		if errors.Is(err, downloads.ErrProfileRequired) {
			writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
			return
		}
		slog.ErrorContext(r.Context(), "failed to delete download", "component", "api", "download_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to delete download")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandlePatchDownload handles PATCH /downloads/{id} — a managed-only endpoint
// where a client confirms local state (downloading|completed).
func (h *DownloadHandler) HandlePatchDownload(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Download ID is required")
		return
	}

	var req patchDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid request body")
		return
	}

	if err := h.svc.PatchStatus(r.Context(), userID, profileID, deviceID, id, req.Status); err != nil {
		switch {
		case errors.Is(err, downloads.ErrNotFound):
			writeError(w, http.StatusNotFound, "not_found", "Download not found")
		case errors.Is(err, downloads.ErrInvalidStatus):
			writeError(w, http.StatusBadRequest, "invalid_status", "status must be downloading or completed")
		case errors.Is(err, downloads.ErrProfileRequired):
			writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
		default:
			slog.ErrorContext(r.Context(), "failed to patch download", "component", "api", "download_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal_error", "Failed to update download")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleDownloadFile handles GET /downloads/{id}/file.
func (h *DownloadHandler) HandleDownloadFile(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Download ID is required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	profileID, deviceID, _, _ := managedIdentity(r)
	filter := requestAccessFilter(r)
	if err := h.svc.ServeFile(r.Context(), w, r, userID, profileID, deviceID, id, filter); err != nil {
		if errors.Is(err, downloads.ErrNotFound) || errors.Is(err, catalog.ErrItemNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "Download not found")
			return
		}
		if errors.Is(err, downloads.ErrDownloadNotActive) {
			writeError(w, http.StatusConflict, "download_inactive", "This download is no longer active")
			return
		}
		if errors.Is(err, downloads.ErrProfileRequired) {
			writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
			return
		}
		if errors.Is(err, downloads.ErrFeatureDisabled) || errors.Is(err, downloads.ErrDownloadNotAllowed) {
			writeError(w, http.StatusForbidden, "forbidden", "You are not allowed to download")
			return
		}
		slog.ErrorContext(r.Context(), "failed to serve download file", "component", "api", "download_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to serve download")
		return
	}
}

// HandleDirectDownload handles GET /direct-download?file_id=N. A legacy
// format=original query is accepted, but direct downloads remain original-only.
func (h *DownloadHandler) HandleDirectDownload(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}

	fileIDStr := r.URL.Query().Get("file_id")
	fileID, err := strconv.Atoi(fileIDStr)
	if err != nil || fileID <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "file_id query parameter is required")
		return
	}

	filter := requestAccessFilter(r)
	if err := h.svc.ServeDirect(r.Context(), w, r, userID, fileID, r.URL.Query().Get("format"), filter); err != nil {
		h.writeDownloadError(w, err)
		return
	}
}

// requireManaged validates a managed (device-scoped) request: authentication, a
// configured service, and the device + profile identity (device_id from the
// X-Silo-Device-Id header only, never the body). On failure it writes the error
// response and returns ok=false. Shared by every managed-only endpoint — the
// offline assets and the series-monitoring subscriptions.
func (h *DownloadHandler) requireManaged(w http.ResponseWriter, r *http.Request) (userID int, profileID, deviceID, deviceName, devicePlatform string, ok bool) {
	userID = apimw.GetUserID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h.svc == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Downloads not configured")
		return
	}
	profileID, deviceID, deviceName, devicePlatform = managedIdentity(r)
	if deviceID == "" {
		writeError(w, http.StatusBadRequest, "device_id_required", "X-Silo-Device-Id header is required")
		return
	}
	if profileID == "" {
		writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
		return
	}
	return userID, profileID, deviceID, deviceName, devicePlatform, true
}

// managedAssetIdentity is requireManaged plus the URL {id} param, for the offline
// asset endpoints (manifest/artwork/subtitle).
func (h *DownloadHandler) managedAssetIdentity(w http.ResponseWriter, r *http.Request) (userID int, profileID, deviceID, id string, ok bool) {
	userID, profileID, deviceID, _, _, ok = h.requireManaged(w, r)
	if !ok {
		return userID, profileID, deviceID, "", false
	}
	id = chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Download ID is required")
		return userID, profileID, deviceID, "", false
	}
	return userID, profileID, deviceID, id, true
}

// HandleManifest handles GET /downloads/{id}/manifest (managed-only).
func (h *DownloadHandler) HandleManifest(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, id, ok := h.managedAssetIdentity(w, r)
	if !ok {
		return
	}
	manifest, err := h.svc.BuildManifest(r.Context(), userID, profileID, deviceID, id, requestAccessFilter(r))
	if err != nil {
		h.writeAssetError(w, "manifest", id, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

// HandleBatchManifests handles GET /downloads/batches/{batch_id}/manifests
// (managed-only).
func (h *DownloadHandler) HandleBatchManifests(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, _, _, ok := h.requireManaged(w, r)
	if !ok {
		return
	}
	batchID := chi.URLParam(r, "batch_id")
	if batchID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "Batch ID is required")
		return
	}
	manifests, skipped, err := h.svc.BuildBatchManifests(r.Context(), userID, profileID, deviceID, batchID, requestAccessFilter(r))
	if err != nil {
		h.writeAssetError(w, "batch_manifests", batchID, err)
		return
	}
	writeJSON(w, http.StatusOK, batchManifestsResponse{Manifests: manifests, Skipped: skipped})
}

// HandleArtwork handles GET /downloads/{id}/artwork/{kind} (managed-only).
func (h *DownloadHandler) HandleArtwork(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, id, ok := h.managedAssetIdentity(w, r)
	if !ok {
		return
	}
	kind := chi.URLParam(r, "kind")
	if err := h.svc.ServeArtwork(r.Context(), w, r, userID, profileID, deviceID, id, kind, requestAccessFilter(r)); err != nil {
		h.writeAssetError(w, "artwork", id, err)
		return
	}
}

// HandleSubtitle handles GET /downloads/{id}/subtitles/{ref} (managed-only).
func (h *DownloadHandler) HandleSubtitle(w http.ResponseWriter, r *http.Request) {
	userID, profileID, deviceID, id, ok := h.managedAssetIdentity(w, r)
	if !ok {
		return
	}
	ref := chi.URLParam(r, "ref")
	if err := h.svc.ServeSubtitle(r.Context(), w, r, userID, profileID, deviceID, id, ref, requestAccessFilter(r)); err != nil {
		h.writeAssetError(w, "subtitle", id, err)
		return
	}
}

// writeAssetError maps offline asset (manifest/artwork/subtitle) errors. Access
// denials and missing assets collapse to 404 so a download id never reveals the
// existence of out-of-scope content.
func (h *DownloadHandler) writeAssetError(w http.ResponseWriter, asset, id string, err error) {
	switch {
	case errors.Is(err, downloads.ErrNotFound),
		errors.Is(err, downloads.ErrAssetNotFound),
		errors.Is(err, catalog.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Not found")
	case errors.Is(err, downloads.ErrInvalidSubtitleRef):
		writeError(w, http.StatusBadRequest, "invalid_subtitle_ref", "Invalid subtitle reference")
	case errors.Is(err, downloads.ErrDownloadNotActive):
		writeError(w, http.StatusConflict, "download_inactive", "This download is no longer active")
	case errors.Is(err, downloads.ErrProfileRequired):
		writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
	case errors.Is(err, downloads.ErrManifestUnavailable):
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Offline assets are not configured")
	case errors.Is(err, downloads.ErrFeatureDisabled), errors.Is(err, downloads.ErrDownloadNotAllowed):
		writeError(w, http.StatusForbidden, "forbidden", "You are not allowed to download")
	default:
		slog.Error("failed to serve download asset", "asset", asset, "download_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to serve download asset")
	}
}

func (h *DownloadHandler) writeDownloadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, downloads.ErrFeatureDisabled):
		writeError(w, http.StatusForbidden, "feature_disabled", "Downloads are disabled")
	case errors.Is(err, downloads.ErrDownloadNotAllowed):
		writeError(w, http.StatusForbidden, "forbidden", "You are not allowed to download")
	case errors.Is(err, downloads.ErrTranscodeDisabled):
		writeError(w, http.StatusForbidden, "transcode_disabled", "Download transcoding is disabled")
	case errors.Is(err, downloads.ErrInvalidQuality):
		writeError(w, http.StatusBadRequest, "invalid_quality", "Unknown download quality")
	case errors.Is(err, downloads.ErrProfileRequired):
		writeError(w, http.StatusBadRequest, "profile_required", "A profile is required for managed downloads")
	case errors.Is(err, downloads.ErrBulkQualityUnavailable):
		writeError(w, http.StatusNotImplemented, "bulk_quality_unavailable", "Bitrate quality is not available for bulk downloads yet")
	case errors.Is(err, downloads.ErrQualityUnavailable):
		writeError(w, http.StatusNotImplemented, "quality_unavailable", "This download quality is not available")
	case errors.Is(err, downloads.ErrFormatUnavailable):
		writeError(w, http.StatusNotImplemented, "format_unavailable", "This download format is not available yet")
	case errors.Is(err, downloads.ErrConcurrentLimitReached):
		writeError(w, http.StatusTooManyRequests, "download_limit_exceeded", "Maximum concurrent downloads reached")
	case errors.Is(err, downloads.ErrPeriodLimitReached):
		writeError(w, http.StatusTooManyRequests, "download_quota_exceeded", "Download quota exceeded for this period")
	case errors.Is(err, downloads.ErrNoDownloadableEpisodes):
		writeError(w, http.StatusNotFound, "no_downloadable_episodes", "No downloadable episodes found")
	case errors.Is(err, catalog.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "not_found", "Media item not found")
	default:
		slog.Error("download operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to process download")
	}
}
