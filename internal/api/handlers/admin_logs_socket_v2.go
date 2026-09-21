package handlers

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/gorilla/websocket"
)

// Administrator log stream, v2 handshake.
//
// The bridge socket (HandleLogStreamWebSocket) authenticates the bearer token
// the web client puts in the URL and streams for as long as the connection
// lives. The v2 handshake keeps the same plain-WebSocket path shape, filters
// and frames, but admits the upgrade only with a single-use ticket offered as
// a subprotocol, requires an effective administrator, matches the Origin to
// the public origin, and bounds the connection's lifetime while rechecking the
// login session, account and profile authority every 15 seconds.

// AdminLogsSocketProtocol is the selected subprotocol; the credential travels
// as the second offered subprotocol, silo.ticket.<ticket>.
const AdminLogsSocketProtocol = "silo.admin-logs.v2"

// ErrAdminLogsSocketForbidden reports a login session whose effective role is
// not administrator (a secondary profile on an admin account included).
var ErrAdminLogsSocketForbidden = errors.New("administrator authority required for the log stream")

type AdminLogsSocketV2 struct {
	Logs     *AdminLogsHandler
	Tickets  *evt.SocketTicketStore
	Validate EventsSocketValidator
	// PublicOrigin is the configured external origin, never a forwarded header.
	PublicOrigin   string
	publicOrigin   atomic.Pointer[string]
	overlayOrigins atomic.Pointer[OverlayOriginSource]
	checkInterval  time.Duration
}

// NewAdminLogsSocketV2 reuses the events socket's ticket store shape and the
// current account, session and viewer authorities.
func NewAdminLogsSocketV2(logs *AdminLogsHandler, tickets *evt.SocketTicketStore, sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker, publicURL string) *AdminLogsSocketV2 {
	return &AdminLogsSocketV2{Logs: logs, Tickets: tickets, Validate: newSocketAuthorityValidator(sessions, users, resolver, primary), PublicOrigin: publicURL}
}

// Available reports whether the handshake is served on this process.
func (h *AdminLogsSocketV2) Available() bool {
	return h != nil && h.Logs != nil && h.Logs.streamHub != nil && h.Tickets != nil && h.Validate != nil
}

// Mint delegates the caller's current login session to one log-stream
// handshake. Only an effective administrator may mint.
func (h *AdminLogsSocketV2) Mint(ctx context.Context, identity evt.SocketIdentity) (string, error) {
	if !h.Available() {
		return "", apiError(http.StatusServiceUnavailable, "unavailable", "Log stream is unavailable")
	}
	validated, claims, err := h.Validate(ctx, identity)
	if err != nil {
		return "", err
	}
	if claims == nil || claims.Role != eventsAdminRole {
		return "", ErrAdminLogsSocketForbidden
	}
	scope, ok := access.GetScope(validated)
	if !ok {
		return "", evt.ErrSocketTicket
	}
	identity.AccessFingerprint = eventsScopeFingerprint(scope)
	identity.EffectiveRole = claims.Role
	return h.Tickets.Mint(ctx, identity)
}

// ServeHTTP is the documented plain-WebSocket handshake. Query parameters are
// the stream selection and filters of the bridge route; the proof travels only
// in the subprotocols.
func (h *AdminLogsSocketV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.Available() {
		http.Error(w, "log stream unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.Query().Has("token") || r.URL.Query().Has("ticket") {
		http.Error(w, "invalid handshake", http.StatusBadRequest)
		return
	}
	if !socketOriginAllowed(r, h.currentPublicOrigin(), overlayOriginsFrom(h.overlayOrigins.Load())) {
		http.Error(w, "origin refused", http.StatusForbidden)
		return
	}
	protocols := websocket.Subprotocols(r)
	if len(protocols) != 2 || protocols[0] != AdminLogsSocketProtocol || !strings.HasPrefix(protocols[1], eventsTicketProtocolPrefix) {
		http.Error(w, "required subprotocol missing", http.StatusBadRequest)
		return
	}
	// Reject malformed upgrade requests and invalid stream selections before
	// burning the credential.
	if !websocket.IsWebSocketUpgrade(r) || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	if key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key")); err != nil || len(key) != 16 {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	if _, _, _, err := h.Logs.parseStreamRequest(r); err != nil {
		http.Error(w, "invalid stream selection", http.StatusBadRequest)
		return
	}
	identity, err := h.Tickets.Consume(r.Context(), strings.TrimPrefix(protocols[1], eventsTicketProtocolPrefix))
	if err != nil {
		http.Error(w, "invalid log stream credential", http.StatusUnauthorized)
		return
	}
	validated, claims, err := h.Validate(r.Context(), identity)
	if err != nil {
		http.Error(w, "log stream authority expired", http.StatusUnauthorized)
		return
	}
	if claims.Role != eventsAdminRole {
		http.Error(w, "administrator authority required", http.StatusForbidden)
		return
	}
	deadline := time.Now().Add(evt.SocketMaxLifetime)
	if identity.AccessExpiresAt.Before(deadline) {
		deadline = identity.AccessExpiresAt
	}
	ctx, cancel := context.WithDeadline(validated, deadline)
	defer cancel()
	go func() {
		interval := h.checkInterval
		if interval <= 0 {
			interval = eventsSessionCheckInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
				_, current, err := h.Validate(checkCtx, identity)
				stop()
				if err != nil || current.Role != eventsAdminRole {
					cancel()
					return
				}
			}
		}
	}()
	h.Logs.serveLogStream(w, r.WithContext(ctx), websocket.Upgrader{Subprotocols: []string{AdminLogsSocketProtocol}, CheckOrigin: func(r *http.Request) bool {
		return socketOriginAllowed(r, h.currentPublicOrigin(), overlayOriginsFrom(h.overlayOrigins.Load()))
	}})
}

func (h *AdminLogsSocketV2) currentPublicOrigin() string {
	if origin := h.publicOrigin.Load(); origin != nil {
		return *origin
	}
	return h.PublicOrigin
}

func (h *AdminLogsSocketV2) SetPublicOrigin(origin string) {
	normalized := strings.TrimRight(origin, "/")
	h.publicOrigin.Store(&normalized)
}

// SetOverlayOrigins installs the source of overlay origins accepted next to
// the public origin.
func (h *AdminLogsSocketV2) SetOverlayOrigins(source OverlayOriginSource) {
	h.overlayOrigins.Store(&source)
}
