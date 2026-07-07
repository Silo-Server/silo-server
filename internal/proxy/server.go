package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"

	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// Server is the HTTP handler for proxy mode.
type Server struct {
	watcher    *nodeconfig.Watcher
	tracker    *nodesessions.Tracker
	httpClient *http.Client
	egress     *egressMeter
}

// NewServer creates a new proxy server backed by a config watcher and session
// tracker.
func NewServer(watcher *nodeconfig.Watcher, tracker *nodesessions.Tracker) *Server {
	return &Server{
		watcher: watcher,
		tracker: tracker,
		// No overall timeout — stream bodies are long-lived. Hung nodes are
		// bounded by the transport's response-header timeout instead.
		httpClient: &http.Client{Transport: newStreamTransport()},
		egress:     newEgressMeter(),
	}
}

// newStreamTransport tunes the proxy→transcode-node connection pool. Many
// concurrent viewers fan their segment fetches through one proxy→node pair,
// and Go's default of 2 idle connections per host causes constant connection
// churn (and TLS re-handshakes) under load. The response-header timeout
// bounds requests to a hung node; the longest legitimate server-side wait is
// the 30s manifest-readiness poll on the transcode node.
func newStreamTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 128
	t.MaxIdleConnsPerHost = 32
	t.ResponseHeaderTimeout = 60 * time.Second
	return t
}

// Handler returns the chi.Router with all proxy routes mounted.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	// hls.js uses XHR for manifest/segment fetches which are subject to
	// CORS when the proxy runs on a different origin than the web app.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "HEAD", "OPTIONS"},
		AllowedHeaders: []string{"Accept", "Authorization", "Content-Type", "Range"},
		MaxAge:         86400,
	}))
	r.Get("/api/v1/health", s.handleHealth)
	r.Group(func(r chi.Router) {
		// Everything under /stream counts toward the node's measured
		// egress bandwidth.
		r.Use(s.meterEgress)
		r.Head("/stream/direct/{token}", s.handleDirectPlay)
		r.Get("/stream/direct/{token}", s.handleDirectPlay)
		r.Head("/stream/remux/{token}", s.handleRemux)
		r.Get("/stream/remux/{token}", s.handleRemux)
		r.Head("/stream/transcode/{token}/master.m3u8", s.handleTranscodeManifest)
		r.Get("/stream/transcode/{token}/master.m3u8", s.handleTranscodeManifest)
		r.Get("/stream/transcode/{token}/segment/{name}", s.handleTranscodeSegment)
		r.Get("/stream/subtitles/{token}/{track}/fonts", s.handleSubtitleFonts)
		r.Get("/stream/subtitles/{token}/{track}", s.handleSubtitle)
	})

	// Admin routes — bearer-auth protected.
	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Get("/status", s.handleStatus)
	})
	return r
}

type healthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int    `json:"active_jobs"`
	EgressKbps int    `json:"egress_kbps"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	activeJobs := 0
	if s.tracker != nil {
		activeJobs = s.tracker.ActiveCount()
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(healthResponse{
		Status:     "ok",
		ActiveJobs: activeJobs,
		EgressKbps: s.egress.RateKbps(),
	})
}

// requireBearer checks Authorization: Bearer {secret} for admin endpoints.
func (s *Server) requireBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.watcher.Config()
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != cfg.Auth.JWTSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// verifyToken extracts and validates the stream token from the URL.
func (s *Server) verifyToken(w http.ResponseWriter, r *http.Request) *streamtoken.Claims {
	cfg := s.watcher.Config()
	tokenStr := chi.URLParam(r, "token")
	claims, err := streamtoken.Verify(tokenStr, cfg.Auth.JWTSecret)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil
	}
	return claims
}

func (s *Server) handleDirectPlay(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}

	info := sessionInfo(s.tracker, claims, "direct_play")
	s.tracker.Track(r.Context(), info)
	defer s.tracker.Remove(r.Context(), claims.SessionID)

	http.ServeFile(w, r, claims.MediaPath)
}

func (s *Server) handleRemux(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}

	info := sessionInfo(s.tracker, claims, "remux")
	s.tracker.Track(r.Context(), info)
	defer s.tracker.Remove(r.Context(), claims.SessionID)

	seekSeconds := 0.0
	if seekStr := r.URL.Query().Get("seek"); seekStr != "" {
		if v, err := strconv.ParseFloat(seekStr, 64); err == nil {
			seekSeconds = v
		}
	}
	_ = playback.ServeRemux(w, r, claims.MediaPath, "mp4", seekSeconds, claims.TranscodeAudio, claims.AudioTrackIndex, claims.DVProfile)
}

func (s *Server) handleTranscodeManifest(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	s.touchTranscodeSession(r, claims)
	s.proxyToTranscodeNode(w, r, claims, "/transcode/"+claims.SessionID+"/master.m3u8")
}

func (s *Server) handleTranscodeSegment(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	s.touchTranscodeSession(r, claims)
	name := chi.URLParam(r, "name")
	s.proxyToTranscodeNode(w, r, claims, "/transcode/"+claims.SessionID+"/segment/"+name)
}

// touchTranscodeSession keeps HLS sessions visible in the active stream count.
// Unlike direct play and remux, transcode playback reaches the proxy as many
// short manifest/segment requests, so the session is tracked by recent
// activity instead of request lifetime.
func (s *Server) touchTranscodeSession(r *http.Request, claims *streamtoken.Claims) {
	s.tracker.Touch(r.Context(), sessionInfo(s.tracker, claims, "transcode"))
}

// sessionInfo builds the node-session tracker record for a verified token,
// copying the numeric ownership keys the node-session tracker needs.
func sessionInfo(tr *nodesessions.Tracker, claims *streamtoken.Claims, kind string) nodesessions.SessionInfo {
	return nodesessions.SessionInfo{
		SessionID:   claims.SessionID,
		NodeURL:     tr.NodeURL(),
		NodeName:    tr.NodeName(),
		Type:        kind,
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
		AuthUserID:  claims.UserID,
		ProfileID:   claims.ProfileID,
		MediaFileID: claims.MediaFileID,
	}
}

func (s *Server) handleSubtitle(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	cfg := s.watcher.Config()
	trackParam := chi.URLParam(r, "track")
	trackIndex, requestedFormat, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		http.Error(w, "invalid subtitle index", http.StatusBadRequest)
		return
	}

	// When the URL requests SUP format (e.g. /subtitles/{token}/2.sup),
	// stream the PGS track as a raw .sup elementary stream for client-side
	// bitmap rendering (libpgs). Unlike the buffered text paths below, this
	// streams ffmpeg output directly: the track can be large and the client
	// renders progressively as data arrives.
	if requestedFormat == "sup" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		err := playback.StreamExtractSubtitle(r.Context(), playback.StreamExtractOpts{
			InputPath:   claims.MediaPath,
			TrackIndex:  trackIndex,
			SourceCodec: "hdmv_pgs_subtitle", // .sup URLs are only generated for PGS tracks
			FFmpegPath:  cfg.Playback.FFmpegPath,
			Writer:      w,
		})
		if err != nil && r.Context().Err() == nil {
			// Headers already committed — log and let the client see a
			// truncated response.
			slog.Error("stream subtitle (sup)", "error", err, "track", trackIndex,
				"path", claims.MediaPath, "playback_session_id", claims.SessionID)
		}
		return
	}

	// When the URL requests ASS format (e.g. /subtitles/{token}/2.ass),
	// extract as raw ASS to preserve styling for client-side rendering.
	if requestedFormat == "ass" {
		data, err := playback.ExtractSubtitleWithFormat(r.Context(), claims.MediaPath, trackIndex, "ass", cfg.Playback.FFmpegPath)
		if err != nil {
			slog.Error("extract subtitle (ass)", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
			http.Error(w, "subtitle extraction failed", http.StatusInternalServerError)
			return
		}
		playback.ServeSubtitle(w, data, "ass")
		return
	}

	data, format, err := playback.ExtractSubtitle(r.Context(), claims.MediaPath, trackIndex, cfg.Playback.FFmpegPath)
	if err != nil {
		slog.Error("extract subtitle", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle extraction failed", http.StatusInternalServerError)
		return
	}

	vtt, err := playback.ConvertToVTT(data, format)
	if err != nil {
		slog.Error("convert to vtt", "error", err, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle conversion failed", http.StatusInternalServerError)
		return
	}

	playback.ServeSubtitle(w, vtt, "vtt")
}

func (s *Server) handleSubtitleFonts(w http.ResponseWriter, r *http.Request) {
	claims := s.verifyToken(w, r)
	if claims == nil {
		return
	}
	cfg := s.watcher.Config()
	trackParam := chi.URLParam(r, "track")
	trackIndex, _, err := playback.ParseSubtitleTrackParam(trackParam)
	if err != nil {
		http.Error(w, "invalid subtitle index", http.StatusBadRequest)
		return
	}

	fonts, err := playback.ExtractAttachedSubtitleFonts(r.Context(), claims.MediaPath, cfg.Playback.FFmpegPath)
	if err != nil {
		slog.Error("extract subtitle fonts", "error", err, "track", trackIndex, "path", claims.MediaPath, "playback_session_id", claims.SessionID)
		http.Error(w, "subtitle font extraction failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(playback.EncodeSubtitleFontBundle(fonts)); err != nil {
		slog.Warn("subtitle font response encode failed", "error", err, "playback_session_id", claims.SessionID)
	}
}

// proxyToTranscodeNode forwards the request to the transcode node specified in the claims.
func (s *Server) proxyToTranscodeNode(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims, path string) {
	cfg := s.watcher.Config()
	if claims.TranscodeNode == "" {
		http.Error(w, "no transcode node in token", http.StatusBadRequest)
		return
	}

	targetURL := claims.TranscodeNode + path
	if rawQuery := r.URL.RawQuery; rawQuery != "" {
		targetURL = fmt.Sprintf("%s?%s", targetURL, rawQuery)
	}

	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, nil)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Auth.JWTSecret)
	// Forward the verified stream token so the transcode node can self-reconstruct
	// a lost session after its OWN restart: the token carries the full byte-affecting
	// recipe, so the node can re-spawn ffmpeg seeked to the requested segment instead
	// of 404ing (the integrated server already does this from the same token). The
	// node re-verifies the token independently before trusting it.
	if token := chi.URLParam(r, "token"); token != "" {
		req.Header.Set("X-Silo-Stream-Token", token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		slog.Error("proxy to transcode node", "error", err, "url", targetURL, "playback_session_id", claims.SessionID)
		http.Error(w, "transcode node unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) handleForceReload(w http.ResponseWriter, r *http.Request) {
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("proxy force reload completed")
	w.WriteHeader(http.StatusNoContent)
}

type statusResponse struct {
	ActiveSessions int `json:"active_sessions"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statusResponse{
		ActiveSessions: s.tracker.ActiveCount(),
	})
}
