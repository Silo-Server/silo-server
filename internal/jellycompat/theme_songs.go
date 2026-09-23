package jellycompat

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themesongs"
	"github.com/go-chi/chi/v5"
)

const (
	compatThemeAudioLower = "audio"
	compatThemeStream     = "stream"
	compatThemeAudio      = "Audio"
	compatThemeUniversal  = "universal"
	compatThemeM4A        = "m4a"
	compatThemeSeason     = "season"
)

type themeSongStore interface {
	themesongs.Store
	Find(context.Context, string, catalog.AccessFilter) (themesongs.File, error)
}

func (h *ItemsHandler) themeSongsResult(w http.ResponseWriter, r *http.Request) (themeMediaResultDTO, bool) {
	empty := themeMediaResultDTO{Items: []baseItemDTO{}, OwnerID: chi.URLParam(r, "id")}
	id, ok := h.validateThemeOwner(w, r)
	if !ok {
		return empty, false
	}
	if h.themeSongs == nil {
		return empty, true
	}
	session := SessionFromContext(r.Context())
	if userID := firstNonEmpty(chi.URLParam(r, "userId"), newCaseInsensitiveQuery(r.URL.Query()).Get("userId")); userID != "" && !validatePseudoUser(w, userID, session) {
		return empty, false
	}
	query := newCaseInsensitiveQuery(r.URL.Query())
	inherit := false
	var err error
	if value := query.Get("inheritFromParent"); value != "" {
		inherit, err = strconv.ParseBool(value)
		if err != nil {
			writeError(w, 400, "InvalidRequest", "Invalid inheritFromParent")
			return empty, false
		}
	}
	owner, files, err := h.themeSongs.Resolve(r.Context(), id, inherit, h.resolveAccessFilter(r.Context(), session))
	if err != nil {
		writeThemeLookupError(w, err)
		return empty, false
	}
	if strings.EqualFold(query.Get("sortBy"), "Random") {
		rand.Shuffle(len(files), func(i, j int) { files[i], files[j] = files[j], files[i] })
	}
	if owner != id {
		kind := EncodedIDItem
		if len(files) > 0 && files[0].OwnerType == compatThemeSeason {
			kind = EncodedIDSeason
		}
		empty.OwnerID = h.codec.EncodeStringID(kind, owner)
	}
	for _, file := range files {
		empty.Items = append(empty.Items, h.themeSongItem(file))
	}
	empty.TotalRecordCount = len(empty.Items)
	return empty, true
}

func (h *ItemsHandler) themeSongItem(file themesongs.File) baseItemDTO {
	n, _ := themesongs.NumericID(file.ID)
	themeID := EncodeNumericID(EncodedIDThemeSong, uint64(n)).String()
	return baseItemDTO{ServerID: h.mapper.serverID, ID: themeID, Name: file.Title, Type: compatThemeAudio, MediaType: compatThemeAudio, Container: file.Container, RunTimeTicks: int64(file.DurationSeconds) * 10000000,
		MediaSources: []mediaSourceDTO{{ID: themeID, Name: file.Title, Type: compatSubtitleDefault, Container: file.Container, RunTimeTicks: int64(file.DurationSeconds) * 10000000, SupportsDirectPlay: true, SupportsDirectStream: false, SupportsTranscoding: false}}}
}

func (h *ItemsHandler) handleThemeItem(w http.ResponseWriter, r *http.Request, session *Session, themeID int64) {
	if userID := newCaseInsensitiveQuery(r.URL.Query()).Get("userId"); userID != "" && !validatePseudoUser(w, userID, session) {
		return
	}
	if h.themeSongs == nil {
		writeError(w, http.StatusServiceUnavailable, "Unavailable", "Theme audio unavailable")
		return
	}
	file, err := h.themeSongs.Find(r.Context(), strconv.FormatInt(themeID, 10), h.resolveAccessFilter(r.Context(), session))
	if err != nil {
		writeThemeLookupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.themeSongItem(file))
}

func (h *ItemsHandler) HandleThemeSongs(w http.ResponseWriter, r *http.Request) {
	result, ok := h.themeSongsResult(w, r)
	if ok {
		writeJSON(w, http.StatusOK, result)
	}
}

