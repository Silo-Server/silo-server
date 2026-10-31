package abs

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
	"github.com/Silo-Server/silo-server/internal/httpstream"
	"github.com/Silo-Server/silo-server/internal/playback"
)

const publicTrackSessionTTL = 24 * time.Hour

// trackInoFor derives a stable, ABS-compatible inode string from a content ID
// and a 0-based file index. Real ABS uses the filesystem inode (a large
// positive BigInt-shaped string); we hash to a 12-hex-digit prefix and parse
// it as a decimal so the client sees an identifier of the same shape.
//
// Stability matters: the mobile app keys offline downloads by ino. This
// implementation must remain bit-for-bit identical to the ino that handlePlayStart
// embeds in the track list, otherwise file lookups will fail.
func trackInoFor(contentID string, fileIdx int) string {
	sum := md5.Sum([]byte(contentID + "/" + strconv.Itoa(fileIdx)))
	hexStr := hex.EncodeToString(sum[:6])
	n, _ := strconv.ParseUint(hexStr, 16, 64)
	return strconv.FormatUint(n, 10)
}

// handleFileStream serves the audio bytes for one file of an audiobook library
// item. Real ABS uses /api/items/{id}/file/{ino}/download for offline-save
// and /api/items/{id}/file/{ino}?token=<jwt> for iOS streaming; both URL
// patterns share this handler.
//
// Auth is via the bearerAuth middleware, which accepts the Authorization header
// and a ?token= query-param fallback (the iOS streaming variant uses ?token=
// because AVPlayer doesn't add Authorization on its own subrequests).
//
// "ino" in real ABS is the file's filesystem inode. We synthesise an
// MD5-derived inode-shaped string per trackInoFor — the same value emitted by
// handlePlayStart. To reverse: call GetMediaFiles for the item, then find the
// file whose index matches the ino. As a fallback we also accept a bare
// 0-based integer index.
//
// Behaviour:
//   - Validate ABS bearer token (bearerAuth middleware has already done this).
//   - Look up the requested file in silo's media_files table.
//   - Serve the bytes directly with Range-request support via playback.ServeDirectPlay.
//   - Serve /download paths through serveFileDownload instead, which applies
//     the download rules. As in real ABS, only /download needs the download
//     permission: the bare route is a stream, which an account that may not
//     download still plays, as on the native and Jellyfin-compatible APIs.
func (h *Handler) handleFileStream(w http.ResponseWriter, r *http.Request) {
	a, ok := absAuthFrom(r)
	if !ok || a.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	contentID := chi.URLParam(r, "libraryItemId")
	inoStr := chi.URLParam(r, "ino")

	access, err := h.accessFilterForAuth(r.Context(), a)
	if err != nil {
		http.Error(w, "resolve access: "+err.Error(), http.StatusForbidden)
		return
	}
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), contentID, access)
	if err != nil || len(files) == 0 {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	// Resolve the ino back to a file by recomputing trackInoFor for each file
	// at its 0-based position in the sorted slice. Keeping the resolution logic
	// here (symmetric with handlePlayStart's generation) means both paths stay
	// in sync whenever the sort order or ino derivation changes.
	fileIdx := -1
	for i, f := range files {
		if trackInoFor(contentID, i) == inoStr {
			fileIdx = i
			_ = f
			break
		}
	}

	// Fallback: legacy or third-party callers sometimes pass the raw 0-based
	// file index directly. Accept it when it resolves to a real position.
	if fileIdx < 0 {
		if n, err := strconv.Atoi(inoStr); err == nil && n >= 0 && n < len(files) {
			fileIdx = n
		}
	}

	if fileIdx < 0 {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	mediaFile := files[fileIdx]
	if strings.HasSuffix(r.URL.Path, "/download") {
		h.serveFileDownload(w, r, a, mediaFile.ID, access)
		return
	}
	// §4.2b: a bare file stream has a user but no stable playback session, so it
	// is a Transfer, never cap-relevant and never subject to per-session rules.
	attachABSTransfer(r.Context(), a.UserID, a.ProfileID, mediaFile.ID)

	// Set Content-Type for audio files. ServeDirectPlay uses MimeFromExtension
	// which covers video containers; we override with audio-specific MIME
	// types because ABS clients pattern-match on Content-Type.
	ext := strings.ToLower(filepath.Ext(mediaFile.FilePath))
	if ct := audioContentType(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	if err := playback.ServeDirectPlay(w, r, mediaFile.FilePath); err != nil {
		// ServeDirectPlay has already written an error response; just log.
		return
	}
}

// serveFileDownload serves the /download variant through Silo's native direct
// download, as jellycompat's /Items/{id}/Download does, so it follows the same
// rules (see Downloads) and answers 403 when they refuse. The direct download
// sets Content-Disposition: attachment and an audio Content-Type.
func (h *Handler) serveFileDownload(w http.ResponseWriter, r *http.Request, a ctxAuth, fileID int, access catalog.AccessFilter) {
	userID, err := strconv.Atoi(a.UserID)
	if h.downloads == nil || err != nil {
		http.Error(w, "download not allowed", http.StatusForbidden)
		return
	}
	ctx := downloads.WithServeAuthorized(r.Context(), func(target downloads.FileTarget) {
		// §4.2b: a download has a user but no stable playback session, so it
		// is a Transfer.
		attachABSTransfer(r.Context(), a.UserID, a.ProfileID, target.MediaFileID)
	})
	// Keep the validator this route sent before it applied the download
	// rules, so a client resuming with If-Range still matches.
	ctx = downloads.WithServeEntityTag(ctx, playback.DirectPlayEntityTag)
	err = h.downloads.ServeDirect(ctx, httpstream.NewRollingDeadlineWriter(w), r, userID, fileID, "", access)
	switch {
	case err == nil, errors.Is(err, downloads.ErrResponseCommitted):
		// Served, or failed after the body started: an error body now would
		// be appended to the file.
	case downloadRefused(err):
		http.Error(w, "download not allowed", http.StatusForbidden)
	case errors.Is(err, catalog.ErrItemNotFound):
		http.Error(w, "file not found", http.StatusNotFound)
	default:
		slog.ErrorContext(r.Context(), "abs download failed", "component", "audiobooks", "err", err, "user", a.UserID)
		http.Error(w, "download failed", http.StatusInternalServerError)
	}
}

// checkDownload returns nil when userID may download now. Without a download
// service it refuses, as downloads.ErrFeatureDisabled.
func (h *Handler) checkDownload(ctx context.Context, userID string) error {
	if h.downloads == nil {
		return downloads.ErrFeatureDisabled
	}
	uid, err := strconv.Atoi(userID)
	if err != nil {
		return downloads.ErrDownloadNotAllowed
	}
	return h.downloads.CheckDownloadAllowed(ctx, uid)
}

// mayDownload reports the account's download permission for the ABS user
// object. A failed check reports false.
func (h *Handler) mayDownload(ctx context.Context, userID string) bool {
	err := h.checkDownload(ctx, userID)
	if err != nil && !downloadRefused(err) {
		slog.WarnContext(ctx, "abs download permission check failed", "component", "audiobooks", "err", err, "user", userID)
	}
	return err == nil
}

// downloadRefused reports whether err is a download-rule refusal rather than a
// failure to check.
func downloadRefused(err error) bool {
	return errors.Is(err, downloads.ErrFeatureDisabled) ||
		errors.Is(err, downloads.ErrDownloadNotAllowed) ||
		errors.Is(err, downloads.ErrTranscodeDisabled) ||
		errors.Is(err, downloads.ErrQualityUnavailable)
}

// handlePublicTrack serves audio bytes for ONE track of a playback session.
//
// Real ABS Android client (v2.22.0+) builds the streaming URL as
//
//	$serverAddress/public/session/{sessionId}/track/{audioTrack.index}
//
// WITHOUT appending any ?token=. The session ID itself is the capability:
// it's a 128-bit ULID, only known to the client that received it from
// /play, and tied server-side to (userID, contentID). This matches both
// the canonical continuum-plugin handler and booklore-ng's implementation.
//
// See android: PlaybackSession.kt:getContentUri (gte 2.22.0 + DirectPlay branch).
//
// Resolution:
//  1. Look up the session by sid via PlaybackSessionStore.
//  2. Load the ordered media-files list for the session's contentID.
//  3. files[idx-1] is the requested track (silo emits 1-based wireIndex).
//  4. Stream via playback.ServeDirectPlay (handles Range + HEAD).
//
// Mounted OUTSIDE bearerAuth: the client sends no Authorization header on
// this endpoint, and the session ID alone authorises access.
func (h *Handler) handlePublicTrack(w http.ResponseWriter, r *http.Request) {
	sid := chi.URLParam(r, "sid")
	idxStr := chi.URLParam(r, "idx")
	if sid == "" || idxStr == "" {
		http.Error(w, "sid and idx required", http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 1 {
		http.Error(w, "idx must be a positive integer", http.StatusBadRequest)
		return
	}
	if h.deps.PlaybackSessionStore == nil {
		http.Error(w, "session store not configured", http.StatusServiceUnavailable)
		return
	}

	sess, err := h.deps.PlaybackSessionStore.GetPlaybackSession(r.Context(), sid)
	if err != nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	if sess.ClosedAt != nil {
		http.Error(w, "session closed", http.StatusGone)
		return
	}
	if publicTrackSessionExpired(sess, time.Now()) {
		http.Error(w, "session expired", http.StatusGone)
		return
	}
	if h.beginNativePlaybackTransport(sid) {
		defer h.endNativePlaybackTransport(sid)
	}

	access, err := h.accessFilterForAuth(r.Context(), ctxAuth{UserID: sess.UserID, ProfileID: sess.ProfileID})
	if err != nil {
		http.Error(w, "session access denied", http.StatusForbidden)
		return
	}
	files, err := h.deps.MediaStore.GetMediaFiles(r.Context(), sess.ContentID, access)
	if err != nil || len(files) == 0 {
		http.Error(w, "item files not found", http.StatusNotFound)
		return
	}
	if idx > len(files) {
		http.Error(w, "track index out of range", http.StatusNotFound)
		return
	}
	mediaFile := files[idx-1]
	// The session id IS the capability on this route, and it is also the
	// canonical session key. Everything above this point is authorization.
	attachABSSession(r.Context(), sid, sess.UserID, sess.ProfileID, mediaFile.ID, sess.StartedAt)

	ext := strings.ToLower(filepath.Ext(mediaFile.FilePath))
	if ct := audioContentType(ext); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	_ = playback.ServeDirectPlay(w, r, mediaFile.FilePath)
}

func publicTrackSessionExpired(sess ABSPlaybackSession, now time.Time) bool {
	anchor := sess.StartedAt
	if sess.LastSyncAt.After(anchor) {
		anchor = sess.LastSyncAt
	}
	return !anchor.IsZero() && now.After(anchor.Add(publicTrackSessionTTL))
}

// audioContentType returns an audio MIME type for the given file extension
// (including the dot). Returns empty string for unknown extensions, letting
// ServeDirectPlay fall back to its own MIME detection.
func audioContentType(ext string) string {
	switch ext {
	case ".mp3":
		return "audio/mpeg"
	case ".m4b", ".m4a":
		return "audio/mp4"
	case ".flac":
		return "audio/flac"
	case ".ogg":
		return "audio/ogg"
	case ".opus":
		return "audio/opus"
	case ".wav":
		return "audio/wav"
	case ".aac":
		return "audio/aac"
	}
	return ""
}
