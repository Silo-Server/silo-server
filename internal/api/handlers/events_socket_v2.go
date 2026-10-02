package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/clientip"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/gorilla/websocket"
)

const eventsSchemeHTTP = "http"
const eventsSchemeHTTPS = "https"
const eventsAdminRole = "admin"

const EventsSocketProtocol = "silo.events.v2"
const eventsTicketProtocolPrefix = "silo.ticket."
const eventsSessionCheckInterval = 15 * time.Second

// EventsCloseAccessChanged is the close code, and eventsAccessChanged the
// close reason and frame type, an events socket ends with when the access it
// was opened under has changed while the login session stays valid.
const EventsCloseAccessChanged = 4001
const eventsAccessChanged = "access_changed"

// eventsAccessChangedGrace bounds how long the recheck waits for the
// connection loop to send the access_changed frame before closing anyway.
const eventsAccessChangedGrace = 2 * wsWriteTimeout

// errSocketAccessChanged is the validator's answer when the login session
// still holds but the access a ticket was minted under no longer does: the
// account role, the effective role, the scope fingerprint, or profile
// verification changed. It wraps evt.ErrSocketTicket, so callers that only
// tell a usable credential from an unusable one treat it as unusable.
var errSocketAccessChanged = fmt.Errorf("%w: access changed", evt.ErrSocketTicket)

type EventsSocketValidator func(context.Context, evt.SocketIdentity) (context.Context, *auth.Claims, error)

type EventsSocketV2 struct {
	Events   *EventsHandler
	Tickets  *evt.SocketTicketStore
	Validate EventsSocketValidator
	// PublicOrigin is the configured external origin, never a forwarded header.
	PublicOrigin   string
	publicOrigin   atomic.Pointer[string]
	overlayOrigins atomic.Pointer[OverlayOriginSource]
	checkInterval  time.Duration
}

func (h *EventsSocketV2) Mint(ctx context.Context, identity evt.SocketIdentity) (string, error) {
	if h == nil || h.Events == nil || h.Events.hub == nil || h.Tickets == nil || h.Validate == nil {
		return "", evt.ErrSocketTicket
	}
	validated, claims, err := h.Validate(ctx, identity)
	if err != nil {
		return "", err
	}
	scope, ok := access.GetScope(validated)
	// A fingerprint of fallback preferences would differ from the profile's
	// real scope once the read recovers and end the connection as an access
	// change; refuse the ticket as for any other failed read, and let the
	// client retry.
	if !ok || scope.PreferencesDegraded {
		return "", evt.ErrSocketTicket
	}
	identity.AccessFingerprint = eventsScopeFingerprint(scope)
	identity.EffectiveRole = claims.Role
	return h.Tickets.Mint(ctx, identity)
}

