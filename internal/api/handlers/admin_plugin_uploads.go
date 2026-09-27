package handlers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/Silo-Server/silo-server/internal/uploads"
)

// PluginChunkedUploadCreateInput opens one process-local upload session.
type PluginChunkedUploadCreateInput struct {
	Filename  string
	SizeBytes int64
	ChunkSize int64
}

// ErrPluginUploadMissingArchive reports a multipart upload without the
// "archive" file part.
var ErrPluginUploadMissingArchive = errors.New("archive upload is required")

func (h *PluginHandler) pluginUploadsReady() error {
	if h == nil || h.uploads == nil {
		return apiError(http.StatusServiceUnavailable, "unavailable", "Plugin uploads not configured")
	}
	return nil
}

// InstallAdminPluginUpload reads the "archive" part of a multipart request,
// spools it to a temporary file and installs it: a zip archive through the
// archive installer, anything else as a plugin binary whose manifest is read
// by executing it. The caller (both listeners' media gates) bounds the body.
// An installation with the same plugin_id is replaced; there is no replay
// identity. Errors: ErrPluginUploadMissingArchive, *APIError, install errors.
func (h *PluginHandler) InstallAdminPluginUpload(ctx context.Context, r *http.Request) (PluginInstallationView, error) {
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	if err := r.ParseMultipartForm(maxPluginUploadSize); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return PluginInstallationView{}, apiError(http.StatusRequestEntityTooLarge, "too_large", "Upload exceeds the maximum allowed size")
		}
		return PluginInstallationView{}, fieldError("archive", "Invalid plugin upload")
	}
	file, _, err := r.FormFile("archive")
	if err != nil {
		return PluginInstallationView{}, ErrPluginUploadMissingArchive
	}
	defer func() { _ = file.Close() }()
	tempFile, err := os.CreateTemp("", "silo-plugin-*.zip")
	if err != nil {
		return PluginInstallationView{}, fmt.Errorf("create temp plugin upload file: %w", err)
	}
	tempPath := tempFile.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := io.Copy(tempFile, file); err != nil {
		_ = tempFile.Close()
		return PluginInstallationView{}, fmt.Errorf("write temp plugin upload file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return PluginInstallationView{}, fmt.Errorf("close temp plugin upload file: %w", err)
	}
	result, err := h.installUploadedPlugin(ctx, tempPath)
	if err != nil {
		return PluginInstallationView{}, err
	}
	return h.installedPluginView(ctx, result)
}

// CreateAdminPluginUpload opens a process-local chunked upload session. Every
// call creates a new session with a fresh identifier; the session lives on
// this replica only and expires after inactivity. Errors: uploads.Err*.
func (h *PluginHandler) CreateAdminPluginUpload(_ context.Context, in PluginChunkedUploadCreateInput) (uploads.SessionInfo, error) {
	if err := h.pluginUploadsReady(); err != nil {
		return uploads.SessionInfo{}, err
	}
	if in.ChunkSize == 0 {
		in.ChunkSize = defaultPluginChunkSize
	}
	return h.uploads.Create(uploads.CreateRequest{Filename: in.Filename, SizeBytes: in.SizeBytes, ChunkSize: in.ChunkSize})
}

// PutAdminPluginUploadChunk stores one chunk. A chunk index already received
// is accepted without rewriting, so a repeat of the same bytes is a no-op; the
// body length must equal the session's chunk size for that index. Errors:
// uploads.Err*.
func (h *PluginHandler) PutAdminPluginUploadChunk(ctx context.Context, uploadID string, chunkIndex int, body io.Reader, contentLength int64) (uploads.SessionInfo, error) {
	if err := h.pluginUploadsReady(); err != nil {
		return uploads.SessionInfo{}, err
	}
	return h.uploads.PutChunk(ctx, uploadID, chunkIndex, body, contentLength)
}

// CompleteAdminPluginUpload consumes a fully received session and installs
// the assembled file as InstallAdminPluginUpload does. The session is removed
// before the install runs, so a repeat finds no session and a lost response is
// not a receipt. Errors: uploads.Err*, install errors.
func (h *PluginHandler) CompleteAdminPluginUpload(ctx context.Context, uploadID string) (PluginInstallationView, error) {
	if err := h.pluginUploadsReady(); err != nil {
		return PluginInstallationView{}, err
	}
	if err := h.pluginLifecycleReady(); err != nil {
		return PluginInstallationView{}, err
	}
	upload, err := h.uploads.Complete(uploadID)
	if err != nil {
		return PluginInstallationView{}, err
	}
	defer upload.Cleanup()
	result, err := h.installUploadedPlugin(ctx, upload.Path)
	if err != nil {
		return PluginInstallationView{}, err
	}
	return h.installedPluginView(ctx, result)
}

// CancelAdminPluginUpload discards a session; an absent session is success.
func (h *PluginHandler) CancelAdminPluginUpload(_ context.Context, uploadID string) error {
	if err := h.pluginUploadsReady(); err != nil {
		return err
	}
	if err := h.uploads.Cancel(uploadID); err != nil && !errors.Is(err, uploads.ErrNotFound) {
		return err
	}
	return nil
}

// writePluginUploadError keeps the frozen bridge answers for the upload seams.
func writePluginUploadError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *APIError
	switch {
	case errors.As(err, &apiErr):
		writeError(w, apiErr.Status, apiErr.Code, apiErr.Message)
	case errors.Is(err, ErrPluginUploadMissingArchive):
		writeError(w, http.StatusBadRequest, "bad_request", "archive upload is required")
	default:
		status, message := uploadErrorResponse(err)
		if status == http.StatusInternalServerError {
			slog.ErrorContext(r.Context(), "plugin upload failed", "component", "api", "error", err)
			writeError(w, status, "internal_error", "Failed to install uploaded plugin")
			return
		}
		writeError(w, status, "upload_error", message)
	}
}
