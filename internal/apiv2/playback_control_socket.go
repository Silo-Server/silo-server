package apiv2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/danielgtaylor/huma/v2"
)

// PlaybackControlSocketService mints one session-bound handshake credential
// and serves the documented plain-WebSocket control handshake.
type PlaybackControlSocketService interface {
	Available() bool
	Mint(ctx context.Context, identity evt.SocketIdentity, playbackSessionID, installationID string) (string, time.Time, error)
	ServeHTTP(http.ResponseWriter, *http.Request)
}

// PlaybackControlSocketTicketInput delegates the caller's current login
// session and profile proof to one control handshake for one playback session.
type PlaybackControlSocketTicketInput struct {
	SessionID string `path:"session_id" minLength:"1" maxLength:"128"`
	Body      struct {
		// InstallationID is the playback installation from getPlaybackCapabilities
		// that started the session; omitted for a session the bridge started.
		InstallationID ID `json:"installation_id,omitempty" doc:"Installation identifier from playback capabilities; required for a session started through v2, absent for a bridge-started session"`
	}
}

// PlaybackControlSocketTicket is the single-use handshake credential.
type PlaybackControlSocketTicket struct {
	Ticket               string `json:"ticket" doc:"Opaque single-use credential; offer it as silo.ticket.<ticket> after the protocol"`
	ExpiresIn            int    `json:"expires_in" doc:"Seconds until the credential expires unused"`
	MaxConnectionSeconds int    `json:"max_connection_seconds" doc:"Upper bound on one connection's lifetime; reconnect with a new credential"`
	Protocol             string `json:"protocol" doc:"The subprotocol to offer first and the only one the server selects" example:"silo.playback-control.v2"`
}
type PlaybackControlSocketTicketOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         PlaybackControlSocketTicket
}

// PlaybackControlSocketCapabilitiesOutput reports whether the handshake is served.
type PlaybackControlSocketCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         PlaybackControlSocketCapabilitiesOutputBody
}

type PlaybackControlSocketCapabilitiesOutputBody struct {
	Capability
	Available bool   `json:"available"`
	Protocol  string `json:"protocol"`
}

const (
	playbackControlSocketProtocolPath = "/{session_id}/control/ws"
	playbackControlSocketTicketPath   = "/{session_id}/control/ws-ticket"
	playbackControlSocketMaxSeconds   = 4 * 60 * 60
)

func registerPlaybackControlSocket(reg *Registry) {
	const root = Prefix + "/playback/sessions"
	Register(reg, Operation{Operation: humaOp(http.MethodGet, root+"/control/capabilities", "getPlaybackControlSocketCapabilities", "playback", "Discover whether the session-bound playback control handshake is served."), Class: ClassProfileScoped, ServiceBacked: true}, func(ctx context.Context, _ *CapabilityInput) (*PlaybackControlSocketCapabilitiesOutput, error) {
		out := &PlaybackControlSocketCapabilitiesOutput{CacheControl: playbackCacheControl}
		out.Body.Allowed = new(capabilityLoginAllowed(ctx))
		out.Body.Available = reg.deps.PlaybackControlSocket != nil && reg.deps.PlaybackControlSocket.Available()
		if out.Body.Available {
			out.Body.Protocol = handlers.PlaybackControlSocketProtocol
		}
		return out, nil
	})

	mint := Operation{Operation: humaOp(http.MethodPost, root+playbackControlSocketTicketPath, "createPlaybackControlSocketTicket", "playback", "Delegate the current login session and profile proof to one control handshake for a playback session this owner started."), Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}
	mint.Errors = []int{http.StatusForbidden, http.StatusNotFound, http.StatusConflict}
	Register(reg, mint, func(ctx context.Context, in *PlaybackControlSocketTicketInput) (*PlaybackControlSocketTicketOutput, error) {
		svc := reg.deps.PlaybackControlSocket
		if svc == nil || !svc.Available() {
			return nil, unavailable("playback control")
		}
		identity, p := socketIdentity(ctx)
		if p != nil {
			return nil, p
		}
		if in.Body.InstallationID != "" && !playbackUUID(string(in.Body.InstallationID)) {
			return nil, validationProblem("body.installation_id", codeInvalid, "Expected the installation identifier from capabilities.")
		}

		ticket, expiry, err := svc.Mint(ctx, identity, in.SessionID, string(in.Body.InstallationID))
		if err != nil {
			return nil, playbackControlSocketProblem(err)
		}
		return &PlaybackControlSocketTicketOutput{CacheControl: playbackCacheControl, Body: PlaybackControlSocketTicket{Ticket: ticket, ExpiresIn: max(0, int(time.Until(expiry).Seconds())), MaxConnectionSeconds: playbackControlSocketMaxSeconds, Protocol: handlers.PlaybackControlSocketProtocol}}, nil
	})

	responses := socketResponses([]string{"400", "401", "403", "404", "409", "503"}, "Control handshake refused.", "Playback control connection established for the session's owner.", adminLogsHandshakeHeaderDoc)
	raw := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: root + playbackControlSocketProtocolPath, OperationID: "connectPlaybackControlSocket", Tags: []string{playbackTag}, Summary: "Connect the session owner's playback control lane using a single-use session-bound credential.", Responses: responses}, Class: ClassPublic, ServiceBacked: true}
	raw.Parameters = []*huma.Param{
		{Name: adminLogsQuerySessionID, In: roomSocketPathParameter, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Playback session the credential was minted for."},
		{Name: eventsProtocolHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Offer silo.playback-control.v2 followed by silo.ticket.<single-use-ticket>."},
		{Name: eventsOriginHeader, In: paramInHeader, Schema: &huma.Schema{Type: huma.TypeString}, Description: socketOriginHeaderDoc},
	}
	RegisterRaw(reg, RawOperation{Operation: raw, Protocol: eventsRawProtocol, Reason: "Single-use session proof, Origin and subprotocol checks precede the upgrade; account, profile and installation ownership are re-checked at upgrade."}, socketHandler(reg.deps.PlaybackControlSocket, "playback control unavailable"))
}

// playbackControlSocketProblem renders the seam's admission errors.
func playbackControlSocketProblem(err error) *Problem {
	switch {
	case errors.Is(err, handlers.ErrPlaybackControlSocketUnavailable):
		return unavailable("playback control")
	case errors.Is(err, playback.ErrSessionNotFound):
		return NewProblem(TypeNotFound, "Playback session not found.")
	case errors.Is(err, handlers.ErrPlaybackControlSocketNotOwner):
		return NewProblem(TypePermissionDenied, "The playback session belongs to another account or profile.")
	case errors.Is(err, handlers.ErrPlaybackControlSocketInstallation):
		return NewProblem(TypeConflict, "The installation does not match the session; refresh capabilities.")
	case errors.Is(err, handlers.ErrPlaybackControlSocketStale):
		return NewProblem(TypeConflict, "The session's control credential is stale; request a new ticket.")
	case errors.Is(err, handlers.ErrPlaybackControlSocketLaneHeld):
		return NewProblem(TypeConflict, "The control lane is held by another installation.")
	case errors.Is(err, evt.ErrSocketTicket):
		return NewProblem(TypePermissionDenied, "Realtime authority could not be delegated.")
	}
	return serviceProblem(err)
}

func (c PlaybackControlSocketCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
