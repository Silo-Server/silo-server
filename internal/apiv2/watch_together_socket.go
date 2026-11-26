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

// watchTogetherHostOtherAppRule and watchTogetherMemberOtherAppRule describe
// the Watch Together operations that drive a room for a session another app
// opened. Host authority is the account and profile, so such a session acting
// as the host's profile would steer the room of the person's own device.
const (
	watchTogetherHostOtherAppRule = "A login session opened by another app the person allowed (its app key was enrolled through a grant) " +
		"is refused with 403 permission_denied before the room is read or changed: it may not host a room, and the host is the account and profile, " +
		"so it would otherwise act for the person's own device. A session the person's own app key opened, and one opened by device sign-in, are not refused."
	watchTogetherMemberOtherAppRule = "A login session opened by another app the person allowed (its app key was enrolled through a grant) " +
		"is refused with 403 permission_denied before the room is read or changed: a fallback changes what every member plays. " +
		"A session the person's own app key opened, and one opened by device sign-in, are not refused."
)

type WatchTogetherSocketService interface {
	// otherApp marks a session another app opened with a grant: refused
	// when it would connect as the room's host, and refused transport
	// requests once connected.
	MintRoomSocket(ctx context.Context, roomID, proof string, identity evt.SocketIdentity, otherApp bool) (string, time.Time, error)
	ServeHTTP(http.ResponseWriter, *http.Request)
}
type WatchTogetherSocketTicketInput struct {
	RoomID    string `path:"room_id"`
	RoomToken string `header:"X-Room-Token" maxLength:"4096"`
}

func registerWatchTogetherSocket(reg *Registry) {
	op := Operation{Operation: humaOp(http.MethodPost, Prefix+"/watch-together/rooms/{room_id}/ws-ticket", "createWatchTogetherSocketTicket", "realtime", "Delegate a current login session and original room proof for one room handshake."), Class: ClassProfileScoped, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	op.Errors = []int{409}
	op.Description = "A login session opened by another app the person allowed (its app key was enrolled through a grant) is refused with 403 permission_denied " +
		"when the room's host is its own account and profile: it may not host a room. As a member of another host's room it may connect, " +
		"but the socket refuses its transport_request frames (play, pause, seek for everyone) with the error frame any refused request gets, " +
		"and an attach_session naming a stream its own login session did not start. " +
		"A ticket does not reserve the member's seat: a connection made while another connection holds that seat receives an error frame " +
		"of code forbidden and the socket closes; request a new ticket and retry shortly."
	Register(reg, withOrigins(op, grantedOwnSeat), func(ctx context.Context, in *WatchTogetherSocketTicketInput) (*EventsSocketTicketOutput, error) {
		if reg.deps.WatchTogetherSocket == nil {
			return nil, unavailable("room socket")
		}
		identity, p := socketIdentity(ctx)
		if p != nil {
			return nil, p
		}

		reaches, p := reg.sessionReachesOtherDevices(ctx)
		if p != nil {
			return nil, p
		}
		ticket, expiry, err := reg.deps.WatchTogetherSocket.MintRoomSocket(ctx, in.RoomID, in.RoomToken, identity, !reaches)
		if p := socketAuthorityUnavailable(err); p != nil {
			return nil, p
		}
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
