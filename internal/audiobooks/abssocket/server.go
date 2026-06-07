// Package abssocket exposes a Socket.io-compatible realtime endpoint for
// the official Audiobookshelf clients, mounted at /abs/socket.io/* on silo's
// main HTTP listener.
//
// Authentication mirrors real ABS exactly: a Socket.io connection opens
// unauthenticated, then the client emits an "auth" event whose payload is
// the access JWT minted by /abs/api/login. We validate the JWT against the
// signing secret supplied via SecretFn (same secret /abs/api/me and friends
// already use), then join the connection to a user-scoped Socket.io room.
// Events published via Publish(userID, ...) reach every client currently
// connected with that user's token on this process.
//
// Scope: single-process hub. A Redis adapter can be injected via
// Options.Adapter for multi-replica deployments; without it, fan-out is
// in-memory only.
package abssocket

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zishang520/socket.io/v2/socket"

	"github.com/Silo-Server/silo-server/internal/audiobooks/abs"
)

// Logger is the narrow logging surface this package needs.
type Logger interface {
	Debug(msg string, args ...any)
	Warn(msg string, args ...any)
}

type noopLogger struct{}

func (noopLogger) Debug(string, ...any) {}
func (noopLogger) Warn(string, ...any)  {}

// SecretFn returns the current ABS JWT signing secret. Called on every
// inbound "auth" event so an admin secret-rotate takes effect for new
// connections without a server restart.
type SecretFn func() []byte

// TokenValidator checks whether a JTI is still valid (non-revoked). It is
// optional — if nil, only JWT signature and expiry gate connections.
type TokenValidator func(ctx context.Context, jti string) (revoked bool, err error)

// Server is the Socket.io realtime server. Construct one per process and
// reuse across reconfigures.
type Server struct {
	io             *socket.Server
	secretFn       SecretFn
	tokenValidator TokenValidator
	logger         Logger

	mu        sync.Mutex
	connCount int // diagnostics only
}

// Options bundles optional knobs for New.
type Options struct {
	// Adapter swaps the in-memory Socket.io adapter for an external one,
	// e.g. a Redis adapter for multi-replica deployments. nil keeps the
	// built-in in-memory adapter.
	Adapter socket.AdapterConstructor
}

// New builds a Server. secretFn is required; tokenValidator and logger are
// optional (nil tokenValidator skips revocation check; nil logger is a no-op).
// opts may be nil for default single-replica behaviour.
func New(secretFn SecretFn, tokenValidator TokenValidator, logger Logger, opts *Options) *Server {
	if logger == nil {
		logger = noopLogger{}
	}
	srvOpts := socket.DefaultServerOptions()
	if opts != nil && opts.Adapter != nil {
		srvOpts.SetAdapter(opts.Adapter)
	}
	io := socket.NewServer(nil, srvOpts)
	s := &Server{
		io:             io,
		secretFn:       secretFn,
		tokenValidator: tokenValidator,
		logger:         logger,
	}
	io.On("connection", func(args ...any) {
		if len(args) == 0 {
			return
		}
		client, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		s.onConnection(client)
	})
	return s
}

func (s *Server) onConnection(client *socket.Socket) {
	s.mu.Lock()
	s.connCount++
	s.mu.Unlock()
	s.logger.Debug("abssocket: connection opened", "sid", client.Id())

	// ABS clients emit "auth" once with the access token as the payload.
	// Until that succeeds the socket sits in the unauthenticated default
	// namespace and receives no scoped events.
	//
	// Event names match the upstream ABS server (SocketAuthority.js):
	// successful auth fires "init" with a user-state payload; failed auth
	// fires "auth_failed" with {message}. The official ABS mobile + web
	// clients listen for these specific names.
	client.On("auth", func(args ...any) {
		token := pickToken(args)
		if token == "" {
			s.logger.Warn("abssocket: auth without token", "sid", client.Id())
			_ = client.Emit("auth_failed", map[string]any{"message": "missing token"})
			client.Disconnect(true)
			return
		}
		secret := s.secretFn()
		if len(secret) == 0 {
			s.logger.Warn("abssocket: server not ready (no jwt secret)", "sid", client.Id())
			_ = client.Emit("auth_failed", map[string]any{"message": "server not ready"})
			client.Disconnect(true)
			return
		}
		claims, err := abs.ParseToken(secret, token)
		if err != nil || claims.Type != "access" {
			s.logger.Warn("abssocket: auth rejected", "sid", client.Id(), "err", errString(err))
			_ = client.Emit("auth_failed", map[string]any{"message": "invalid token"})
			client.Disconnect(true)
			return
		}
		if s.tokenValidator != nil {
			revoked, err := s.tokenValidator(context.Background(), claims.JTI)
			if err != nil || revoked {
				s.logger.Warn("abssocket: token revoked", "sid", client.Id(), "jti", claims.JTI)
				_ = client.Emit("auth_failed", map[string]any{"message": "token revoked"})
				client.Disconnect(true)
				return
			}
		}
		// Bind the socket to a user-scoped room so Publish(userID, ...) can
		// fan a single in-process emit across every device on that account.
		client.Join(userRoom(claims.UserID))
		s.logger.Debug("abssocket: auth ok", "sid", client.Id(), "user_id", claims.UserID)
		_ = client.Emit("init", map[string]any{
			"userId":      claims.UserID,
			"connectedAt": time.Now().UnixMilli(),
		})
	})

	client.On("disconnect", func(...any) {
		s.mu.Lock()
		if s.connCount > 0 {
			s.connCount--
		}
		s.mu.Unlock()
		s.logger.Debug("abssocket: connection closed", "sid", client.Id())
	})
}

// Handler returns the http.Handler to mount at /socket.io/*.
func (s *Server) Handler() http.Handler {
	return s.io.ServeHandler(nil)
}

// Publish emits the given event to every socket currently joined to the
// user's room. Non-blocking; a publish to a userID with zero connected
// sockets is a no-op. Satisfies abs.EventPublisher.
func (s *Server) Publish(userID, event string, payload any) {
	if userID == "" {
		return
	}
	s.io.To(userRoom(userID)).Emit(event, payload)
}

// Broadcast emits to every authenticated socket regardless of user. Use
// for global events like library_item_added that aren't user-scoped.
// Satisfies abs.EventPublisher.
func (s *Server) Broadcast(event string, payload any) {
	s.io.Emit(event, payload)
}

// ConnectionCount returns the current connection count for diagnostics.
func (s *Server) ConnectionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connCount
}

// Close shuts the Socket.io server down. Idempotent.
func (s *Server) Close() {
	s.io.Close(nil)
}

func userRoom(userID string) socket.Room {
	return socket.Room("user:" + userID)
}

// pickToken extracts the bearer JWT from the variadic "auth" payload.
// Real ABS clients send a single string; our SPA may send {token: "..."}.
func pickToken(args []any) string {
	if len(args) == 0 {
		return ""
	}
	switch v := args[0].(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if t, ok := v["token"].(string); ok {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
