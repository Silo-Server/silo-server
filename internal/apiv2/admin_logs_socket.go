package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/danielgtaylor/huma/v2"
)

// AdminLogsSocketService mints one administrator handshake credential and
// serves the documented plain-WebSocket log stream handshake.
type AdminLogsSocketService interface {
	Available() bool
	Mint(context.Context, evt.SocketIdentity) (string, error)
	ServeHTTP(http.ResponseWriter, *http.Request)
}

// AdminLogsSocketTicketOutput reuses the events ticket shape: opaque single-use
// credential, its unused expiry, the connection lifetime bound and the
// subprotocol to offer first.
type AdminLogsSocketTicketOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         EventsSocketTicket
}

// AdminLogsSocketCapabilitiesOutput reports whether the handshake is served.
type AdminLogsSocketCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminLogsSocketCapabilitiesOutputBody
}

type AdminLogsSocketCapabilitiesOutputBody struct {
	Capability
	Available bool   `json:"available"`
	Protocol  string `json:"protocol"`
	// Streams lists the stream selections the handshake accepts.
	Streams []string `json:"streams"`
}

const (
	adminLogsSocketCacheControl = "no-store"
	adminLogsQuerySessionID     = "session_id"
	adminLogsQueryPlaybackID    = "playback_session_id"
	adminLogsQueryRequestID     = "request_id"
	adminLogsQueryUserID        = "user_id"
	adminLogsFormatDateTime     = "date-time"
	adminLogsQueryLimit         = "limit"
	adminLogsHandshakeHeaderDoc = "WebSocket handshake header."
	// socketOriginHeaderDoc documents the Origin requirement shared by every socket handshake.
	socketOriginHeaderDoc = "Browser origin must match the configured public origin, the request origin, or a connected network access overlay origin."
)

func registerAdminLogsSocket(reg *Registry) {
	const root = Prefix + "/admin/logs"
	Register(reg, Operation{Operation: humaOp(http.MethodGet, root+"/ws/capabilities", "getAdminLogsSocketCapabilities", "admin-observability", "Discover whether the administrator log stream handshake is served."), Class: ClassActingAdmin, ServiceBacked: true}, func(ctx context.Context, _ *CapabilityInput) (*AdminLogsSocketCapabilitiesOutput, error) {
		out := &AdminLogsSocketCapabilitiesOutput{CacheControl: adminLogsSocketCacheControl}
		out.Body.Streams = []string{}
		out.Body.Allowed = new(capabilityLoginAllowed(ctx))
		out.Body.Available = reg.deps.AdminLogsSocket != nil && reg.deps.AdminLogsSocket.Available()
		if out.Body.Available {
			out.Body.Protocol = handlers.AdminLogsSocketProtocol
			out.Body.Streams = []string{"app", "audit"}
		}
		return out, nil
	})

	mint := Operation{Operation: humaOp(http.MethodPost, root+"/ws-ticket", "createAdminLogsSocketTicket", "admin-observability", "Delegate the current administrator login session to one log stream handshake. Minting is naturally idempotent in effect: extra credentials are unused orphans that expire."), Class: ClassActingAdmin, DemoRestricted: true, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	Register(reg, mint, func(ctx context.Context, _ *struct{}) (*AdminLogsSocketTicketOutput, error) {
		svc := reg.deps.AdminLogsSocket
		if svc == nil || !svc.Available() {
			return nil, unavailable("administrator log stream")
		}
		identity, p := socketIdentity(ctx)
		if p != nil {
			return nil, p
		}

		ticket, err := svc.Mint(ctx, identity)
		if err != nil {
			if errors.Is(err, handlers.ErrAdminLogsSocketForbidden) {
				return nil, NewProblem(TypePermissionDenied, "Administrator authority is required for the log stream.")
			}
			var apiErr *handlers.APIError
			if errors.As(err, &apiErr) {
				return nil, serviceProblem(err)
			}
			return nil, NewProblem(TypePermissionDenied, "Log stream authority could not be delegated.")
		}
		return &AdminLogsSocketTicketOutput{CacheControl: adminLogsSocketCacheControl, Body: EventsSocketTicket{Ticket: ticket, ExpiresIn: int(min(evt.SocketTicketTTL, time.Until(identity.AccessExpiresAt)).Seconds()), MaxConnectionSeconds: int(evt.SocketMaxLifetime.Seconds()), Protocol: handlers.AdminLogsSocketProtocol}}, nil
	})

	responses := socketResponses([]string{"400", "401", "403", "503"}, "Handshake refused.", "Administrator log stream established.", adminLogsHandshakeHeaderDoc)
	raw := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: root + "/ws", OperationID: "connectAdminLogsSocket", Tags: []string{"admin-observability"}, Summary: "Connect the administrator log stream using a single-use session-bound credential in Sec-WebSocket-Protocol. Frames are the bridge's snapshot, append and error messages.", Responses: responses}, Class: ClassPublic, ServiceBacked: true}
	raw.Parameters = []*huma.Param{
		{Name: eventsProtocolHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Offer silo.admin-logs.v2 followed by silo.ticket.<single-use-ticket>."},
		{Name: eventsOriginHeader, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}, Description: socketOriginHeaderDoc},
		{Name: "stream", In: discordLinkQuery, Required: true, Schema: &huma.Schema{Type: huma.TypeString, Enum: []any{"app", "audit"}}, Description: "Which log stream to snapshot and follow."},
		{Name: adminLogsQueryLimit, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeInteger}, Description: "Snapshot size, 1-200; the list route default applies when omitted."},
		{Name: "cursor", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Snapshot continuation from the matching list route."},
		{Name: "level", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "app: comma-separated levels."},
		{Name: "component", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "app: component filter."},
		{Name: "node_id", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "app: node filter."},
		{Name: "q", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "app: message text search."},
		{Name: labelMethod, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "audit: HTTP method filter."},
		{Name: "path_prefix", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "audit: path prefix filter."},
		{Name: "status_code", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeInteger}, Description: "audit: status filter."},
		{Name: "client_ip", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "audit: client address or prefix filter."},
		{Name: adminLogsQueryRequestID, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Request identifier filter."},
		{Name: adminLogsQueryUserID, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeInteger}, Description: "Account filter."},
		{Name: adminLogsQuerySessionID, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Login session filter."},
		{Name: adminLogsQueryPlaybackID, In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Playback session filter."},
		{Name: "from", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString, Format: adminLogsFormatDateTime}, Description: "Inclusive lower time bound."},
		{Name: "to", In: discordLinkQuery, Schema: &huma.Schema{Type: huma.TypeString, Format: adminLogsFormatDateTime}, Description: "Inclusive upper time bound."},
	}
	RegisterRaw(reg, RawOperation{Operation: raw, Protocol: eventsRawProtocol, Reason: "Administrator single-use session proof, Origin and subprotocol checks precede the upgrade; administrator authority is rechecked while connected and the lifetime is bounded."}, socketHandler(reg.deps.AdminLogsSocket, "log stream unavailable"))
}

func (c AdminLogsSocketCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
