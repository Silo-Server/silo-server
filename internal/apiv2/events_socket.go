package apiv2

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/danielgtaylor/huma/v2"
)

const eventsRawProtocol = "websocket"
const eventsConnectionHeader = "Connection"

const eventsPlainMedia = "text/plain"
const eventsAcceptHeader = "Sec-WebSocket-Accept"
const eventsProtocolHeader = "Sec-WebSocket-Protocol"
const eventsUpgradeHeader = "Upgrade"
const eventsOriginHeader = "Origin"
const eventsChannelsQuery = "channels"

type EventsSocketService interface {
	Mint(context.Context, evt.SocketIdentity) (string, error)
	ServeHTTP(http.ResponseWriter, *http.Request)
}
type EventsSocketTicket struct {
	Ticket               string `json:"ticket"`
	ExpiresIn            int    `json:"expires_in"`
	MaxConnectionSeconds int    `json:"max_connection_seconds"`
	Protocol             string `json:"protocol"`
}
type EventsSocketTicketOutput struct{ Body EventsSocketTicket }

// eventsSocketIdentity is socketIdentity, or for an API key the key itself:
// a key holding admin:monitor:read, sent with no profile, or a user key
// holding catalog:read, watch:read or notifications may open the events socket, and its
// ticket names the key rather than a login session.
func eventsSocketIdentity(ctx context.Context) (evt.SocketIdentity, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.TokenType != auth.TokenTypeAPIKey {
		return socketIdentity(ctx)
	}
	if claims.APIKeyID > 0 && userKeyEventsSocket(ctx, claims) {
		// A user key acts as the profile it named (a bound key always names
		// its own) and holds the user role on the socket.
		identity := evt.SocketIdentity{UserID: claims.UserID, Role: models.RoleUser, APIKeyID: claims.APIKeyID, ProfileID: profileFrom(ctx), AccessExpiresAt: time.Now().Add(evt.APIKeySocketMaxLifetime)}
		return identity, nil
	}
	if claims.APIKeyID <= 0 || !slices.Contains(claims.APIKeyScopes, auth.ScopeAdminMonitorRead) {
		return evt.SocketIdentity{}, NewProblem(TypePermissionDenied, "Only an API key with the admin:monitor:read, catalog:read, watch:read or notifications scope may open the realtime socket.")
	}
	if profileFrom(ctx) != "" {
		return evt.SocketIdentity{}, NewProblem(TypePermissionDenied, "An API key opens the realtime socket without a profile.")
	}
	identity := evt.SocketIdentity{UserID: claims.UserID, Role: claims.Role, APIKeyID: claims.APIKeyID, AccessExpiresAt: time.Now().Add(evt.APIKeySocketMaxLifetime)}
	if r := requestFrom(ctx); r != nil {
		identity.DeviceID = socketDeviceID(r)
	}
	return identity, nil
}

// userKeyEventsSocket reports whether an API key's request opens the events
// socket as a user key: it holds an event scope and is not a monitoring
// request. A key holding admin:monitor:read as well is a monitoring key when
// it names no profile and a user key when it names one.
func userKeyEventsSocket(ctx context.Context, claims *auth.Claims) bool {
	if !auth.HoldsAPIKeyEventScope(claims.APIKeyScopes) {
		return false
	}
	return !slices.Contains(claims.APIKeyScopes, auth.ScopeAdminMonitorRead) || declaresProfile(ctx)
}

// socketAuthorityRetrySeconds is the Retry-After a ticket mint answers when
// the credential could not be checked.
const socketAuthorityRetrySeconds = 5

// socketAuthorityUnavailable answers a ticket mint whose credential could not
// be checked with 503, so the client retries instead of treating the
// credential as refused; nil for any other outcome.
func socketAuthorityUnavailable(err error) *Problem {
	if !errors.Is(err, evt.ErrSocketAuthorityUnavailable) {
		return nil
	}
	return NewProblem(TypeDependencyUnavailable, "Realtime authority could not be checked; retry shortly.").WithRetryAfter(socketAuthorityRetrySeconds)
}

func registerEventsSocket(reg *Registry) {
	mint := Operation{Operation: humaOp(http.MethodPost, Prefix+"/events/ws-ticket", "createEventsSocketTicket", "realtime", "Delegate the current login session, or an API key holding admin:monitor:read, catalog:read, watch:read or notifications, for one realtime handshake."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, mint, func(ctx context.Context, _ *struct{}) (*EventsSocketTicketOutput, error) {
		svc := reg.deps.EventsSocket
		if svc == nil {
			return nil, unavailable("realtime events")
		}
		identity, p := eventsSocketIdentity(ctx)
		if p != nil {
			return nil, p
		}

		ticket, err := svc.Mint(ctx, identity)
		if p := socketAuthorityUnavailable(err); p != nil {
			return nil, p
		}
		if err != nil {
			return nil, NewProblem(TypePermissionDenied, "Realtime authority could not be delegated.")
		}
		return &EventsSocketTicketOutput{Body: EventsSocketTicket{Ticket: ticket, ExpiresIn: int(min(evt.SocketTicketTTL, time.Until(identity.AccessExpiresAt)).Seconds()), MaxConnectionSeconds: int(identity.MaxLifetime().Seconds()), Protocol: "silo.events.v2"}}, nil
	})
	responses := socketResponses([]string{"400", "401", "403", "503"}, "Handshake refused.", "Realtime connection established.", "WebSocket handshake header.")
	op := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/events/ws", OperationID: "connectEventsSocket", Tags: []string{"realtime"}, Summary: "Connect using one session-bound ticket in Sec-WebSocket-Protocol.", Responses: responses}, Class: ClassPublic, ServiceBacked: true}
	op.Parameters = []*huma.Param{
		{Name: eventsProtocolHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Offer silo.events.v2 followed by silo.ticket.<single-use-ticket>."},
		{Name: eventsOriginHeader, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}, Description: socketOriginHeaderDoc},
		{Name: eventsChannelsQuery, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Optional comma-separated declared channel selection."},
	}
	RegisterRaw(reg, RawOperation{Operation: op, Protocol: eventsRawProtocol, Reason: "Session-bound single-use proof, Origin and subprotocol validation precede the upgrade; connection lifetime is bounded."}, socketHandler(reg.deps.EventsSocket, "realtime unavailable"))
}
