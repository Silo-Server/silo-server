package apiv2

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"
	"tailscale.com/net/wsconn"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

// directSignalingBucket is the per-client-IP rate-limit budget of registration
// and netmap, sized for a household of devices behind one NAT.
const directSignalingBucket = "direct_signaling"

// directRegistrationMaxBytes is the protocol's registration body limit.
const directRegistrationMaxBytes = 64 << 10

// directSignalProtocol is the WebSocket subprotocol of the signaling stream.
const directSignalProtocol = "derp"

// DirectConnectionConfirmation is a host's observation of a device's direct
// UDP pong.
type DirectConnectionConfirmation struct {
	Peer     string `json:"peer" pattern:"^nodekey:[0-9a-f]{64}$" doc:"The node whose direct pong was received"`
	DiscoKey string `json:"disco_key" pattern:"^discokey:[0-9a-f]{64}$" doc:"That node's discovery key at the pong"`
	Endpoint string `json:"endpoint" doc:"The unicast UDP source of the pong, as host:port; not validated as a URI"`
}

// DirectConnectionRegistration is the body a device's engine posts every refresh
// period: its discovery identity, its endpoints and the pongs it has seen.
type DirectConnectionRegistration struct {
	NodeKey       string                         `json:"node_key" pattern:"^nodekey:[0-9a-f]{64}$" doc:"The engine's node key; it must equal the ticket's"`
	DiscoKey      string                         `json:"disco_key" pattern:"^discokey:[0-9a-f]{64}$" doc:"The engine's discovery key"`
	Endpoints     []string                       `json:"endpoints" maxItems:"64" doc:"At most 64 unicast UDP endpoints as host:port"`
	Confirmations []DirectConnectionConfirmation `json:"confirmations,omitempty" maxItems:"64" doc:"At most 64 observations of direct pongs, one per peer"`
}

// DirectConnectionHostEntry is a host's identity and endpoints as a device sees
// them.
type DirectConnectionHostEntry struct {
	Label        string                        `json:"label" doc:"The host's label; the API host is api"`
	NodeKey      string                        `json:"node_key" pattern:"^nodekey:[0-9a-f]{64}$"`
	DiscoKey     string                        `json:"disco_key" pattern:"^discokey:[0-9a-f]{64}$"`
	Endpoints    []string                      `json:"endpoints" doc:"Unicast UDP endpoints as host:port"`
	HomeRegion   int                           `json:"home_region" doc:"Always 0: the device builds the signal URL from its base URL"`
	Origin       string                        `json:"origin,omitempty" doc:"Never sent by this server"`
	Confirmation *DirectConnectionConfirmation `json:"confirmation,omitempty" doc:"The host's observation of the requesting device, when it has one"`
}

// DirectConnectionRegion names a signaling region. This server never sends one.
type DirectConnectionRegion struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
	URL  string `json:"url" format:"uri"`
}

// DirectConnectionNetmap is a device's lease on the hosts it can reach.
type DirectConnectionNetmap struct {
	Version        string                      `json:"version" doc:"Opaque content hash; send it back as the version query to hold the call until the netmap changes"`
	LeaseSeconds   int                         `json:"lease_seconds"`
	RefreshSeconds int                         `json:"refresh_seconds"`
	Regions        []DirectConnectionRegion    `json:"regions,omitempty" doc:"Never sent by this server"`
	Hosts          []DirectConnectionHostEntry `json:"hosts"`
}

type DirectConnectionRegistrationInput struct {
	Ticket string `header:"Silo-Signaling-Ticket" doc:"The enrollment ticket; a missing or invalid one is 401. Never log it"`
	requestCapture
}

type DirectConnectionNetmapInput struct {
	Ticket  string `header:"Silo-Signaling-Ticket" doc:"The enrollment ticket; a missing or invalid one is 401. Never log it"`
	Version string `query:"version" doc:"The version of the netmap the caller holds; an equal current version holds the call up to 25 seconds"`
}

type DirectConnectionNetmapOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         DirectConnectionNetmap
}