func (h *EventsSocketV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h == nil || h.Events == nil || h.Events.hub == nil || h.Tickets == nil || h.Validate == nil {
		http.Error(w, "realtime unavailable", http.StatusServiceUnavailable)
		return
	}
	// Proof travels only in the handshake protocols, never a URL or cookie.
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.Query().Has("token") || r.URL.Query().Has("ticket") {
		http.Error(w, "invalid handshake", http.StatusBadRequest)
		return
	}
	if !h.validOrigin(r) {
		http.Error(w, "origin refused", http.StatusForbidden)
		return
	}
	protocols := websocket.Subprotocols(r)
	if len(protocols) != 2 || protocols[0] != EventsSocketProtocol || !strings.HasPrefix(protocols[1], eventsTicketProtocolPrefix) {
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
	identity, err := h.Tickets.Consume(r.Context(), strings.TrimPrefix(protocols[1], eventsTicketProtocolPrefix))
	if err != nil {
		http.Error(w, "invalid realtime credential", http.StatusUnauthorized)
		return
	}
	validated, claims, err := h.Validate(r.Context(), identity)
	if err != nil {
		http.Error(w, "realtime authority expired", http.StatusUnauthorized)
		return
	}
	deadline := time.Now().Add(evt.SocketMaxLifetime)
	if identity.AccessExpiresAt.Before(deadline) {
		deadline = identity.AccessExpiresAt
	}
	ctx, cancel := context.WithDeadline(validated, deadline)
	defer cancel()
	// Poll the actual session/account/profile validator without extending the
	// deadline. Revocation and policy changes close an otherwise healthy socket.
	accessChanged := make(chan struct{})
	go func() {
		err := h.awaitAuthorityLoss(ctx, identity)
		if err == nil {
			return
		}
		if !errors.Is(err, errSocketAccessChanged) {
			cancel()
			return
		}
		// The connection loop is the only data writer, so it sends the
		// access_changed frame and close code. Canceling now would close the
		// connection under it; cancel only if it does not finish in time.
		close(accessChanged)
		grace := time.NewTimer(eventsAccessChangedGrace)
		defer grace.Stop()
		select {
		case <-ctx.Done():
		case <-grace.C:
			cancel()
		}
	}()
	h.Events.serveWebSocket(w, r.WithContext(ctx), claims, identity.ProfileID, websocket.Upgrader{Subprotocols: []string{EventsSocketProtocol}, CheckOrigin: h.validOrigin}, accessChanged)
}

// awaitAuthorityLoss re-validates the connection's identity every check
// interval and returns the first validation error, or nil once ctx ends.
func (h *EventsSocketV2) awaitAuthorityLoss(ctx context.Context, identity evt.SocketIdentity) error {
	interval := h.checkInterval
	if interval <= 0 {
		interval = eventsSessionCheckInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
			_, _, err := h.Validate(checkCtx, identity)
			stop()
			if err != nil {
				return err
			}
		}
	}
}

func (h *EventsSocketV2) validOrigin(r *http.Request) bool {
	return socketOriginAllowed(r, h.currentPublicOrigin(), overlayOriginsFrom(h.overlayOrigins.Load()))
}

func (h *EventsSocketV2) currentPublicOrigin() string {
	if origin := h.publicOrigin.Load(); origin != nil {
		return *origin
	}
	return h.PublicOrigin
}

// SetPublicOrigin updates the browser origin accepted by new handshakes.
func (h *EventsSocketV2) SetPublicOrigin(origin string) {
	normalized := strings.TrimRight(origin, "/")
	h.publicOrigin.Store(&normalized)
}

// SetOverlayOrigins installs the source of overlay origins (connected
// network access providers on this host) accepted next to the public origin.
func (h *EventsSocketV2) SetOverlayOrigins(source OverlayOriginSource) {
	h.overlayOrigins.Store(&source)
}

// OverlayOriginSource lists the scheme://host[:port] origins of the network
// access providers currently connected on this host. netaccess.StatusCache's
// ConnectedOrigins is the production source; it is read per handshake so a
// provider that connects or drops is reflected without a config reload.
type OverlayOriginSource func() []string

func overlayOriginsFrom(source *OverlayOriginSource) []string {
	if source == nil || *source == nil {
		return nil
	}
	return (*source)()
}

// socketOriginAllowed accepts a browser Origin that matches the configured
// public origin, the request's own scheme and host, or one of the overlay
// origins connected providers report. The request's own origin stays accepted
// with a public origin configured so a browser on a LAN address or IP:port can
// open the sockets of the page it loaded. All are exact scheme and host
// matches; forwarded host headers are never consulted here.
func socketOriginAllowed(r *http.Request, publicOrigin string, overlayOrigins []string) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	} // Native clients authenticate with the same proof.
	if len(origins) != 1 {
		return false
	}
	origin, err := url.Parse(origins[0])
	if err != nil || origin.User != nil || origin.Host == "" || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" || (origin.Scheme != eventsSchemeHTTPS && origin.Scheme != eventsSchemeHTTP) {
		return false
	}
	if publicOrigin != "" && originMatches(origin, publicOrigin) {
		return true
	}
	// An empty scheme means ambiguous proxy metadata; refuse the request's own
	// origin rather than guess it.
	if scheme := clientip.RequestScheme(r); scheme != "" && originMatches(origin, scheme+"://"+r.Host) {
		return true
	}
	for _, overlay := range overlayOrigins {
		if originMatches(origin, overlay) {
			return true
		}
	}
	return false
}

