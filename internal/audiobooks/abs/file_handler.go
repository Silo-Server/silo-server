package abs

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
//   - Set Content-Disposition: attachment on /download paths to encourage
//     browser save-to-disk / mobile offline-save behaviour.
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

	// /download variant: hint the client to save rather than stream.
	if strings.HasSuffix(r.URL.Path, "/download") {
		filename := filepath.Base(mediaFile.FilePath)
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	}

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
