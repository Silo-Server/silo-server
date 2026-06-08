package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/nodeconfig"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// TranscodeStartRequest is the JSON body for POST /transcode/start.
type TranscodeStartRequest struct {
	SessionID          string  `json:"session_id"`
	InputPath          string  `json:"input_path"`
	SourceVideoCodec   string  `json:"source_video_codec"`
	SeekSeconds        float64 `json:"seek_seconds"`
	StartSegmentNumber int     `json:"start_segment_number"`
	TargetResolution   string  `json:"target_resolution"`
	TargetCodecVideo   string  `json:"target_codec_video"`
	TargetCodecAudio   string  `json:"target_codec_audio"`
	TargetBitrateKbps  int     `json:"target_bitrate_kbps"`
	SegmentDuration    int     `json:"segment_duration"`
	HWAccel            string  `json:"hw_accel"`
	AudioTrackIndex    int     `json:"audio_track_index"`
	SubtitleTrackIndex int     `json:"subtitle_track_index"`
	SubtitleBurnIn     bool    `json:"subtitle_burn_in"`
	TotalDuration      float64 `json:"total_duration"`
}

// TranscodeStartResponse is the JSON response for POST /transcode/start.
type TranscodeStartResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	HWAccel   string `json:"hw_accel,omitempty"`
}

// HealthResponse is the JSON response for GET /api/v1/health.
type HealthResponse struct {
	Status     string `json:"status"`
	ActiveJobs int32  `json:"active_jobs"`
}

// Server is the HTTP handler for transcode mode.
type Server struct {
	watcher    *nodeconfig.Watcher
	tracker    *nodesessions.Tracker
	ffmpegSink playback.FFmpegLogSink
	sessions   map[string]*playback.TranscodeSession
	mu         sync.RWMutex
	activeJobs atomic.Int32
}

// NewServer creates a new transcode server.
func NewServer(watcher *nodeconfig.Watcher, tracker *nodesessions.Tracker) *Server {
	s := &Server{
		watcher:  watcher,
		tracker:  tracker,
		sessions: make(map[string]*playback.TranscodeSession),
	}
	if cfg := watcher.Config(); cfg != nil {
		if cleaned, err := playback.CleanupOrphanedTranscodeDirs(cfg.Playback.TranscodeDir, nil); err != nil {
			slog.Warn("transcode node cleanup failed", "dir", cfg.Playback.TranscodeDir, "error", err)
		} else if cleaned > 0 {
			slog.Info("transcode node cleanup removed orphaned dirs", "dir", cfg.Playback.TranscodeDir, "count", cleaned)
		}
	}
	return s
}

func (s *Server) SetFFmpegLogSink(sink playback.FFmpegLogSink) {
	s.ffmpegSink = sink
}

// Handler returns the chi.Router with all transcode routes.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/api/v1/health", s.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(s.requireBearer)
		r.Get("/hw-capabilities", s.handleHWCapabilities)
		r.Post("/chapter-thumbnails/extract", s.handleChapterThumbnailExtract)
		r.Post("/transcode/start", s.handleStart)
		r.Delete("/transcode/{session_id}", s.handleStop)
		r.Get("/transcode/{session_id}/master.m3u8", s.handleManifest)
		r.Get("/transcode/{session_id}/segment/{name}", s.handleSegment)
		r.Post("/admin/force-reload", s.handleForceReload)
		r.Get("/status", s.handleStatus)
	})
	return r
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{
		Status:     "ok",
		ActiveJobs: s.activeJobs.Load(),
	})
}

