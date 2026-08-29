package proxy

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtoken"
)

// The /stream/v3 family is the proxy side of authorized_media_origins_v1. A
// client that negotiated the mode receives absolute proxy URLs carrying no
// playback credential at all, and attaches the same Authorization header it
// sends the API. This node therefore has to answer two questions the token
// routes answered from the URL alone:
//
//   - what to serve: the API wrote the session's recipe to the shared grant
//     store when it planned the route, keyed by playback session id;
//   - who is asking: the bearer token is the user's own access token, so it is
//     validated against the live login session in Postgres on every request,
//     which keeps revocation immediate here as well as on the API.
//
// Both answers are required. A grant alone would let any authenticated user
// stream any session; a valid login alone says nothing about which session's
// bytes the caller is entitled to.

// proxyGrantLookup reads the recipe central authorized for a session. It fails
// closed: a miss is a 404, never a guess.
type proxyGrantLookup interface {
	Get(ctx context.Context, sessionID string) (*playback.RecipeCard, bool)
}

// loginSessionValidator reports whether a login session is still active
// (not revoked, not expired). Implemented by *auth.SessionRepository.
type loginSessionValidator interface {
	IsValid(ctx context.Context, sessionID string) (bool, error)
}

// grantErrorResponse mirrors the API's error body so a client sees one error
// shape whichever origin served it.
type grantErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeGrantError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(grantErrorResponse{Error: code, Message: message})
}

// authorizeGrant resolves a /stream/v3 request to the recipe it may serve.
//
// It deliberately accepts only a bearer access token in the Authorization
// header: no ?token= fallback (a query credential is exactly what this mode
// removes) and no sa_ API keys (a machine key is not a viewer, and scope
// enforcement lives on the API). The JWT is verified against the watcher's
// CURRENT secret, so a rotated secret invalidates in-flight streams here the
// same way it does on the token routes.
func (s *Server) authorizeGrant(w http.ResponseWriter, r *http.Request) (*playback.RecipeCard, bool) {
	if s.grants == nil || s.loginSessions == nil {
		writeGrantError(w, http.StatusServiceUnavailable, "service_unavailable", "This node cannot serve header-authenticated media")
		return nil, false
	}
	sessionID := chi.URLParam(r, "session_id")
	if sessionID == "" {
		writeGrantError(w, http.StatusBadRequest, "bad_request", "Session ID is required")
		return nil, false
	}

	cfg := s.watcher.Config()
	secret := ""
	if cfg != nil {
		secret = cfg.Auth.JWTSecret
	}
	token := grantBearerToken(r)
	if token == "" || secret == "" {
		writeGrantError(w, http.StatusUnauthorized, "unauthorized", "Missing or malformed authorization header")
		return nil, false
	}
	claims, err := auth.NewJWTService(secret, 0, 0).ValidateToken(token)
	if err != nil || claims.TokenType != auth.TokenTypeAccess {
		writeGrantError(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired token")
		return nil, false
	}
	valid, err := s.loginSessions.IsValid(r.Context(), claims.SessionID)
	if err != nil || !valid {
		if err != nil {
			slog.WarnContext(r.Context(), "login session check failed", "component", "proxy", "error", err, "playback_session_id", sessionID)
		}
		writeGrantError(w, http.StatusUnauthorized, "unauthorized", "Session is no longer valid")
		return nil, false
	}

	card, ok := s.grants.Get(r.Context(), sessionID)
	if !ok || card == nil {
		writeGrantError(w, http.StatusNotFound, "playback_session_not_found", "Playback session not found")
		return nil, false
	}
	if card.UserID != claims.UserID {
		writeGrantError(w, http.StatusForbidden, "forbidden", "Session belongs to another user")
		return nil, false
	}
	return card, true
}

// grantBearerToken extracts the access token from the Authorization header
// only. An sa_ API key is rejected outright rather than passed on to JWT
// validation, so the refusal is a deliberate policy rather than a parse error.
func grantBearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	token := strings.TrimSpace(parts[1])
	if strings.HasPrefix(token, "sa_") {
		return ""
	}
	return token
}

// handleGrantIdentity serves a direct-play or progressive-remux session. It
// runs the same serve paths as the token routes, from claims projected out of
// the grant, so seek handling, range/ETag behavior and session tracking are the
// ones the legacy routes already have.
func (s *Server) handleGrantIdentity(w http.ResponseWriter, r *http.Request) {
	card, ok := s.authorizeGrant(w, r)
	if !ok {
		return
	}
	claims := card.ToClaims()
	switch card.PlayMethod {
	case playback.PlayRemux:
		s.serveRemuxClaims(w, r, &claims)
	case playback.PlayTranscode:
		writeGrantError(w, http.StatusBadRequest, "bad_request", "Transcode streams use manifest/segment endpoints")
	default:
		s.serveDirectPlayClaims(w, r, &claims)
	}
}

func (s *Server) handleGrantTranscodeManifest(w http.ResponseWriter, r *http.Request) {
	card, ok := s.authorizeGrant(w, r)
	if !ok {
		return
	}
	claims := card.ToClaims()
	s.touchTranscodeSession(r, &claims)
	s.relayGrantToTranscodeNode(w, r, &claims, "/transcode/"+transcodeTransportIDFromClaims(&claims)+"/master.m3u8")
}

func (s *Server) handleGrantTranscodeSegment(w http.ResponseWriter, r *http.Request) {
	card, ok := s.authorizeGrant(w, r)
	if !ok {
		return
	}
	claims := card.ToClaims()
	s.touchTranscodeSession(r, &claims)
	s.relayGrantToTranscodeNode(w, r, &claims, "/transcode/"+transcodeTransportIDFromClaims(&claims)+"/segment/"+chi.URLParam(r, "name"))
}

// relayGrantToTranscodeNode forwards to the transcode node exactly as the token
// routes do, minting the node-facing stream token here from the grant.
//
// That token never reaches the client: it is the node's own reconstruction
// descriptor (it re-verifies it independently and can re-spawn ffmpeg from it
// after its own restart), so the credential the client was promised it would
// never see stays strictly on the proxy→node hop.
func (s *Server) relayGrantToTranscodeNode(w http.ResponseWriter, r *http.Request, claims *streamtoken.Claims, path string) {
	// The token transcode routes attach in their handlers; this is the only path
	// the two grant transcode handlers take, so attaching once here is the
	// equivalent hook. The proxy→node hop itself stays internal_relay.
	attachStream(r.Context(), claims)
	cfg := s.watcher.Config()
	forwardToken := ""
	if cfg != nil && cfg.Auth.JWTSecret != "" {
		token, err := streamtoken.Sign(*claims, cfg.Auth.JWTSecret, playback.MaxTokenTTL)
		if err != nil {
			slog.WarnContext(r.Context(), "sign node relay stream token failed", "component", "proxy", "error", err, "playback_session_id", claims.SessionID)
		} else {
			forwardToken = token
		}
	}
	s.proxyToTranscodeNode(w, r, claims, path, forwardToken)
}
