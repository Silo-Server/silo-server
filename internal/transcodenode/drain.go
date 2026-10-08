package transcodenode

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/mediasample"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

const (
	drainStartSegment     = "start"
	drainTranscodeSegment = "transcode"
)

func (s *Server) drainActiveJobs() int {
	s.gpu.mu.Lock()
	busy := s.gpu.workers
	if s.gpu.reprobing {
		busy++
	}
	s.gpu.mu.Unlock()
	return int(s.activeJobs.Load()) + busy + playback.HWProbesInFlight() + playback.SubtitleFillsInFlight() + playback.CopySeekAnchorsInFlight() + tonemap.ProbesInFlight() + tonemap.PreflightsInFlight() + mediasample.CapabilitiesInFlight()
}

func (s *Server) drainMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if s.drain == nil || path == "/admin/drain" || path == "/api/v1/health" || path == "/metrics" || path == "/status" || path == "/admin/reload-config" || path == "/admin/force-reload" || r.Method == http.MethodDelete || strings.HasSuffix(path, "/progress") || strings.HasSuffix(path, "/downloaded") {
			next.ServeHTTP(w, r)
			return
		}
		// All media and execution paths on this listener require the node bearer.
		// Do not retain retirement reservations from unauthenticated requests.
		if s.watcher == nil || s.watcher.Config() == nil || r.Header.Get("Authorization") != "Bearer "+s.watcher.Config().Auth.JWTSecret {
			next.ServeHTTP(w, r)
			return
		}
		key := ""
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 2 && (parts[0] == drainTranscodeSegment || parts[0] == "remux") && parts[1] != drainStartSegment {
			key = parts[1]
		}
		if path == "/transcode/start" {
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var input struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(body, &input) == nil {
				key = input.SessionID
			}
		}
		end, err := s.drain.Begin(r.Context(), key, time.Now().Add(playback.MaxTokenTTL))
		if err != nil {
			http.Error(w, "worker retiring or drain authority unavailable", http.StatusServiceUnavailable)
			return
		}
		defer end()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	status, err := s.drain.Observe(r.Context())
	if err != nil {
		http.Error(w, "worker drain authority unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method == http.MethodPost && !status.Fenced {
		http.Error(w, "durable drain fence not requested", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(status)
}
