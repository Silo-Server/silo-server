package proxy

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

// themeAudioTrackerType labels theme transfers in node session reports so they
// are never mistaken for a video playback session.
const themeAudioTrackerType = "theme_audio"

type themeTokenShape uint8

const (
	themeTokenInvalid themeTokenShape = iota
	themeTokenDirect
	themeTokenConvertHere
	themeTokenRelay
)

// classifyThemeToken binds a verified token to one theme recipe. Video tokens,
// and theme tokens whose routing tuple does not match their method, are
// refused: a theme token is never authority for video, nor the reverse.
func classifyThemeToken(claims *streamtoken.Claims) themeTokenShape {
	if claims == nil || claims.ThemeID <= 0 || claims.MediaPath == "" ||
		claims.RoutingEgress != string(noderouting.EgressProxy) {
		return themeTokenInvalid
	}
	switch claims.PlayMethod {
	case streamtoken.PlayMethodThemeDirect:
		if claims.RoutingWorkload == string(noderouting.WorkloadDirectPlay) && claims.RoutingExecution == string(noderouting.ExecutionNone) {
			return themeTokenDirect
		}
	case streamtoken.PlayMethodThemeAAC:
		if claims.RoutingWorkload != string(noderouting.WorkloadRemux) || !claims.TranscodeAudio || !claims.AudioOnly || claims.TargetCodecAudio != themesongs.CodecAAC {
			return themeTokenInvalid
		}
		switch claims.RoutingExecution {
		case string(noderouting.ExecutionProxy):
			return themeTokenConvertHere
		case string(noderouting.ExecutionTranscode):
			if claims.TranscodeNode != "" && claims.TranscodeTransportID != "" {
				return themeTokenRelay
			}
		}
	}
	return themeTokenInvalid
}

// handleThemeAudio serves a detail-page theme routed through this proxy: the
// original file, a progressive AAC conversion run here, or one relayed from
// the transcode node the API reserved. Themes are transfers, not playback
// sessions, so they carry no playback session identity in telemetry.
func (s *Server) handleThemeAudio(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyPlaybackToken(w, r)
	if claims == nil {
		return
	}
	shape := classifyThemeToken(claims)
	if shape == themeTokenInvalid {
		writeProxyRouteStatusV3(w, http.StatusServiceUnavailable)
		return
	}
	attachTransfer(r.Context(), claims)
	if s.tracker != nil && r.Method != http.MethodHead {
		// A HEAD probe is not an active transfer and must not count against
		// this proxy's capacity, as for downloads.
		s.tracker.Track(r.Context(), sessionInfo(s.tracker, claims, themeAudioTrackerType))
		defer s.tracker.Remove(context.WithoutCancel(r.Context()), claims.SessionID)
	}
	modified := time.Unix(0, claims.ThemeModifiedUnixNano)
	switch shape {
	case themeTokenDirect:
		themesongs.ServeFile(w, r, strconv.FormatInt(claims.ThemeID, 10), claims.MediaPath, claims.ThemeSize, modified)
	case themeTokenRelay:
		// The transcode node re-approves the theme file and re-reads the stored
		// authority before it starts FFmpeg; the seek query is forwarded.
		s.proxyToTranscodeNode(w, r, claims, "/remux/"+claims.TranscodeTransportID, chi.URLParam(r, "token"))
	case themeTokenConvertHere:
		if !themesongs.Unchanged(claims.MediaPath, claims.ThemeSize, modified) {
			http.Error(w, "theme audio changed", http.StatusNotFound)
			return
		}
		seekSeconds := 0.0
		if raw := r.URL.Query().Get("seek"); raw != "" {
			parsed, err := strconv.ParseFloat(raw, 64)
			if err != nil || parsed < 0 {
				http.Error(w, "invalid seek", http.StatusBadRequest)
				return
			}
			seekSeconds = parsed
		}
		themesongs.ServeConverted(w, r, claims.MediaPath, themesongs.Conversion{
			Channels: claims.TargetAudioChannels, BitrateKbps: claims.TargetAudioBitrateKbps, SourceChannels: claims.SourceAudioChannels,
		}, seekSeconds, s.watcher.Config().Playback.FFmpegPath)
	}
}
