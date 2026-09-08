package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Playback control socket, v2 handshake.
//
// The bridge socket (HandleSessionWebSocket) upgrades any authenticated
// account that owns the session and lets a later registration replace the
// lane unconditionally. The v2 handshake is a documented plain-WebSocket path
// admitted by a single-use ticket that binds the original login session,
// profile proof, playback session and installation at mint time and again at
// upgrade:
//
//   - a caller that is not the session's account and profile is refused (403);
//   - the session must exist in this replica's manager (404 otherwise);
//   - a reconnect resumes the lane only for the same owner and installation;
//     a different installation is refused while the lane is held (409);
//   - ack and result frames are routed only while the registration that
//     received them still owns the lane.
//
// The realtime hub (registration foundation) stays the single delivery lane;
// this handler adds admission and ownership around it and changes no frame.

// PlaybackControlSocketProtocol is the selected subprotocol; the credential
// travels as the second offered subprotocol, silo.ticket.<ticket>.
const PlaybackControlSocketProtocol = "silo.playback-control.v2"

const (
	playbackControlTicketTTL      = 30 * time.Second
	playbackControlMaxLifetime    = 4 * time.Hour
	playbackControlTicketRedisKey = "silo:playback:v2:control-ticket:"
	playbackControlCheckInterval  = 15 * time.Second
	playbackControlTicketBytes    = 32
	playbackControlTicketLength   = 43
	playbackControlMemoryTickets  = 10000
)

// Errors the control-socket seam reports; each transport renders its own shape.
var (
	ErrPlaybackControlSocketUnavailable   = errors.New("playback control socket unavailable")
	ErrPlaybackControlSocketNotOwner      = errors.New("playback session belongs to another account or profile")
	ErrPlaybackControlSocketInstallation  = errors.New("playback installation does not match the session")
	ErrPlaybackControlSocketStale         = errors.New("playback control authority is stale")
	ErrPlaybackControlSocketLaneHeld      = errors.New("playback control lane is held by another installation")
	ErrPlaybackControlSocketInvalidTicket = evt.ErrSocketTicket
)

// PlaybackControlBinding is the identity a ticket captures: the playback
// session and, for a session started through v2, the installation the client
// presented. A bridge-started session carries no installation.
type PlaybackControlBinding struct {
	PlaybackSessionID string `json:"playback_session_id"`
	InstallationID    string `json:"installation_id"`
}

// PlaybackControlTicket is the single-use handshake credential payload.
type PlaybackControlTicket struct {
	Identity evt.SocketIdentity     `json:"identity"`
	Binding  PlaybackControlBinding `json:"binding"`
}

// PlaybackControlTicketStore mints and atomically consumes control tickets.
// With Redis the credential is shared across API nodes; without it the ticket
// is process-local and the client must reconnect to the minting node.
type PlaybackControlTicketStore struct {
	redis   *redis.Client
	mu      sync.Mutex
	tickets map[string]PlaybackControlTicket
}

func NewPlaybackControlTicketStore(client *redis.Client) *PlaybackControlTicketStore {
	return &PlaybackControlTicketStore{redis: client, tickets: map[string]PlaybackControlTicket{}}
}

