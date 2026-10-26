package jellycompat

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

// TrickplaySheets opens published seek-bar preview sheets
// (*trickplay.Reader).
type TrickplaySheets interface {
	OpenSheet(ctx context.Context, fileID, width, index int) (io.ReadCloser, string, bool, error)
}

// The Jellyfin trickplay routes. Clients read the Trickplay member of an item
// for the layout and fetch sheets by position; the playlist form serves
// players that read HLS image playlists.
const (
	compatTrickplaySheetRoute    = "/Videos/{itemId}/Trickplay/{width}/{index}.jpg"
	compatTrickplayPlaylistRoute = "/Videos/{itemId}/Trickplay/{width}/tiles.m3u8"
	// compatMediaSourceIDQuery names the media source a trickplay request is
	// for; clients spell it in either case.
	compatMediaSourceIDQuery = "MediaSourceId"
)

// HandleTrickplaySheet serves one sheet of an item's seek-bar previews. The
// bytes are proxied rather than redirected: some clients (Roku) do not follow
// a redirect for images.
func (h *PlaybackHandler) HandleTrickplaySheet(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	width, errWidth := strconv.Atoi(chiURLParam(r, "width"))
	index, errIndex := strconv.Atoi(chiURLParam(r, "index"))
	if errWidth != nil || errIndex != nil || h.Trickplay == nil {
		writeError(w, http.StatusNotFound, "NotFound", "Trickplay sheet not found")
		return
	}
	version, ok := h.trickplayVersion(w, r, session)
	if !ok {
		return
	}
	body, etag, ok, err := h.Trickplay.OpenSheet(r.Context(), version.FileID, width, index)
	if err != nil {
		slog.ErrorContext(r.Context(), "jellycompat: reading trickplay sheet failed", "component", "jellycompat", "file_id", version.FileID, "error", err)
		writeError(w, http.StatusInternalServerError, "ServerError", "Failed to read trickplay sheet")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "NotFound", "Trickplay sheet not found")
		return
	}
	defer func() { _ = body.Close() }()
	header := w.Header()
	header.Set("ETag", etag)
	// A sheet URL can resolve to another file after a version switch, or to
	// another revision after regeneration. Revalidate before reusing it.
	header.Set("Cache-Control", "private, no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Type", "image/jpeg")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.Copy(w, body)
	}
}

// HandleTrickplayPlaylist serves an HLS image playlist of an item's sheets,
// in the form Jellyfin writes: one segment per sheet, each tiled
// EXT-X-TILES, sheet URLs carrying the caller's token.
func (h *PlaybackHandler) HandleTrickplayPlaylist(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, http.StatusUnauthorized, "Unauthorized", "Missing authentication token")
		return
	}
	width, err := strconv.Atoi(chiURLParam(r, "width"))
	if err != nil {
		writeError(w, http.StatusNotFound, "NotFound", "Trickplay playlist not found")
		return
	}
	version, ok := h.trickplayVersion(w, r, session)
	if !ok {
		return
	}
	grid := version.Trickplay
	if grid == nil || grid.Width != width {
		writeError(w, http.StatusNotFound, "NotFound", "Trickplay playlist not found")
		return
	}
	query := url.Values{compatMediaSourceIDQuery: {h.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))}}
	if token, ok := ExtractToken(r); ok {
		query.Set("ApiKey", token)
	}
	w.Header().Set("Content-Type", "application/x-mpegURL")
	w.Header().Set("Cache-Control", "private, no-cache")
	_, _ = io.WriteString(w, trickplayPlaylist(*grid, query.Encode()))
}