func registerDirectConnectionsSignaling(reg *Registry) {
	register := humaOp(http.MethodPost, Prefix+"/direct-connections/registration", "registerDirectConnectionPeer", "direct-connections", "Publish the calling engine's discovery key, endpoints and confirmations. Authenticated by the enrollment ticket.")
	register.DefaultStatus = http.StatusNoContent
	register.Errors = []int{http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusConflict, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: register, Class: ClassPublic, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent, RateLimitBucket: directSignalingBucket, MaxBodyBytes: directRegistrationMaxBytes, BodyMayLackMediaType: true},
		reg.registerDirectConnectionPeer)
	// The body is decoded by the protocol package after the ticket gate, with its
	// own size limit. The engine's client sends no Content-Type, so the media-type
	// guard lets that through (BodyMayLackMediaType); do not pre-read it through Huma.
	registeredOperation(reg.api.OpenAPI(), Operation{Operation: register}).RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
		mediaTypeJSON: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[DirectConnectionRegistration](), true, "")},
	}}

	netmap := humaOp(http.MethodGet, Prefix+"/direct-connections/netmap", "getDirectConnectionNetmap", "direct-connections", "Read the hosts this device can reach, holding the call while the netmap is unchanged. Authenticated by the enrollment ticket.")
	netmap.Errors = []int{http.StatusUnauthorized, http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests, http.StatusServiceUnavailable}
	Register(reg, Operation{Operation: netmap, Class: ClassPublic, ServiceBacked: true, RateLimitBucket: directSignalingBucket}, reg.getDirectConnectionNetmap)

	registerDirectConnectionSignal(reg)
}

// registerDirectConnectionSignal registers the signaling stream as a raw
// WebSocket handshake. The stream is not rate limited per request: the region's
// envelope bounds its traffic.
func registerDirectConnectionSignal(reg *Registry) {
	responses := socketResponses([]string{"400", "401", "409", "500", "503"}, "Handshake refused.", "Signaling stream established.", adminLogsHandshakeHeaderDoc)
	responses["101"].Headers[eventsProtocolHeader].Description = "The selected subprotocol, always " + directSignalProtocol + "."
	// The gate's refusals are Problem Details; the 400 comes from the subprotocol
	// check and the WebSocket library and stays plain text.
	for _, status := range []string{"401", "409", "500", "503"} {
		responses[status].Content = map[string]*huma.MediaType{problemContentType: {Schema: reg.api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[Problem](), true, "")}}
	}
	raw := Operation{Operation: huma.Operation{Method: http.MethodGet, Path: Prefix + "/direct-connections/signal", OperationID: "connectDirectConnectionSignal", Tags: []string{"direct-connections"},
		Summary: "Open the calling engine's signaling stream. Authenticated by the enrollment ticket; the stream carries DERP frames as binary messages.", Responses: responses}, Class: ClassPublic, ServiceBacked: true}
	raw.Parameters = []*huma.Param{
		{Name: directconn.SignalingTicketHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "The enrollment ticket; a missing or invalid one is 401 before any upgrade. Never log it."},
		{Name: eventsProtocolHeader, In: paramInHeader, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Offer " + directSignalProtocol + "."},
	}
	RegisterRaw(reg, RawOperation{Operation: raw, Protocol: eventsRawProtocol, Reason: "Enrollment ticket and entry checks precede the upgrade; the stream closes at the ticket's expiry or when the entry is revoked."},
		socketHandler(http.HandlerFunc(reg.connectDirectConnectionSignal), "direct signaling unavailable"))
}

// directSignalingGate runs before any work: the ticket must verify and name a
// live entry. It then refuses when direct connections are off, when this
// replica does not run the engine, and, while the engine runs but the API host
// is unavailable, for a device that holds no live registration.
func (reg *Registry) directSignalingGate(ctx context.Context, ticket string) (server.Principal, *Problem) {
	svc := reg.deps.DirectCoordination
	if svc == nil {
		return server.Principal{}, unavailable("direct connections")
	}
	principal, device, err := svc.Authorize(ctx, ticket)
	if err != nil {
		return server.Principal{}, directSignalingProblem(err)
	}
	value, err := svc.Capability(ctx)
	if err != nil {
		return server.Principal{}, directCoordinationProblem(err)
	}
	if !value.Enabled {
		return server.Principal{}, CapabilityProblem(StateDisabled, "direct connections")
	}
	if !value.Host.Available {
		// A replica whose engine runs while the replica gate refuses new
		// connections keeps serving the devices it already serves, so their
		// established sessions continue; every other device waits for the gate.
		switch {
		case !value.Host.EngineRunning:
			return server.Principal{}, NewProblem(TypeDependencyUnavailable, "This server replica does not run the direct connection engine; try again shortly.").WithRetryAfter(directRetryAfterSeconds)
		case !svc.Established(device):
			return server.Principal{}, NewProblem(TypeDependencyUnavailable, "This server replica is not accepting new direct connections; try again shortly.").WithRetryAfter(directRetryAfterSeconds)
		}
	}
	return principal, nil
}

