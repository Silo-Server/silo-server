package apiv2

import (
	"context"
	"net/http"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
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

func registerEventsSocket(reg *Registry) {
	mint := Operation{Operation: humaOp(http.MethodPost, Prefix+"/events/ws-ticket", "createEventsSocketTicket", "realtime", "Delegate the current login session for one realtime handshake."), Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, mint, func(ctx context.Context, _ *struct{}) (*EventsSocketTicketOutput, error) {
		svc := reg.deps.EventsSocket
		if svc == nil {
			return nil, unavailable("realtime events")
		}
		identity, p := socketIdentity(ctx)
		if p != nil {
			return nil, p
		}

		ticket, err := svc.Mint(ctx, identity)
		if err != nil {
			return nil, NewProblem(TypePermissionDenied, "Realtime authority could not be delegated.")
		}
		return &EventsSocketTicketOutput{Body: EventsSocketTicket{Ticket: ticket, ExpiresIn: int(min(evt.SocketTicketTTL, time.Until(identity.AccessExpiresAt)).Seconds()), MaxConnectionSeconds: int(evt.SocketMaxLifetime.Seconds()), Protocol: "silo.events.v2"}}, nil
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
