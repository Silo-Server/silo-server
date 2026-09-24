package apiv2

import (
	"context"
	"net/http"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/watchtogether"
	"github.com/danielgtaylor/huma/v2"
)

const roomSocketPathParameter = "path"

type WatchTogetherSocketService interface {
	MintRoomSocket(context.Context, string, string, evt.SocketIdentity) (string, time.Time, error)
	ServeHTTP(http.ResponseWriter, *http.Request)
}
type WatchTogetherSocketTicketInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
}

func registerWatchTogetherSocket(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/ws-ticket", "createWatchTogetherSocketTicket", "realtime", "Delegate a current login session and original room proof for one room handshake."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.Errors = []int{409}
	Register(reg, op, func(ctx context.Context, in *WatchTogetherSocketTicketInput) (*EventsSocketTicketOutput, error) {
		if reg.deps.WatchTogetherSocket == nil {
			return nil, unavailable("room socket")
		}
		identity, p := socketIdentity(ctx)
		if p != nil {
			return nil, p
		}

		ticket, expiry, err := reg.deps.WatchTogetherSocket.MintRoomSocket(ctx, in.RoomID, in.RoomToken, identity)
		if err != nil {
			return nil, suggestionProblem(err)
		}
		return &EventsSocketTicketOutput{Body: EventsSocketTicket{Ticket: ticket, ExpiresIn: max(0, int(time.Until(expiry).Seconds())), MaxConnectionSeconds: int(watchtogether.RoomSocketMaxLifetime.Seconds()), Protocol: watchtogether.RoomSocketProtocol}}, nil
	})
	responses := socketResponses([]string{"400", "401", "403", "503"}, "Room handshake refused.", "Room connection established.", "WebSocket handshake header.")
	raw := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/watch-together/rooms/{room_id}/ws", OperationID: "connectWatchTogetherSocket", Tags: []string{"realtime"}, Summary: "Connect a room using a single-use session-bound credential.", Responses: responses}, Class: ClassPublic, ServiceBacked: true}
	raw.Parameters = []*huma.Param{
		{Name: "room_id", In: roomSocketPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}},
		{Name: eventsProtocolHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Offer silo.room.v2 followed by silo.ticket.<single-use-ticket>."},
		{Name: eventsOriginHeader, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}, Description: socketOriginHeaderDoc},
	}
	RegisterRaw(reg, RawOperation{Operation: raw, Protocol: eventsRawProtocol, Reason: "Room-bound single-use session proof, Origin and subprotocol checks precede upgrade; connection authority and lifetime are bounded."}, socketHandler(reg.deps.WatchTogetherSocket, "room socket unavailable"))
}
