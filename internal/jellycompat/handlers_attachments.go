package jellycompat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// Keep the slot through delivery, bounding both extraction processes and font
// buffers held by slow clients. Waiting and extraction each have a bounded budget.
var compatAttachmentSlots = make(chan struct{}, 2)

// HandleAttachment serves a real font attachment by its original container
// stream index. It shares subtitle authorization, source resolution, extraction
// limits and cancellation with the native playback service.
func (h *PlaybackHandler) HandleAttachment(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	routeID := chiURLParam(r, "routeItemId")
	if routeID == "" {
		routeID = chiURLParam(r, "id")
	}
	mediaSourceID := chiURLParam(r, "routeMediaSourceId")
	negotiated, source, err := h.resolvePlaybackRoute(r, session, routeID, mediaSourceID)
	if err != nil || source == nil || negotiated == nil || !mediaSourceIDsEqual(negotiated.RouteItemID, routeID) || !mediaSourceIDsEqual(source.ID, mediaSourceID) {
		writeError(w, 404, "NotFound", "Playback source not found")
		return
	}
	if h.content == nil {
		writeError(w, 503, "Unavailable", "Catalog unavailable")
		return
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, negotiated.ItemID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return
	}
	authorized := false
	for _, version := range detail.Versions {
		if version.FileID == source.FileID {
			authorized = true
			break
		}
	}
	if !authorized {
		writeError(w, 404, "NotFound", "Media source not found")
		return
	}

	index, err := strconv.Atoi(chiURLParam(r, "routeIndex"))
	if err != nil || index < 0 {
		writeError(w, 400, "BadRequest", "Invalid attachment index")
		return
	}
	if h.fileResolver == nil {
		writeError(w, 503, "Unavailable", "File resolver unavailable")
		return
	}
	file, err := h.fileResolver.GetByID(r.Context(), source.FileID)
	if err != nil {
		writeError(w, 404, "NotFound", "Media file not found")
		return
	}
	attachCompatStream(r.Context(), session, negotiated, source.FileID)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	select {
	case compatAttachmentSlots <- struct{}{}:
		defer func() { <-compatAttachmentSlots }()
	case <-ctx.Done():
		writeError(w, 503, "Unavailable", "Attachment extraction is busy")
		return
	}
	// Queue time must not consume the extractor's own thirty-second budget.
	cancel()
	font, err := playback.ExtractAttachedSubtitleFont(r.Context(), file.FilePath, h.FFmpegPath, index)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || r.Context().Err() != nil {
			writeError(w, 503, "Unavailable", "Attachment extraction timed out")
			return
		}
		writeError(w, 500, "ServerError", "Failed to extract attachments")
		return
	}
	if font != nil {
		typ := mime.TypeByExtension(filepath.Ext(font.Name))
		if typ == "" {
			typ = "application/octet-stream"
		}
		w.Header().Set("Content-Type", typ)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": font.Name}))
		http.ServeContent(w, r, font.Name, time.Time{}, bytes.NewReader(font.Data))
		return
	}
	writeError(w, 404, "NotFound", "Font attachment not found")
}

// mediaAttachments probes only containers with ASS/SSA tracks, where attached
// fonts affect rendering. All versions share PlaybackInfo's two-second budget.
func (h *PlaybackHandler) mediaAttachments(ctx context.Context, itemID, playSessionID string, source PlaybackMediaSource) []map[string]any {
	attachments := []map[string]any{}
	relevant := false
	for _, track := range source.Version.SubtitleTracks {
		if strings.EqualFold(track.Codec, "ass") || strings.EqualFold(track.Codec, "ssa") {
			relevant = true
			break
		}
	}
	if !relevant || source.Version.FilePath == "" || ctx.Err() != nil {
		return attachments
	}
	fonts, err := playback.ListAttachedSubtitleFonts(ctx, source.Version.FilePath, h.FFmpegPath)
	if err != nil {
		return attachments
	}
	for _, font := range fonts {
		delivery := fmt.Sprintf("/Videos/%s/%s/Attachments/%d?PlaySessionId=%s", url.PathEscape(itemID), url.PathEscape(source.ID), font.Index, url.QueryEscape(playSessionID))
		attachments = append(attachments, map[string]any{"Index": font.Index, "Codec": font.Codec, "FileName": font.FileName, "MimeType": font.MimeType, "DeliveryUrl": delivery})
	}
	return attachments
}