func (s *Server) handleHWCapabilities(w http.ResponseWriter, _ *http.Request) {
	ffmpegPath := ""
	if cfg := s.watcher.Config(); cfg != nil {
		ffmpegPath = cfg.Playback.FFmpegPath
	}
	info := playback.DetectHWAccelWithFFmpeg(ffmpegPath)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (s *Server) handleChapterThumbnailExtract(w http.ResponseWriter, r *http.Request) {
	var req chapterthumbs.RemoteExtractRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeChapterThumbnailError(w, http.StatusBadRequest, "invalid_request", "invalid request body")
		return
	}
	if strings.TrimSpace(req.InputPath) == "" {
		writeChapterThumbnailError(w, http.StatusBadRequest, "invalid_request", "input_path is required")
		return
	}

	cfg := s.watcher.Config()
	frame, reason, err := chapterthumbs.ExtractFrame(r.Context(), chapterthumbs.FrameExtractOptions{
		InputPath:   req.InputPath,
		SeekSeconds: req.SeekSeconds,
		FFmpegPath:  cfg.Playback.FFmpegPath,
		HWAccel:     cfg.Playback.HWAccel,
		HWDevice:    cfg.Playback.HWDevice,
		ToneMap:     req.ToneMap,
	})
	if err != nil {
		writeChapterThumbnailError(w, http.StatusUnprocessableEntity, reason, err.Error())
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

func writeChapterThumbnailError(w http.ResponseWriter, status int, reason string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(chapterthumbs.RemoteExtractErrorResponse{
		Reason: reason,
		Error:  message,
	})
}

// requireBearer is middleware that checks for Authorization: Bearer {secret}.
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

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	var req TranscodeStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.SessionID == "" || req.InputPath == "" {
		http.Error(w, "session_id and input_path are required", http.StatusBadRequest)
		return
	}

	cfg := s.watcher.Config()
	outputDir := filepath.Join(cfg.Playback.TranscodeDir, req.SessionID)

	opts := playback.TranscodeOpts{
		InputPath:          req.InputPath,
		OutputDir:          outputDir,
		SessionID:          req.SessionID,
		SourceVideoCodec:   req.SourceVideoCodec,
		SeekSeconds:        req.SeekSeconds,
		StartSegmentNumber: req.StartSegmentNumber,
		TargetResolution:   req.TargetResolution,
		TargetCodecVideo:   req.TargetCodecVideo,
		TargetCodecAudio:   req.TargetCodecAudio,
		TargetBitrateKbps:  req.TargetBitrateKbps,
		SegmentDuration:    req.SegmentDuration,
		FFmpegPath:         cfg.Playback.FFmpegPath,
		HWAccel:            req.HWAccel,
		HWDevice:           "",
		AudioTrackIndex:    req.AudioTrackIndex,
		SubtitleTrackIndex: req.SubtitleTrackIndex,
		SubtitleBurnIn:     req.SubtitleBurnIn,
		TotalDuration:      req.TotalDuration,
		FastStart:          true,
		NodeType:           "transcode",
		ExecutionMode:      "transcode_node",
		FFmpegLogSink:      s.ffmpegSink,
	}

	if opts.HWAccel == "" && cfg.Playback.HWAccel != "" {
		opts.HWAccel = cfg.Playback.HWAccel
	}

	// Defensively close any existing session for this ID so that a quality
	// switch doesn't orphan the old ffmpeg process or leave stale segments.
	s.mu.Lock()
	if old, ok := s.sessions[req.SessionID]; ok {
		delete(s.sessions, req.SessionID)
		s.mu.Unlock()
		s.activeJobs.Add(-1)
		_ = old.Close()
		os.RemoveAll(outputDir)
	} else {
		s.mu.Unlock()
	}

	session, err := playback.StartTranscode(context.WithoutCancel(r.Context()), opts)
	if err != nil {
		slog.Error("start transcode", "error", err, "session", req.SessionID, "playback_session_id", req.SessionID)
		http.Error(w, "failed to start transcode", http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.sessions[req.SessionID] = session
	s.mu.Unlock()
	s.activeJobs.Add(1)

	// Track session in Redis
	effectiveHWAccel := session.Opts().HWAccel
	s.tracker.Track(r.Context(), nodesessions.SessionInfo{
		SessionID:  req.SessionID,
		NodeURL:    s.tracker.NodeURL(),
		NodeName:   s.tracker.NodeName(),
		Type:       "transcode",
		CodecVideo: req.TargetCodecVideo,
		CodecAudio: req.TargetCodecAudio,
		Resolution: req.TargetResolution,
		HWAccel:    effectiveHWAccel,
		StartedAt:  time.Now().UTC().Format(time.RFC3339),
	})

	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(TranscodeStartResponse{
		SessionID: req.SessionID,
		Status:    "started",
		HWAccel:   effectiveHWAccel,
	})
}

func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")

	s.mu.Lock()
	session, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	s.activeJobs.Add(-1)

	if err := session.Close(); err != nil {
		slog.Error("close transcode session", "error", err, "session", sessionID, "playback_session_id", sessionID)
	}

	cfg := s.watcher.Config()
	outputDir := filepath.Join(cfg.Playback.TranscodeDir, sessionID)
	os.RemoveAll(outputDir)

	s.tracker.Remove(r.Context(), sessionID)

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")

	s.mu.RLock()
	session, ok := s.sessions[sessionID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	manifest, err := session.BuildPlaybackManifest("segment/", r.URL.RawQuery)
	if err != nil {
		slog.Error("get manifest", "error", err, "session", sessionID, "playback_session_id", sessionID)
		http.Error(w, "manifest not ready", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Write(manifest)
}

func (s *Server) handleSegment(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "session_id")
	name := chi.URLParam(r, "name")

	s.mu.RLock()
	session, ok := s.sessions[sessionID]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	segPath, err := session.GetSegment(name)
	if err != nil && err == playback.ErrSegmentNotFound {
		segNum, parseErr := playback.ParseSegmentNumber(name)
		if parseErr == nil {
			now := time.Now()
			decision := session.SegmentRecoveryDecision(segNum, now)
			lastProducedAgeMS := int64(-1)
			if !decision.Progress.LastProducedAt.IsZero() {
				lastProducedAgeMS = now.Sub(decision.Progress.LastProducedAt).Milliseconds()
			}
			slog.Info("transcode segment missing",
				"segment", name,
				"requested_segment", segNum,
				"produced_head", decision.Progress.ProducedHead,
				"last_requested_segment", decision.Progress.LastRequestedSegment,
				"start_segment_number", decision.Progress.StartSegmentNumber,
				"last_produced_age_ms", lastProducedAgeMS,
				"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
				"reason", decision.Reason,
				"session", sessionID,
				"playback_session_id", sessionID,
			)
			if decision.Wait {
				slog.Info("transcode segment wait",
					"segment", name,
					"requested_segment", segNum,
					"produced_head", decision.Progress.ProducedHead,
					"last_requested_segment", decision.Progress.LastRequestedSegment,
					"start_segment_number", decision.Progress.StartSegmentNumber,
					"last_produced_age_ms", lastProducedAgeMS,
					"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
					"reason", decision.Reason,
					"session", sessionID,
					"playback_session_id", sessionID,
				)
				segPath, err = session.WaitForSegment(name, decision.WaitTimeout)
				if err != nil && err == playback.ErrSegmentNotFound {
					slog.Info("transcode segment wait timeout",
						"segment", name,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"session", sessionID,
						"playback_session_id", sessionID,
					)
				}
			}

			if err != nil && err == playback.ErrSegmentNotFound && decision.RestartOnTimeout {
				seekSeconds, ok, seekErr := session.RestartSeekTarget(segNum)
				if seekErr != nil && !errors.Is(seekErr, playback.ErrManifestNotReady) {
					slog.Error("resolve transcode node seek target", "error", seekErr, "segment", name, "session", sessionID, "playback_session_id", sessionID)
				}

				if ok {
					slog.Info("transcode node seek restart",
						"segment", name,
						"requested_segment", segNum,
						"produced_head", decision.Progress.ProducedHead,
						"last_requested_segment", decision.Progress.LastRequestedSegment,
						"start_segment_number", decision.Progress.StartSegmentNumber,
						"last_produced_age_ms", lastProducedAgeMS,
						"wait_timeout_ms", decision.WaitTimeout.Milliseconds(),
						"reason", decision.Reason,
						"seek_seconds", seekSeconds,
						"session", sessionID,
						"playback_session_id", sessionID,
					)

					if restartErr := session.Restart(
						context.WithoutCancel(r.Context()),
						seekSeconds,
						segNum,
					); restartErr == nil {
						segPath, err = session.WaitForSegment(name, 30*time.Second)
					}
				}
				if !ok && session.IsCopyVideo() {
					err = playback.ErrSegmentNotFound
				}
			}
		} else if session.IsRunning() {
			// Non-numbered segment (e.g., init.mp4 for fMP4 HLS).
			// Wait briefly — the init segment is written almost immediately.
			segPath, err = session.WaitForSegment(name, 10*time.Second)
		}
	}
	if err != nil {
		http.Error(w, "segment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	http.ServeFile(w, r, segPath)
}

func (s *Server) handleForceReload(w http.ResponseWriter, r *http.Request) {
	if err := s.watcher.ForceReload(r.Context()); err != nil {
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	cfg := s.watcher.Config()
	s.mu.Lock()
	for id, session := range s.sessions {
		session.Close()
		os.RemoveAll(filepath.Join(cfg.Playback.TranscodeDir, id))
		delete(s.sessions, id)
	}
	s.activeJobs.Store(0)
	s.mu.Unlock()

	s.tracker.Cleanup(r.Context())

	slog.Info("transcode force reload completed")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	sessionIDs := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		sessionIDs = append(sessionIDs, id)
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	type statusResponse struct {
		Status     string   `json:"status"`
		ActiveJobs int32    `json:"active_jobs"`
		Sessions   []string `json:"sessions"`
	}
	json.NewEncoder(w).Encode(statusResponse{
		Status:     "ok",
		ActiveJobs: s.activeJobs.Load(),
		Sessions:   sessionIDs,
	})
}
