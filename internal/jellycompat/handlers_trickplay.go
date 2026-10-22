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
// players that read HLS image playlists. The item parameter is {id}, as on the
// other media routes, so a PlaySessionId grant can authorize a sheet.
const (
	compatTrickplaySheetRoute    = "/Videos/{id}/Trickplay/{width}/{index}.jpg"
	compatTrickplayPlaylistRoute = "/Videos/{id}/Trickplay/{width}/tiles.m3u8"
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
// in the form Jellyfin writes: one segment per sheet, each tiled EXT-X-TILES.
// Jellyfin puts the caller's token on each sheet URL; Silo puts the
// PlaySessionId of the caller's live play of that source instead, or nothing
// when there is none, so a logged playlist never carries the login token.
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
	sourceID := h.codec.EncodeIntID(EncodedIDMediaSource, int64(version.FileID))
	query := url.Values{compatMediaSourceIDQuery: {sourceID}}
	playSessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
	if grant := h.trickplayGrant(r.Context(), session, chiURLParam(r, "id"), sourceID, playSessionID); grant != "" {
		query.Set("PlaySessionId", grant)
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
// when it names a media source, else the one selected by the play the
// request's PlaySessionId names, else the one this token is playing for the
// item (Swiftfin and Findroid send no mediaSourceId), else the item's default
// version. A request a PlaySessionId authorized gets only a version that
// negotiation offered.
func (h *PlaybackHandler) trickplayVersion(w http.ResponseWriter, r *http.Request, session *Session) (catalog.FileVersion, bool) {
	if h.content == nil {
		writeError(w, http.StatusServiceUnavailable, "Unavailable", "Catalog unavailable")
		return catalog.FileVersion{}, false
	}
	contentID, fileID, err := decodeItemOrMediaSourceID(r.Context(), h.codec, chiURLParam(r, "id"))
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
	grant := playbackGrantFromContext(r.Context())
	if fileID == 0 {
		if play := h.requestedPlay(r, session, contentID, grant); play != nil {
			fileID = int64(negotiatedFile(play))
		} else {
			fileID = int64(h.playingFile(r.Context(), session, contentID))
		}
	}
	for _, version := range detail.Versions {
		if (fileID == 0 || int64(version.FileID) == fileID) && (grant == nil || negotiationOffersFile(grant, version.FileID)) {
			return version, true
		}
	}
	writeError(w, http.StatusNotFound, "NotFound", "Media source not found")
	return catalog.FileVersion{}, false
}

// activePlayLister lists a token's live negotiations.
type activePlayLister interface {
	ListActiveForToken(context.Context, string) ([]PlaybackSession, error)
}

// trickplayGrant is the PlaySessionId a sheet URL can carry in place of the
// caller's token: a live play of the caller that PlaybackSessionAuth accepts
// for a sheet of routeItemID (the item as the request path names it) and
// sourceID, preferring the one playSessionID names, else the most recent. It
// is empty when there is none or the plays could not be read; the sheets then
// need the caller's own credential.
func (h *PlaybackHandler) trickplayGrant(ctx context.Context, session *Session, routeItemID, sourceID, playSessionID string) string {
	lister, ok := h.playbackStore.(activePlayLister)
	if !ok {
		return ""
	}
	plays, err := lister.ListActiveForToken(ctx, session.Token)
	if err != nil {
		slog.WarnContext(ctx, "jellycompat: listing plays for a trickplay playlist failed", "component", "jellycompat", "error", err)
		return ""
	}
	var grant PlaybackSession
	for _, play := range plays {
		if !playbackGrantCovers(&play, routeItemID, sourceID) {
			continue
		}
		if play.ID == playSessionID {
			return play.ID
		}
		if grant.ID == "" || play.UpdatedAt.After(grant.UpdatedAt) {
			grant = play
		}
	}
	return grant.ID
}

// requestedPlay is the negotiation a trickplay request names: the one whose
// PlaySessionId authorized it, else this token's live play of contentID that
// the request's PlaySessionId names, else nil.
func (h *PlaybackHandler) requestedPlay(r *http.Request, session *Session, contentID string, grant *PlaybackSession) *PlaybackSession {
	if grant != nil {
		return grant
	}
	playSessionID := newCaseInsensitiveQuery(r.URL.Query()).Get("PlaySessionId")
	if playSessionID == "" || h.playbackStore == nil {
		return nil
	}
	play, ok := h.playbackStore.Get(playSessionID)
	if !ok || play.CompatToken != session.Token || play.ItemID != contentID {
		return nil
	}
	return play
}

// negotiatedFile is the file a negotiation selected, either during playback or
// by offering one source, or zero when it offered several and none was chosen.
func negotiatedFile(play *PlaybackSession) int {
	if play.UpstreamMediaFileID != 0 {
		return play.UpstreamMediaFileID
	}
	// A source-specific PlaybackInfo response already identifies the file
	// before the first stream request creates an upstream session.
	if len(play.MediaSources) == 1 {
		return play.MediaSources[0].FileID
	}
	return 0
}

// negotiationOffersFile reports whether play offered fileID as a source.
func negotiationOffersFile(play *PlaybackSession, fileID int) bool {
	for _, source := range play.MediaSources {
		if source.FileID == fileID {
			return true
		}
	}
	return false
}

// playingFile is the file this token most recently selected for contentID,
// either during negotiation or playback, or zero when no source is selected.
func (h *PlaybackHandler) playingFile(ctx context.Context, session *Session, contentID string) int {
	lister, ok := h.playbackStore.(activePlayLister)
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
		selected := negotiatedFile(&play)
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
