package transcodenode

import (
	"context"
	"errors"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// RuntimeConfig supplies execution settings without a database-backed watcher.
type RuntimeConfig interface {
	Config() *config.Config
	NodeRowID() (int, bool)
	NodeRegisteredURL() (string, bool)
	NodeDrainRequested() bool
	OnNodeDrainChange(func(bool))
	ForceReload(context.Context) error
}

type SessionTracker interface {
	Track(context.Context, nodesessions.SessionInfo)
	Remove(context.Context, string)
	Cleanup(context.Context)
	NodeURL() string
	NodeName() string
}

type RuntimeOptions struct {
	Config         RuntimeConfig
	Tracker        SessionTracker
	VerifyCommand  func(*http.Request) error
	VerifyToken    func(context.Context, string) (*streamtoken.Claims, error)
	Denied         func(context.Context, string) bool
	ExecuteHandler http.Handler
}

// NewRuntimeServer retains the established worker engines while requiring
// explicit command, output and execution authority without cluster credentials.
func NewRuntimeServer(options RuntimeOptions) (*Server, error) {
	if options.Config == nil || options.Config.Config() == nil || options.Tracker == nil || options.VerifyCommand == nil || options.VerifyToken == nil || options.Denied == nil {
		return nil, errors.New("worker runtime requires config, reporting and execution authority")
	}
	cfg := options.Config.Config()
	if cfg.Auth.JWTSecret != "" || cfg.Database.URL != "" || cfg.Redis.URL != "" {
		return nil, errors.New("worker runtime rejects cluster credentials")
	}
	s := newServer(options.Config, options.Tracker)
	s.runtime = options.Config
	s.verifyCommand = options.VerifyCommand
	s.verifyToken = options.VerifyToken
	s.executionDenied = options.Denied
	s.executeHandler = options.ExecuteHandler
	s.workerProtocol = "http_only_v1"
	return s, nil
}

func (s *Server) handleWorkerExecute(w http.ResponseWriter, r *http.Request) {
	if s.executeHandler == nil {
		http.Error(w, "standalone execution unavailable", http.StatusServiceUnavailable)
		return
	}
	s.executeHandler.ServeHTTP(w, r)
}

func (s *Server) runtimeConfig() RuntimeConfig {
	if s.runtime != nil {
		return s.runtime
	}
	if s.watcher == nil {
		return nil
	}
	return s.watcher
}

func (s *Server) verifyStreamToken(ctx context.Context, token string) (*streamtoken.Claims, error) {
	if s.verifyToken != nil {
		return s.verifyToken(ctx, token)
	}
	cfg := s.runtimeConfig().Config()
	if cfg == nil {
		return nil, errors.New("node not configured")
	}
	return streamtoken.Verify(token, cfg.Auth.JWTSecret)
}

// StopWork retires expired local work without revoking a newer core generation.
func (s *Server) StopWork(ctx context.Context, sessionID string) {
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.Lock()
	session := s.sessions[sessionID]
	delete(s.sessions, sessionID)
	delete(s.lastAccess, sessionID)
	delete(s.progressiveRemuxes, sessionID)
	s.mu.Unlock()
	if session != nil {
		_ = s.closeSessionOffGPU(session)
	}
	s.tracker.Remove(ctx, sessionID)
}

// StopWorkInputs closes only the producer whose transient input belonged to a
// revoked generation. It cannot wait behind a replacement's readiness check or
// retire a successor that published before the revocation callback ran.
func (s *Server) StopWorkInputs(ctx context.Context, sessionID string, inputs []string) {
	matches := func(path string) bool {
		for _, input := range inputs {
			if input == path {
				return true
			}
		}
		return false
	}
	s.mu.Lock()
	session := s.sessions[sessionID]
	if session == nil || !matches(session.Opts().InputPath) {
		session = nil
	} else {
		delete(s.sessions, sessionID)
		delete(s.lastAccess, sessionID)
	}
	if progressive, ok := s.progressiveRemuxes[sessionID]; ok && matches(progressive.inputPath) {
		progressive.cancel()
		delete(s.progressiveRemuxes, sessionID)
	}
	s.mu.Unlock()
	if session != nil {
		_ = s.closeSessionOffGPU(session)
		s.tracker.Remove(ctx, sessionID)
	}
}
