package jellycompat

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
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
	compatThemeAudioRate  = "audioBitRate"
	compatThemeMaxRate    = "maxStreamingBitrate"
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
	query := newCaseInsensitiveQuery(r.URL.Query())
	universal := strings.EqualFold(path.Base(r.URL.Path), compatThemeUniversal)
	routeContainer := chi.URLParam(r, "container")
	delivery, method := themesongs.DeliveryOriginal, playback.PlayDirect
	var conversion themesongs.Conversion
	seekSeconds := 0.0
	if !themeDirectPlayAllowed(query, routeContainer, file, universal) {
		var reason string
		var ok bool
		conversion, seekSeconds, reason, ok = themeConversionAllowed(query, routeContainer, file, universal)
		if ok && h.themeRouter != nil && !h.themeRouter.CanConvert(r.Context()) {
			ok, reason = false, "Theme audio conversion is unavailable on this server"
		}
		if !ok {
			writeError(w, 400, "PlaybackUnavailable", reason)
			return
		}
		delivery, method = themesongs.DeliveryConverted, playback.PlayRemux
	}
	streamtelemetry.Attach(r.Context(), streamtelemetry.Attachment{Subject: streamtelemetry.UserSubject(session.StreamAppUserID), ProfileID: session.ProfileID, PlayMethod: string(method)})
	if h.themeRouter != nil {
		expires, _ := themesongs.Expiry(time.Now(), time.Time{})
		result, err := h.themeRouter.Resolve(r.Context(), themedelivery.Request{
			File: file, Delivery: delivery, Conversion: conversion, SeekSeconds: seekSeconds,
			UserID: session.StreamAppUserID, ProfileID: session.ProfileID,
			AccessPath: netaccess.PathFromContext(r.Context()), ExpiresAt: expires,
		})
		if err != nil {
			code := compatRoutingPolicyUnsatisfiedCode
			if errors.Is(err, themedelivery.ErrCapacityUnavailable) {
				code = compatRouteCapacityUnavailableCode
			}
			writeError(w, http.StatusServiceUnavailable, code, "No theme audio route satisfies the configured policy and current node availability")
			return
		}
		if !result.Local() {
			// Like compatibility video, a routed theme is a redirect to the proxy
			// the route reserved. A HEAD probe does not hold that capacity.
			http.Redirect(w, r, result.URL, http.StatusTemporaryRedirect)
			if r.Method == http.MethodHead {
				result.Release()
			}
			return
		}
	}

	f, err := themesongs.Open(file)
	if err != nil {
		writeError(w, 503, "PlaybackUnavailable", "Theme audio is unavailable on this node")
		return
	}
	defer func() { _ = f.Close() }()
	if delivery == themesongs.DeliveryConverted {
		ffmpeg := ""
		if h.themeFFmpegPath != nil {
			ffmpeg = h.themeFFmpegPath()
		}
		themesongs.ServeConverted(w, r, file.Path, conversion, seekSeconds, ffmpeg)
		return
	}
	themesongs.Serve(w, r, file, f)
}

// themeConversionAllowed decides whether a request the original cannot satisfy
// accepts the progressive AAC conversion, and with which output. Themes have
// no HLS transcode, and a static request asks for the original bytes only.
func themeConversionAllowed(query caseInsensitiveQuery, routeContainer string, file themesongs.File, universal bool) (themesongs.Conversion, float64, string, bool) {
	const unsupported = "Only original theme audio or its AAC conversion is supported"
	if strings.EqualFold(query.Get("static"), "true") {
		return themesongs.Conversion{}, 0, "Static theme streams serve original audio only", false
	}
	if stream := query.Get("audioStreamIndex"); stream != "" && stream != "-1" && stream != "0" {
		return themesongs.Conversion{}, 0, unsupported, false
	}
	containers := []string{routeContainer, query.Get("container")}
	if universal {
		if strings.EqualFold(query.Get("transcodingProtocol"), "hls") {
			return themesongs.Conversion{}, 0, "HLS theme transcoding is unsupported", false
		}
		// Universal Container lists direct-play formats; the conversion is
		// described by the transcoding parameters instead.
		containers = []string{query.Get("transcodingContainer")}
	}
	// The request must name an MP4 target itself: a client that did not ask for
	// a container could not expect the conversion's audio-only MP4.
	named := false
	for _, container := range containers {
		switch strings.ToLower(strings.TrimSpace(container)) {
		case "":
		case compatContainerMP4, compatThemeM4A:
			named = true
		default:
			return themesongs.Conversion{}, 0, unsupported, false
		}
	}
	if !named {
		return themesongs.Conversion{}, 0, unsupported, false
	}
	if codec := strings.ToLower(query.Get("audioCodec")); codec != "" && !containsThemeContainer(codec, themesongs.CodecAAC) {
		return themesongs.Conversion{}, 0, unsupported, false
	}
	target := file
	if query.Get("maxAudioChannels") == "1" || query.Get("transcodingAudioChannels") == "1" {
		target.AudioChannels = 1
	}
	conversion := themesongs.ConversionFor(target)
	if target.AudioChannels == 1 {
		conversion.SourceChannels = 0
	}
	for _, key := range []string{"maxAudioBitRate", compatThemeAudioRate, compatThemeMaxRate} {
		if value := query.Get(key); value != "" {
			bps, err := strconv.Atoi(value)
			if err != nil || bps <= 0 {
				return themesongs.Conversion{}, 0, unsupported, false
			}
			conversion.BitrateKbps = min(conversion.BitrateKbps, bps/1000)
		}
	}
	if conversion.BitrateKbps < 32 {
		return themesongs.Conversion{}, 0, "The requested bitrate is below the theme conversion minimum", false
	}
	seekSeconds := 0.0
	if raw := query.Get("startTimeTicks"); raw != "" && raw != "0" {
		ticks, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || ticks < 0 {
			return themesongs.Conversion{}, 0, unsupported, false
		}
		seekSeconds = float64(ticks) / 1e7
		if duration := float64(file.DurationSeconds); duration > 0 && seekSeconds > duration {
			seekSeconds = duration
		}
	}
	return conversion, seekSeconds, "", true
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
		{compatThemeAudioRate, file.BitrateKbps * 1000, false}, {"maxAudioBitRate", file.BitrateKbps * 1000, false},
		{compatThemeMaxRate, file.BitrateKbps * 1000, false}, {"audioChannels", file.AudioChannels, true},
		{"maxAudioChannels", file.AudioChannels, false}, {"audioSampleRate", file.SampleRate, true},
		{"maxAudioSampleRate", file.SampleRate, false},
	} {
		if universal && limit.key == compatThemeAudioRate {
			// The universal route uses this only for its fallback encoder.
			continue
		}
		value := query.Get(limit.key)
		if value == "" {
			continue
		}
		if limit.actual <= 0 && limit.key == compatThemeMaxRate {
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