func originMatches(origin *url.URL, expected string) bool {
	target, err := url.Parse(expected)
	return err == nil && strings.EqualFold(origin.Host, target.Host) && strings.EqualFold(origin.Scheme, target.Scheme)
}

type eventsSessionValidator interface {
	IsValid(context.Context, string) (bool, error)
}

// NewEventsSocketV2 reuses the current account, session and viewer authorities.
func NewEventsSocketV2(events *EventsHandler, tickets *evt.SocketTicketStore, sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker, publicURL string) *EventsSocketV2 {
	h := &EventsSocketV2{Events: events, Tickets: tickets, PublicOrigin: publicURL}
	h.Validate = newSocketAuthorityValidator(sessions, users, resolver, primary)
	return h
}

func newSocketAuthorityValidator(sessions eventsSessionValidator, users access.UserRepository, resolver apimw.ViewerResolver, primary apimw.PrimaryProfileChecker) EventsSocketValidator {
	if sessions == nil || users == nil || resolver == nil || primary == nil {
		return nil
	}
	return func(ctx context.Context, identity evt.SocketIdentity) (context.Context, *auth.Claims, error) {
		checkCtx, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		if identity.SessionID == "" || !identity.AccessExpiresAt.After(time.Now()) {
			return ctx, nil, evt.ErrSocketTicket
		}
		// A revoked or expired session and a missing or disabled account end
		// the credential. A changed role, scope or profile verification under
		// a valid session is an access change the client can recover from by
		// refetching and reconnecting.
		valid, err := sessions.IsValid(checkCtx, identity.SessionID)
		if err != nil || !valid {
			return ctx, nil, evt.ErrSocketTicket
		}
		user, err := users.GetByID(checkCtx, identity.UserID)
		if err != nil || user == nil || !user.Enabled {
			return ctx, nil, evt.ErrSocketTicket
		}
		if user.Role != identity.Role {
			return ctx, nil, errSocketAccessChanged
		}
		scope, err := resolver.Resolve(checkCtx, access.ResolveInput{UserID: identity.UserID, SessionID: identity.SessionID, ProfileID: identity.ProfileID, ProfileToken: identity.ProfileToken})
		if errors.Is(err, access.ErrProfileUnverified) || (err == nil && !scope.ProfileVerified) {
			return ctx, nil, errSocketAccessChanged
		}
		if err != nil {
			return ctx, nil, evt.ErrSocketTicket
		}
		// Preferences that fell back to their defaults say nothing about
		// whether access changed, so this round skips the comparison.
		if identity.AccessFingerprint != "" && !scope.PreferencesDegraded && identity.AccessFingerprint != eventsScopeFingerprint(scope) {
			return ctx, nil, errSocketAccessChanged
		}
		role := user.Role
		if role == eventsAdminRole && identity.ProfileID != "" {
			isPrimary, found, err := primary(checkCtx, identity.UserID, identity.ProfileID)
			if err != nil || !found {
				return ctx, nil, evt.ErrSocketTicket
			}
			if !isPrimary {
				role = string(scopeUser)
			}
		}
		if identity.AccessFingerprint != "" && identity.EffectiveRole != role {
			return ctx, nil, errSocketAccessChanged
		}
		claims := &auth.Claims{ImpersonatorUserID: identity.ImpersonatorUserID, UserID: identity.UserID, SessionID: identity.SessionID, Role: role, TokenType: auth.TokenTypeAccess}
		ctx = apimw.SetClaims(ctx, claims)
		ctx = apimw.SetProfileID(ctx, identity.ProfileID)
		ctx = access.SetScope(ctx, scope)
		return ctx, claims, nil
	}
}

func eventsScopeFingerprint(scope access.Scope) string {
	data, _ := json.Marshal(scope)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