// connectDirectConnectionSignal checks the ticket, then upgrades to the derp
// subprotocol and hands the stream to the region, which closes it at the
// ticket's expiry or when the entry is revoked.
func (reg *Registry) connectDirectConnectionSignal(w http.ResponseWriter, r *http.Request) {
	principal, p := reg.directSignalingGate(r.Context(), r.Header.Get(directconn.SignalingTicketHeader))
	if p != nil {
		writeProblem(w, r, p)
		return
	}
	if !offersSubprotocol(r.Header, directSignalProtocol) {
		http.Error(w, "required subprotocol missing", http.StatusBadRequest)
		return
	}
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{directSignalProtocol}})
	if err != nil {
		return // Accept has already answered the request.
	}
	conn := &directSignalConn{Conn: wsconn.NetConn(r.Context(), socket, websocket.MessageBinary, r.RemoteAddr), socket: socket}
	// The region ends the stream and reports why; either way the request is over.
	_ = reg.deps.DirectCoordination.ServeRegion(r.Context(), conn, principal)
}

// directSignalConn closes the socket at once instead of waiting out a close
// handshake, so a revoked device is cut off without delay.
type directSignalConn struct {
	net.Conn
	socket *websocket.Conn
}

func (c *directSignalConn) Close() error { return c.socket.CloseNow() }

// offersSubprotocol reports whether the client's Sec-WebSocket-Protocol header
// lists name.
func offersSubprotocol(h http.Header, name string) bool {
	for _, line := range h.Values(eventsProtocolHeader) {
		for _, offered := range strings.Split(line, ",") {
			if strings.TrimSpace(offered) == name {
				return true
			}
		}
	}
	return false
}

func (reg *Registry) registerDirectConnectionPeer(ctx context.Context, in *DirectConnectionRegistrationInput) (*struct{}, error) {
	principal, p := reg.directSignalingGate(ctx, in.Ticket)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.DirectCoordination.Register(ctx, principal, in.request.Body); err != nil {
		return nil, directSignalingProblem(err)
	}
	return nil, nil
}

func (reg *Registry) getDirectConnectionNetmap(ctx context.Context, in *DirectConnectionNetmapInput) (*DirectConnectionNetmapOutput, error) {
	principal, p := reg.directSignalingGate(ctx, in.Ticket)
	if p != nil {
		return nil, p
	}
	m, err := reg.deps.DirectCoordination.Netmap(ctx, principal, in.Version)
	if err != nil {
		return nil, directSignalingProblem(err)
	}
	return &DirectConnectionNetmapOutput{CacheControl: "no-store", Body: directNetmapBody(m)}, nil
}

// directSignalingProblem maps a ticket, body or store failure of registration
// and netmap to its problem. Every ticket and entry refusal is the same 401, so a
// caller cannot tell which check failed.
func directSignalingProblem(err error) *Problem {
	switch {
	case errors.Is(err, directconn.ErrInvalidTicket), errors.Is(err, server.ErrUnauthorized):
		return NewProblem(TypeInvalidToken, "The signaling ticket is missing, expired or no longer valid; enroll again.")
	case errors.Is(err, protocol.ErrTooLarge):
		return NewProblem(TypeForStatus(http.StatusRequestEntityTooLarge), "The registration is larger than 64 KiB.")
	case errors.Is(err, protocol.ErrInvalid):
		return NewProblem(TypeValidationFailed, "The registration did not pass validation.")
	case errors.Is(err, server.ErrUnavailable):
		// A store timeout wraps its deadline error, so this precedes the timeouts below.
		return directCoordinationProblem(err)
	case errors.Is(err, os.ErrDeadlineExceeded):
		return NewProblem(TypeRequestTimeout, "The request body was not received in time.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return NewProblem(TypeRequestTimeout, "The request ended before the netmap was ready.")
	}
	return directCoordinationProblem(err)
}

func directConfirmationBody(c *protocol.Confirmation) *DirectConnectionConfirmation {
	if c == nil {
		return nil
	}
	return &DirectConnectionConfirmation{Peer: c.Peer.String(), DiscoKey: c.DiscoKey.String(), Endpoint: c.Endpoint.String()}
}

func directNetmapBody(m protocol.Netmap) DirectConnectionNetmap {
	out := DirectConnectionNetmap{Version: m.Version, LeaseSeconds: m.LeaseSeconds, RefreshSeconds: m.RefreshSeconds, Hosts: make([]DirectConnectionHostEntry, 0, len(m.Hosts))}
	for _, r := range m.Regions {
		out.Regions = append(out.Regions, DirectConnectionRegion{ID: r.ID, Code: r.Code, URL: r.URL})
	}
	for _, h := range m.Hosts {
		entry := DirectConnectionHostEntry{Label: h.Label, NodeKey: h.NodeKey.String(), DiscoKey: h.DiscoKey.String(), Endpoints: make([]string, 0, len(h.Endpoints)),
			HomeRegion: h.HomeRegion, Origin: h.Origin, Confirmation: directConfirmationBody(h.Confirmation)}
		for _, e := range h.Endpoints {
			entry.Endpoints = append(entry.Endpoints, e.String())
		}
		out.Hosts = append(out.Hosts, entry)
	}
	return out
}