func (s *PlaybackControlTicketStore) Mint(ctx context.Context, ticket PlaybackControlTicket) (string, error) {
	now := time.Now()
	identity := ticket.Identity
	if identity.AccessFingerprint == "" || identity.UserID <= 0 || identity.SessionID == "" || identity.ProfileID == "" || !identity.AccessExpiresAt.After(now) || ticket.Binding.PlaybackSessionID == "" {
		return "", evt.ErrSocketTicket
	}
	ticket.Identity.TicketExpiresAt = now.Add(playbackControlTicketTTL)
	if identity.AccessExpiresAt.Before(ticket.Identity.TicketExpiresAt) {
		ticket.Identity.TicketExpiresAt = identity.AccessExpiresAt
	}
	raw := make([]byte, playbackControlTicketBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	if s.redis != nil {
		payload, err := json.Marshal(ticket)
		if err != nil {
			return "", err
		}
		ok, err := s.redis.SetArgs(ctx, playbackControlTicketRedisKey+value, payload, redis.SetArgs{Mode: "NX", TTL: time.Until(ticket.Identity.TicketExpiresAt)}).Result()
		if err != nil {
			return "", err
		}
		if ok != "OK" {
			return "", evt.ErrSocketTicket
		}
		return value, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, v := range s.tickets {
		if !v.Identity.TicketExpiresAt.After(now) {
			delete(s.tickets, key)
		}
	}
	if len(s.tickets) >= playbackControlMemoryTickets {
		return "", evt.ErrSocketTicket
	}
	s.tickets[value] = ticket
	return value, nil
}

// Consume burns the credential atomically. Redis failures never fall back.
func (s *PlaybackControlTicketStore) Consume(ctx context.Context, value string) (PlaybackControlTicket, error) {
	var ticket PlaybackControlTicket
	if len(value) != playbackControlTicketLength {
		return ticket, evt.ErrSocketTicket
	}
	if _, err := base64.RawURLEncoding.DecodeString(value); err != nil {
		return ticket, evt.ErrSocketTicket
	}
	if s.redis != nil {
		data, err := s.redis.GetDel(ctx, playbackControlTicketRedisKey+value).Bytes()
		if err != nil {
			return ticket, evt.ErrSocketTicket
		}
		if err := json.Unmarshal(data, &ticket); err != nil {
			return PlaybackControlTicket{}, evt.ErrSocketTicket
		}
	} else {
		s.mu.Lock()
		ticket = s.tickets[value]
		delete(s.tickets, value)
		s.mu.Unlock()
	}
	now := time.Now()
	id := ticket.Identity
	if id.AccessFingerprint == "" || id.UserID <= 0 || id.SessionID == "" || !id.TicketExpiresAt.After(now) || !id.AccessExpiresAt.After(now) || ticket.Binding.PlaybackSessionID == "" {
		return PlaybackControlTicket{}, evt.ErrSocketTicket
	}
	return ticket, nil
}

type playbackControlLane struct {
	registration   *playback.RealtimeRegistration
	userID         int
	profileID      string
	installationID string
	// close ends the connection that held the lane once a same-owner
	// reconnect takes it over, so its frames stop at the takeover instead
	// of at its next read.
	close func()
}

// PlaybackControlSocketV2 serves the v2 control handshake for one PlaybackHandler.
type PlaybackControlSocketV2 struct {
	Playback *PlaybackHandler
	Tickets  *PlaybackControlTicketStore
	Validate EventsSocketValidator
	// PublicOrigin is the configured external origin, never a forwarded header.
	PublicOrigin  string
	checkInterval time.Duration

	laneMu sync.Mutex
	lanes  map[string]*playbackControlLane
}

func NewPlaybackControlSocketV2(playbackHandler *PlaybackHandler, client *redis.Client, sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker, publicURL string) *PlaybackControlSocketV2 {
	h := &PlaybackControlSocketV2{Playback: playbackHandler, Tickets: NewPlaybackControlTicketStore(client), PublicOrigin: publicURL, lanes: map[string]*playbackControlLane{}}
	h.Validate = newSocketAuthorityValidator(sessions, users, resolver, primary)
	return h
}

// Available reports whether the handshake can be served from this process.
func (h *PlaybackControlSocketV2) Available() bool {
	return h != nil && h.Playback != nil && h.Playback.RealtimeHub != nil && h.Playback.sessionMgr != nil && h.Tickets != nil && h.Validate != nil
}

// Mint validates the caller's login authority and ownership of the playback
// session and issues one single-use credential bound to both.
func (h *PlaybackControlSocketV2) Mint(ctx context.Context, identity evt.SocketIdentity, playbackSessionID, installationID string) (string, time.Time, error) {
	if !h.Available() {
		return "", time.Time{}, ErrPlaybackControlSocketUnavailable
	}
	validated, claims, err := h.Validate(ctx, identity)
	if err != nil {
		return "", time.Time{}, err
	}
	scope, ok := access.GetScope(validated)
	if !ok {
		return "", time.Time{}, evt.ErrSocketTicket
	}
	identity.AccessFingerprint = eventsScopeFingerprint(scope)
	identity.EffectiveRole = claims.Role
	binding, err := h.admit(ctx, identity, playbackSessionID, installationID, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	ticket, err := h.Tickets.Mint(ctx, PlaybackControlTicket{Identity: identity, Binding: binding})
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := time.Now().Add(playbackControlTicketTTL)
	if identity.AccessExpiresAt.Before(expiry) {
		expiry = identity.AccessExpiresAt
	}
	return ticket, expiry, nil
}

// admit checks that the session exists here and belongs to the caller's
// account and profile, and that the presented installation matches the
// server's. When expected is non-nil (upgrade), the current binding must
// equal the one the ticket captured.
func (h *PlaybackControlSocketV2) admit(_ context.Context, identity evt.SocketIdentity, playbackSessionID, installationID string, expected *PlaybackControlBinding) (PlaybackControlBinding, error) {
	session, err := h.Playback.sessionMgr.GetSession(playbackSessionID)
	if err != nil {
		return PlaybackControlBinding{}, err
	}
	if session == nil || session.UserID != identity.UserID || session.ProfileID == "" || session.ProfileID != identity.ProfileID {
		return PlaybackControlBinding{}, ErrPlaybackControlSocketNotOwner
	}
	// A claimed installation must be this server's; a bridge-started client
	// presents none.
	if installationID != "" && installationID != h.Playback.InstallationID {
		return PlaybackControlBinding{}, ErrPlaybackControlSocketInstallation
	}
	binding := PlaybackControlBinding{PlaybackSessionID: session.ID, InstallationID: installationID}
	if expected != nil && *expected != binding {
		return PlaybackControlBinding{}, ErrPlaybackControlSocketStale
	}
	h.laneMu.Lock()
	lane := h.lanes[session.ID]
	h.laneMu.Unlock()
	if lane != nil && (lane.userID != identity.UserID || lane.profileID != identity.ProfileID || lane.installationID != binding.InstallationID) {
		return PlaybackControlBinding{}, ErrPlaybackControlSocketLaneHeld
	}
	return binding, nil
}

// ServeHTTP performs the documented handshake for GET .../control/ws.
func (h *PlaybackControlSocketV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.Available() {
		http.Error(w, "playback control unavailable", http.StatusServiceUnavailable)
		return
	}
	// Proof travels only in the handshake protocols, never a URL or cookie.
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.Query().Has("token") || r.URL.Query().Has("ticket") {
		http.Error(w, "invalid handshake", http.StatusBadRequest)
		return
	}
	if !socketOriginAllowed(r, h.PublicOrigin) {
		http.Error(w, "origin refused", http.StatusForbidden)
		return
	}
	protocols := websocket.Subprotocols(r)
	if len(protocols) != 2 || protocols[0] != PlaybackControlSocketProtocol || !strings.HasPrefix(protocols[1], eventsTicketProtocolPrefix) {
		http.Error(w, "required subprotocol missing", http.StatusBadRequest)
		return
	}
	// Reject malformed upgrade requests before burning the credential.
	if !websocket.IsWebSocketUpgrade(r) || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	key, keyErr := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if keyErr != nil || len(key) != 16 {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	ticket, err := h.Tickets.Consume(r.Context(), strings.TrimPrefix(protocols[1], eventsTicketProtocolPrefix))
	if err != nil {
		http.Error(w, "invalid realtime credential", http.StatusUnauthorized)
		return
	}
	sessionID := ticket.Binding.PlaybackSessionID
	if routed := playbackSessionRouteParam(r); routed != "" && routed != sessionID {
		http.Error(w, "credential is bound to another playback session", http.StatusForbidden)
		return
	}
	validated, claims, err := h.Validate(r.Context(), ticket.Identity)
	if err != nil {
		http.Error(w, "realtime authority expired", http.StatusUnauthorized)
		return
	}
	expected := ticket.Binding
	if _, err := h.admit(validated, ticket.Identity, sessionID, ticket.Binding.InstallationID, &expected); err != nil {
		switch {
		case errors.Is(err, playback.ErrSessionNotFound):
			http.Error(w, "playback session not found", http.StatusNotFound)
		case errors.Is(err, ErrPlaybackControlSocketNotOwner):
			http.Error(w, "forbidden", http.StatusForbidden)
		case errors.Is(err, ErrPlaybackControlSocketStale), errors.Is(err, ErrPlaybackControlSocketInstallation), errors.Is(err, ErrPlaybackControlSocketLaneHeld):
			http.Error(w, "playback control authority is stale", http.StatusConflict)
		default:
			http.Error(w, "playback control unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	setPlaybackSessionLogContext(r, sessionID)

	deadline := time.Now().Add(playbackControlMaxLifetime)
	if ticket.Identity.AccessExpiresAt.Before(deadline) {
		deadline = ticket.Identity.AccessExpiresAt
	}
	ctx, cancel := context.WithDeadline(validated, deadline)
	defer cancel()

	upgrader := websocket.Upgrader{Subprotocols: []string{PlaybackControlSocketProtocol}, CheckOrigin: func(r *http.Request) bool { return socketOriginAllowed(r, h.PublicOrigin) }}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.ErrorContext(r.Context(), "playback control websocket upgrade failed", "component", "api", "error", err, "session", sessionID, "playback_session_id", sessionID)
		return
	}
	realtimeConn := &sessionRealtimeConn{conn: conn}
	registration := h.Playback.RealtimeHub.Register(sessionID, realtimeConn)
	if registration == nil {
		_ = conn.Close()
		return
	}
	lane := &playbackControlLane{registration: registration, userID: claims.UserID, profileID: ticket.Identity.ProfileID, installationID: expected.InstallationID, close: func() { _ = conn.Close() }}
	h.laneMu.Lock()
	previous := h.lanes[sessionID]
	h.lanes[sessionID] = lane
	h.laneMu.Unlock()
	if previous != nil && previous.close != nil {
		previous.close()
	}
	defer func() {
		h.laneMu.Lock()
		if h.lanes[sessionID] == lane {
			delete(h.lanes, sessionID)
		}
		h.laneMu.Unlock()
		if h.Playback.RealtimeHub.Unregister(registration) && h.Playback.setRealtimeConnectionState(sessionID, false) {
			h.Playback.syncSessionsNow(context.Background(), "realtime_disconnect")
		}
		_ = conn.Close()
	}()

	configureWebSocket(conn)
	readCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()
	startWebSocketPingLoop(readCtx, realtimeConn.WritePing)
	// Re-check login authority and session ownership without extending the
	// deadline; loss of either closes an otherwise healthy socket.
	go func() {
		interval := h.checkInterval
		if interval <= 0 {
			interval = playbackControlCheckInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-readCtx.Done():
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(readCtx, 2*time.Second)
				_, _, err := h.Validate(checkCtx, ticket.Identity)
				if err == nil {
					_, err = h.admit(checkCtx, ticket.Identity, sessionID, expected.InstallationID, &expected)
				}
				stop()
				if err != nil {
					cancelRead()
					_ = conn.Close()
					return
				}
			}
		}
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if !h.owns(sessionID, lane) {
			// A newer registration for the same owner took the lane; frames
			// from this connection no longer belong to the session.
			return
		}
		if err := h.Playback.handleRealtimeClientMessage(sessionID, data); err != nil {
			slog.WarnContext(r.Context(), "invalid realtime client message", "component", "api", "session", sessionID, "playback_session_id", sessionID, "error", err)
		}
	}
}

func (h *PlaybackControlSocketV2) owns(sessionID string, lane *playbackControlLane) bool {
	h.laneMu.Lock()
	defer h.laneMu.Unlock()
	return h.lanes[sessionID] == lane
}

// playbackSessionRouteParam reads the routed session id when the raw route
// carries one; the credential's binding remains the authority.
func playbackSessionRouteParam(r *http.Request) string {
	return chi.URLParam(r, "session_id")
}
