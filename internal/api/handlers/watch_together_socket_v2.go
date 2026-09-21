package handlers

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type roomSocketTickets interface {
	Mint(context.Context, watchtogether.RoomSocketCredential) (string, error)
	Consume(context.Context, string, string) (watchtogether.RoomSocketCredential, error)
}
type WatchTogetherSocketV2 struct {
	Room           *WatchTogetherHandler
	Tickets        roomSocketTickets
	Validate       EventsSocketValidator
	PublicOrigin   string
	publicOrigin   atomic.Pointer[string]
	overlayOrigins atomic.Pointer[OverlayOriginSource]
	checkInterval  time.Duration
}

func NewWatchTogetherSocketV2(room *WatchTogetherHandler, tickets *watchtogether.RoomSocketCredentialStore, sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker, publicURL string) *WatchTogetherSocketV2 {
	return &WatchTogetherSocketV2{Room: room, Tickets: tickets, Validate: newSocketAuthorityValidator(sessions, users, resolver, primary), PublicOrigin: publicURL}
}
func (h *WatchTogetherSocketV2) available() bool {
	return h != nil && h.Room != nil && h.Room.Service != nil && h.Room.TokenService != nil && h.Tickets != nil && h.Validate != nil
}
func (h *WatchTogetherSocketV2) MintRoomSocket(ctx context.Context, roomID, proof string, identity evt.SocketIdentity) (string, time.Time, error) {
	if !h.available() {
		return "", time.Time{}, apiError(503, "unavailable", "Room socket is unavailable")
	}
	claims, expiry, err := h.Room.TokenService.ValidateSocketProof(proof)
	if err != nil || claims.RoomID != roomID || claims.UserID != identity.UserID || claims.ProfileID != identity.ProfileID || identity.ProfileID == "" {
		return "", time.Time{}, apiError(403, "forbidden", "Room proof is required")
	}
	validated, effective, err := h.Validate(ctx, identity)
	if err != nil {
		return "", time.Time{}, apiError(403, "forbidden", "Current room authority is required")
	}
	scope, ok := access.GetScope(validated)
	if !ok {
		return "", time.Time{}, apiError(403, "forbidden", "Current room authority is required")
	}
	if _, err = h.Room.Service.GetRoom(ctx, roomID); err != nil {
		return "", time.Time{}, err
	}
	identity.AccessFingerprint = eventsScopeFingerprint(scope)
	identity.EffectiveRole = effective.Role
	credential := watchtogether.RoomSocketCredential{RoomID: roomID, Session: identity, RoomProofExpiresAt: expiry}
	until := time.Now().Add(watchtogether.RoomSocketTicketTTL)
	for _, limit := range []time.Time{expiry, identity.AccessExpiresAt} {
		if limit.Before(until) {
			until = limit
		}
	}
	ticket, err := h.Tickets.Mint(ctx, credential)
	if err != nil {
		return "", time.Time{}, apiError(503, "unavailable", "Room credential storage is unavailable")
	}

	return ticket, until, nil
}
func (h *WatchTogetherSocketV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !h.available() {
		http.Error(w, "room socket unavailable", http.StatusServiceUnavailable)
		return
	}
	roomID := chi.URLParam(r, "room_id")
	if roomID == "" || r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
		http.Error(w, "invalid handshake", http.StatusBadRequest)
		return
	}
	if !socketOriginAllowed(r, h.currentPublicOrigin(), overlayOriginsFrom(h.overlayOrigins.Load())) {
		http.Error(w, "origin refused", http.StatusForbidden)
		return
	}
	protocols := websocket.Subprotocols(r)
	if len(protocols) != 2 || protocols[0] != watchtogether.RoomSocketProtocol || !strings.HasPrefix(protocols[1], eventsTicketProtocolPrefix) {
		http.Error(w, "required subprotocol missing", http.StatusBadRequest)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	key, err := base64.StdEncoding.DecodeString(r.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		http.Error(w, "invalid upgrade", http.StatusBadRequest)
		return
	}
	credential, err := h.Tickets.Consume(r.Context(), strings.TrimPrefix(protocols[1], eventsTicketProtocolPrefix), roomID)
	if err != nil {
		http.Error(w, "invalid room credential", http.StatusUnauthorized)
		return
	}
	if credential.RoomID != roomID || credential.Session.ProfileID == "" || !credential.RoomProofExpiresAt.After(time.Now()) {
		http.Error(w, "invalid room credential", http.StatusUnauthorized)
		return
	}
	validated, claims, err := h.Validate(r.Context(), credential.Session)
	if err != nil {
		http.Error(w, "room authority expired", http.StatusUnauthorized)
		return
	}
	if _, err = h.Room.Service.GetRoom(validated, roomID); err != nil {
		http.Error(w, "room unavailable", http.StatusForbidden)
		return
	}
	deadline := time.Now().Add(watchtogether.RoomSocketMaxLifetime)
	for _, limit := range []time.Time{credential.Session.AccessExpiresAt, credential.RoomProofExpiresAt} {
		if limit.Before(deadline) {
			deadline = limit
		}
	}
	ctx, cancel := context.WithDeadline(validated, deadline)
	defer cancel()
	upgrader := websocket.Upgrader{Subprotocols: []string{watchtogether.RoomSocketProtocol}, CheckOrigin: func(r *http.Request) bool {
		return socketOriginAllowed(r, h.currentPublicOrigin(), overlayOriginsFrom(h.overlayOrigins.Load()))
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
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
				_, _, err := h.Validate(checkCtx, credential.Session)
				if err == nil {
					_, err = h.Room.Service.GetRoom(checkCtx, roomID)
				}
				stop()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	h.Room.serveRoomConnection(ctx, conn, roomID, claims.UserID, credential.Session.ProfileID)
}

func (h *WatchTogetherSocketV2) currentPublicOrigin() string {
	if origin := h.publicOrigin.Load(); origin != nil {
		return *origin
	}
	return h.PublicOrigin
}

func (h *WatchTogetherSocketV2) SetPublicOrigin(origin string) {
	normalized := strings.TrimRight(origin, "/")
	h.publicOrigin.Store(&normalized)
}

// SetOverlayOrigins installs the source of overlay origins accepted next to
// the public origin.
func (h *WatchTogetherSocketV2) SetOverlayOrigins(source OverlayOriginSource) {
	h.overlayOrigins.Store(&source)
}