// trickplayPlaylist writes the image playlist of grid's sheets.
func trickplayPlaylist(grid catalog.TrickplayGrid, query string) string {
	perSheet := grid.TileColumns * grid.TileRows
	interval := float64(grid.IntervalMS) / 1000
	sheets := (grid.ThumbnailCount + perSheet - 1) / perSheet
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", int(math.Ceil(float64(perSheet)*interval)))
	b.WriteString("#EXT-X-VERSION:7\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-IMAGES-ONLY\n")
	for sheet := range sheets {
		thumbnails := min(perSheet, grid.ThumbnailCount-sheet*perSheet)
		fmt.Fprintf(&b, "#EXTINF:%s,\n", strconv.FormatFloat(float64(thumbnails)*interval, 'f', -1, 64))
		fmt.Fprintf(&b, "#EXT-X-TILES:RESOLUTION=%dx%d,LAYOUT=%dx%d,DURATION=%s\n",
			grid.Width, grid.Height, grid.TileColumns, grid.TileRows, strconv.FormatFloat(interval, 'f', -1, 64))
		fmt.Fprintf(&b, "%d.jpg?%s\n", sheet, query)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// trickplayVersion resolves the file whose previews a request names, among
// the item's versions this account can see, and answers the request itself
// when there is none. The file is the mediaSourceId query's, else the route's
// when it names a media source, else the one this token is playing for the
// item (Swiftfin and Findroid send no mediaSourceId), else the item's default
// version.
func (h *PlaybackHandler) trickplayVersion(w http.ResponseWriter, r *http.Request, session *Session) (catalog.FileVersion, bool) {
	if h.content == nil {
		writeError(w, http.StatusServiceUnavailable, "Unavailable", "Catalog unavailable")
		return catalog.FileVersion{}, false
	}
	contentID, fileID, err := decodeItemOrMediaSourceID(r.Context(), h.codec, chiURLParam(r, "itemId"))
	if err != nil {
		writeItemIDError(w, r, err)
		return catalog.FileVersion{}, false
	}
	if raw := newCaseInsensitiveQuery(r.URL.Query()).Get(compatMediaSourceIDQuery); raw != "" {
		id, err := h.codec.DecodeIntID(EncodedIDMediaSource, raw)
		if err != nil {
			writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
			return catalog.FileVersion{}, false
		}
		fileID = id
	}
	detail, err := h.content.GetItemDetail(r.Context(), session, contentID, nil)
	if err != nil {
		writeCompatUpstreamError(w, err)
		return catalog.FileVersion{}, false
	}
	if len(detail.Versions) == 0 {
		writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
		return catalog.FileVersion{}, false
	}
	if fileID == 0 {
		fileID = int64(h.playingFile(r.Context(), session, contentID))
	}
	if fileID == 0 {
		return detail.Versions[0], true
	}
	for _, version := range detail.Versions {
		if int64(version.FileID) == fileID {
			return version, true
		}
	}
	writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
	return catalog.FileVersion{}, false
}

// playingFile is the file this token most recently selected for contentID,
// either during negotiation or playback, or zero when no source is selected.
func (h *PlaybackHandler) playingFile(ctx context.Context, session *Session, contentID string) int {
	lister, ok := h.playbackStore.(interface {
		ListActiveForToken(context.Context, string) ([]PlaybackSession, error)
	})
	if !ok {
		return 0
	}
	plays, err := lister.ListActiveForToken(ctx, session.Token)
	if err != nil {
		return 0
	}
	fileID := 0
	var latest PlaybackSession
	for _, play := range plays {
		if play.ItemID != contentID || (fileID != 0 && !play.UpdatedAt.After(latest.UpdatedAt)) {
			continue
		}
		selected := play.UpstreamMediaFileID
		// A source-specific PlaybackInfo response already identifies the file
		// before the first stream request creates an upstream session.
		if selected == 0 && len(play.MediaSources) == 1 {
			selected = play.MediaSources[0].FileID
		}
		// Existing negotiations written before this field was introduced can
		// still resolve on their owning process.
		if selected == 0 && play.UpstreamSessionID != "" && h.sessionMgr != nil {
			native, err := h.sessionMgr.GetSession(play.UpstreamSessionID)
			if err == nil && native != nil && native.UserID == session.StreamAppUserID && native.ProfileID == session.ProfileID {
				selected = native.MediaFileID
			}
		}
		if selected != 0 {
			fileID, latest = selected, play
		}
	}
	return fileID
}