func (h *ItemsHandler) HandleThemeAudio(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session == nil {
		writeError(w, 401, "Unauthorized", "Missing authentication token")
		return
	}
	if h.themeSongs == nil {
		writeError(w, 503, "Unavailable", "Theme audio unavailable")
		return
	}
	id, err := DecodeID(chi.URLParam(r, "itemId"))
	if err != nil || id.Type != EncodedIDThemeSong {
		writeError(w, 404, "NotFound", "Theme not found")
		return
	}
	file, err := h.themeSongs.Find(r.Context(), strconv.FormatUint(id.Value, 10), h.resolveAccessFilter(r.Context(), session))
	if err != nil {
		writeThemeLookupError(w, err)
		return
	}
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(session.StreamAppUserID), ProfileID: session.ProfileID, PlayMethod: string(playback.PlayDirect)})
	query := newCaseInsensitiveQuery(r.URL.Query())
	universal := strings.EqualFold(path.Base(r.URL.Path), compatThemeUniversal)
	if !themeDirectPlayAllowed(query, chi.URLParam(r, "container"), file, universal) {
		writeError(w, 400, "PlaybackUnavailable", "Only original theme audio is supported")
		return
	}

	f, err := themesongs.Open(file)
	if err != nil {
		writeError(w, 503, "PlaybackUnavailable", "Theme audio is unavailable on this node")
		return
	}
	defer func() { _ = f.Close() }()
	themesongs.Serve(w, r, file, f)
}

func writeThemeLookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, themesongs.ErrNotFound) || errors.Is(err, catalog.ErrItemNotFound) {
		writeError(w, http.StatusNotFound, "NotFound", "Theme not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "InternalServerError", "Theme lookup failed")
}

func containsThemeContainer(list, container string) bool {
	for _, value := range strings.Split(list, ",") {
		if strings.TrimSpace(value) == container {
			return true
		}
	}
	return false
}

func themeContainerFormat(container string) string {
	if container == compatThemeM4A || container == "m4b" {
		return compatContainerMP4
	}
	return container
}

// Universal requests describe accepted formats and fallback transcode options.
// A fallback hint alone does not require conversion when the original fits.
func themeDirectPlayAllowed(query caseInsensitiveQuery, routeContainer string, file themesongs.File, universal bool) bool {
	for _, container := range []string{routeContainer, query.Get("container")} {
		if container != "" {
			accepted := false
			for _, value := range strings.Split(strings.ToLower(container), ",") {
				format, codecs, qualified := strings.Cut(strings.TrimSpace(value), "|")
				codecAccepted := !qualified || containsThemeContainer(strings.ReplaceAll(codecs, "|", ","), file.AudioCodec)
				accepted = accepted || themeContainerFormat(format) == themeContainerFormat(file.Container) && codecAccepted
			}
			if !accepted {
				return false
			}
		}
	}
	// Selecting a particular audio stream requires demuxing. -1 means default.
	if stream := query.Get("audioStreamIndex"); stream != "" && stream != "-1" {
		return false
	}
	if strings.EqualFold(query.Get("static"), "false") || strings.EqualFold(query.Get("enableDirectPlay"), "false") {
		return false
	}
	// Universal AudioCodec selects the fallback encoder. Its Container list
	// separately declares direct-play formats, including container|codec entries.
	fallbackCodec := universal && query.Get("container") != ""
	if codec := query.Get("audioCodec"); codec != "" && !fallbackCodec && !containsThemeContainer(strings.ToLower(codec), file.AudioCodec) {
		return false
	}
	for _, limit := range []struct {
		key    string
		actual int
		exact  bool
	}{
		{"audioBitRate", file.BitrateKbps * 1000, false}, {"maxAudioBitRate", file.BitrateKbps * 1000, false},
		{"maxStreamingBitrate", file.BitrateKbps * 1000, false}, {"audioChannels", file.AudioChannels, true},
		{"maxAudioChannels", file.AudioChannels, false}, {"audioSampleRate", file.SampleRate, true},
		{"maxAudioSampleRate", file.SampleRate, false},
	} {
		if universal && limit.key == "audioBitRate" {
			// The universal route uses this only for its fallback encoder.
			continue
		}
		value := query.Get(limit.key)
		if value == "" {
			continue
		}
		if limit.actual <= 0 && limit.key == "maxStreamingBitrate" {
			// Jellyfin's StreamBuilder assumes 40 Mbps when bitrate is unknown.
			limit.actual = 40_000_000
		}
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return false
		}
		if universal && limit.actual <= 0 && (limit.key == "maxAudioChannels" || limit.key == "maxAudioSampleRate") {
			// These universal device-profile constraints are not required when
			// the source metadata is unknown.
			continue
		}
		if limit.actual <= 0 || (limit.exact && n != limit.actual) || (!limit.exact && n < limit.actual) {
			return false
		}
	}
	if start := query.Get("startTimeTicks"); start != "" && start != "0" {
		return false
	}
	return true
}
