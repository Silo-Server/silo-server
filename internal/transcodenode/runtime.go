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

type stopInputScopeKey struct{}
type stopInputScope struct {
	work   string
	inputs map[string]bool
}

// WithStopInputScope binds an authenticated destructive command to the inputs
// of its observed generations. Empty scopes cannot stop an unknown producer.
func WithStopInputScope(ctx context.Context, work string, inputs []string) context.Context {
	scope := stopInputScope{work: work, inputs: make(map[string]bool, len(inputs))}
	for _, input := range inputs {
		if input != "" {
			scope.inputs[input] = true
		}
	}
	return context.WithValue(ctx, stopInputScopeKey{}, scope)
}

// The caller holds the session lifecycle lock, shared with producer publication.
func (s *Server) stopInputScopeMatches(ctx context.Context, work string) bool {
	scope, scoped := ctx.Value(stopInputScopeKey{}).(stopInputScope)
	if !scoped {
		return true
	}
	if scope.work != work || len(scope.inputs) == 0 {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if session := s.sessions[work]; session != nil && !scope.inputs[session.Opts().InputPath] {
		return false
	}
	if progressive, ok := s.progressiveRemuxes[work]; ok && !scope.inputs[progressive.inputPath] {
		return false
	}
	return true
}

type artifactDeleteScopeKey struct{}
type artifactDeleteScope struct {
	work    string
	allowed func() (bool, error)
}

// WithArtifactDeleteScope binds a verified core cutoff to a local ownership
// check. The handler invokes it under the same lifecycle lock as publication;
// it must not perform network operations or wait for another lifecycle lock.
func WithArtifactDeleteScope(ctx context.Context, work string, allowed func() (bool, error)) context.Context {
	return context.WithValue(ctx, artifactDeleteScopeKey{}, artifactDeleteScope{work: work, allowed: allowed})
}

func artifactDeleteAllowed(ctx context.Context, work string) (bool, error) {
	scope, scoped := ctx.Value(artifactDeleteScopeKey{}).(artifactDeleteScope)
	if !scoped {
		return true, nil
	}
	if scope.work != work || scope.allowed == nil {
		return false, errors.New("artifact delete scope unavailable")
	}
	return scope.allowed()
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

// StopWorkInputs cancels the exact revoked producer immediately. Directory and
// accounting teardown then wait for lifecycle ownership, so a replacement can
// neither inherit a directory being deleted nor lose its own job or files.
func (s *Server) StopWorkInputs(ctx context.Context, sessionID string, inputs []string) {
	matches := func(path string) bool {
		for _, input := range inputs {
			if input == path {
				return true
			}
		}
		return false
	}
	s.mu.RLock()
	session := s.sessions[sessionID]
	if session == nil || !matches(session.Opts().InputPath) {
		session = nil
	}
	progressive, progressiveFound := s.progressiveRemuxes[sessionID]
	progressiveFound = progressiveFound && matches(progressive.inputPath)
	s.mu.RUnlock()
	if session != nil {
		session.CancelProcess()
	}
	if progressiveFound {
		progressive.cancel()
	}
	unlock := s.lockSessionLifecycle(sessionID)
	defer unlock()
	s.mu.Lock()
	if session != nil && s.sessions[sessionID] == session && matches(session.Opts().InputPath) {
		delete(s.sessions, sessionID)
		delete(s.lastAccess, sessionID)
	} else {
		session = nil
	}
	if current, ok := s.progressiveRemuxes[sessionID]; progressiveFound && ok && current.id == progressive.id {
		delete(s.progressiveRemuxes, sessionID)
	}
	s.mu.Unlock()
	if session != nil {
		_ = s.closeSessionOffGPU(session)
		s.tracker.Remove(ctx, sessionID)
	}
}
