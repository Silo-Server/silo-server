package proxy

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

func (s *Server) drainMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if s.drain == nil || (!strings.HasPrefix(path, "/stream/") && !strings.HasPrefix(path, "/downloads/") && path != "/hw-capabilities" && path != "/admin/reprobe-capabilities") {
			next.ServeHTTP(w, r)
			return
		}
		key := ""
		expiry := time.Now().Add(playback.MaxTokenTTL)
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "stream" && parts[1] == "v3" {
			// Unknown/unauthenticated requests must not create retained permits.
			if s.watcher != nil && s.watcher.Config() != nil && s.grants != nil && s.loginSessions != nil {
				claims, err := auth.NewJWTService(s.watcher.Config().Auth.JWTSecret, 0, 0).ValidateToken(grantBearerToken(r))
				if err == nil && claims.TokenType == auth.TokenTypeAccess {
					valid, err := s.loginSessions.IsValid(r.Context(), claims.SessionID)
					card, found := s.grants.Get(r.Context(), parts[2])
					if err == nil && valid && found && card != nil && card.UserID == claims.UserID {
						key = parts[2]
					}
				}
			}
		} else {
			index := 2
			if len(parts) > 3 && parts[1] == kindRemux && parts[2] == "audio-v2" {
				index = 3
			}
			if len(parts) > index && s.watcher != nil && s.watcher.Config() != nil {
				claims, err := streamtoken.Verify(parts[index], s.watcher.Config().Auth.JWTSecret)
				if err == nil {
					key = claims.SessionID
					if claims.ExpiresAt != nil {
						expiry = claims.ExpiresAt.Time
					}
				}
			}
		}
		end, err := s.drain.Begin(r.Context(), key, expiry)
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
