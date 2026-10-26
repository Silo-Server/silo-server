package jellycompat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// DownloadServer serves an original media file under Silo's download rules:
// the server's Allow downloads setting, the account's download permission,
// the policy engine, library and quality access, and the download bandwidth
// limits. *downloads.Service satisfies it through the native direct-download
// path.
type DownloadServer interface {
	ServeDirect(ctx context.Context, w http.ResponseWriter, r *http.Request, userID, fileID int, format string, filter catalog.AccessFilter) error
}

// HandleDownload serves the original media file for /Items/{id}/Download.
// This route backs the CanDownload flag set in mapping.go. CanDownload is
// load-bearing for Infuse: it refuses Direct Play (Static=true streaming)
// for items it believes it cannot download, so the flag stays true for every
// account and this route enforces the download rules instead. An account
// that may not download gets 403 here while its static streams keep playing.
func (h *PlaybackHandler) HandleDownload(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}

	contentID, routeFileID, err := decodeContentOrMediaSourceID(r.Context(), h.codec, chiURLParam(r, "id"))
	if err != nil {
		writeItemIDError(w, r, err)
		return
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil || detail == nil || len(detail.Versions) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Item not found")
		return
	}

	version := detail.Versions[0]
	mediaSourceID := firstNonEmpty(r.URL.Query().Get("mediaSourceId"), r.URL.Query().Get("MediaSourceId"))
	sourceFromRoute := mediaSourceID == "" && routeFileID > 0
	if sourceFromRoute {
		mediaSourceID = h.codec.EncodeIntID(EncodedIDMediaSource, routeFileID)
	}
	if mediaSourceID != "" {
		matched := false
		if fileID, decodeErr := h.codec.DecodeIntID(EncodedIDMediaSource, mediaSourceID); decodeErr == nil {
			for _, v := range detail.Versions {
				if int64(v.FileID) == fileID {
					version = v
					matched = true
					break
				}
			}
		}
		if !matched && sourceFromRoute {
			// The route named a version the item no longer has; do not serve
			// a different file.
			writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
			return
		}
	}

	if h.downloads == nil {
		writeError(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Downloads are not available")
		return
	}
	var filter catalog.AccessFilter
	if h.accessFilter != nil {
		filter = h.accessFilter(r.Context(), session.StreamAppUserID, session.ProfileID)
	}
	serveCtx := downloads.WithServeAuthorized(r.Context(), func(target downloads.FileTarget) {
		// §4.2b: a download has a user but no stable playback session, so it
		// is a Transfer rather than a logical session.
		attachCompatTransfer(r.Context(), session, target.MediaFileID)
	})
	// Keep the validator this route sent before it applied the download
	// rules, so a client resuming with If-Range still matches.
	serveCtx = downloads.WithServeEntityTag(serveCtx, playback.DirectPlayEntityTag)
	err = h.downloads.ServeDirect(serveCtx, httpstream.NewRollingDeadlineWriter(w), r, session.StreamAppUserID, version.FileID, "", filter)
	if err == nil || errors.Is(err, downloads.ErrResponseCommitted) {
		// Served, or failed after the body started: an error body now would
		// be appended to the file.
		return
	}
	switch {
	case errors.Is(err, downloads.ErrFeatureDisabled),
		errors.Is(err, downloads.ErrDownloadNotAllowed),
		errors.Is(err, downloads.ErrTranscodeDisabled),
		errors.Is(err, downloads.ErrQualityUnavailable):
		writeError(w, http.StatusForbidden, "Forbidden", "You are not allowed to download this item")
	case errors.Is(err, catalog.ErrItemNotFound):
		writeError(w, http.StatusNotFound, "NotFound", "Media file not found")
	default:
		slog.ErrorContext(r.Context(), "jellycompat download failed", "component", "jellycompat", "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to serve the download")
	}
}
