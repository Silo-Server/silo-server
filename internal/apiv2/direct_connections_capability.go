package apiv2

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/darthShadow/shunpike/coordination/protocol"
	"github.com/darthShadow/shunpike/coordination/server"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

// directCoordinationURL is the base URL the engine appends registration,
// netmap and signal to. A value starting with "/" is appended to the client's
// saved base URL.
const directCoordinationURL = Prefix + "/direct-connections"

// directRetryAfterSeconds is the retry hint, in seconds, of the 503 answers of
// the direct-connection operations.
const directRetryAfterSeconds = 5

// DirectCoordinationService describes direct connections to clients and enrolls
// their devices.
type DirectCoordinationService interface {
	Capability(context.Context) (directconn.Capability, error)
	Enroll(context.Context, directconn.EnrollRequest) (directconn.Enrollment, error)
	Withdraw(ctx context.Context, userID int, deviceID string) error
	// Authorize checks a signaling ticket and the entry it names.
	Authorize(ctx context.Context, ticket string) (server.Principal, server.Device, error)
	// Established reports whether an entry Authorize returned holds a live
	// registration, so the engine is serving its device now.
	Established(server.Device) bool
	// Register stores the registration read from body for the principal's device.
	Register(ctx context.Context, principal server.Principal, body io.Reader) error
	// Netmap returns the device's view of the hosts, holding while it is unchanged.
	Netmap(ctx context.Context, principal server.Principal, version string) (protocol.Netmap, error)
	// ServeRegion serves the principal's signaling stream until it ends, the
	// principal expires or the entry is revoked, and closes conn.
	ServeRegion(ctx context.Context, conn net.Conn, principal server.Principal) error
}

// DirectConnectionsHost is one host a client can reach directly.
type DirectConnectionsHost struct {
	HostID  string `json:"host_id" example:"api"`
	NodeKey string `json:"node_key" pattern:"^nodekey:[0-9a-f]{64}$" doc:"The host's node key; the engine dials it"`
	State   string `json:"state" enum:"available,unavailable,draining" doc:"Whether the host can take direct connections now. A client treats an unknown state as unavailable"`
}

// DirectConnectionsCapability tells a client whether and how to use direct
// connections. state is disabled when an administrator turned them off.
type DirectConnectionsCapability struct {
	Capability
	ProtocolRevision string                  `json:"protocol_revision" doc:"Opaque revision of the direct protocol between a client engine and the server. Compare it for exact equality with each revision the client supports; a client that does not know it does not enroll" example:"1"`
	CoordinationURL  string                  `json:"coordination_url" format:"uri-reference" doc:"Base URL of the coordination operations. A value that starts with / is appended to the saved base URL, keeping its path; an absolute https URL is used as is" example:"/api/v2/direct-connections"`
	TLSPin           string                  `json:"tls_pin" doc:"Base64 SHA-256 of the SubjectPublicKeyInfo the direct path and the local route present. It authenticates those two only, never coordination_url"`
	STUNServers      []string                `json:"stun_servers" doc:"STUN servers as host:port"`
	ACKDecimation    *bool                   `json:"ack_decimation,omitempty" doc:"true asks the client, which receives the media, to send fewer QUIC ACKs for application data on its direct connections to this server; false asks for the standard RFC 9000 rate. A local receiver setting that nothing negotiates; a client whose QUIC stack cannot change its ACK rate ignores it. Absent: keep the client default"`
	ReceiveWindowMiB *int                    `json:"receive_window_mib,omitempty" minimum:"4" maximum:"128" doc:"Largest flow-control receive window in MiB (1,048,576 bytes) the client grants to one QUIC stream on its direct connections to this server. It sets neither the initial stream window nor the connection window. A client that cannot set a stream window ignores it. Absent: keep the client default"`
	Hosts            []DirectConnectionsHost `json:"hosts" doc:"The hosts reachable directly; empty when the capability is disabled"`
}

type DirectConnectionsCapabilityOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         DirectConnectionsCapability
}

// DirectConnectionEnrollmentRequest names the node key a device's engine uses.
type DirectConnectionEnrollmentRequest struct {
	NodeKey        string `json:"node_key" pattern:"^nodekey:[0-9a-f]{64}$" doc:"The engine's public node key"`
	ConnectionPath string `json:"connection_path" enum:"automatic,direct_only" doc:"The device's playback.connection_path setting"`
}

type DirectConnectionEnrollmentInput struct {
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128" doc:"The client's stable device identifier" example:"iphone-1"`
	Body     DirectConnectionEnrollmentRequest
}

// DirectConnectionEnrollment carries the ticket the engine presents on every
// coordination request until the lease ends. Renew at half of expires_at - now.
type DirectConnectionEnrollment struct {
	Ticket          string  `json:"ticket" doc:"Secret credential for the coordination operations; send it in the Silo-Signaling-Ticket header and never log it"`
	ExpiresAt       Instant `json:"expires_at" doc:"End of the enrollment lease and of the ticket"`
	CoordinationURL string  `json:"coordination_url" format:"uri-reference" example:"/api/v2/direct-connections"`
}

type DirectConnectionEnrollmentOutput struct{ Body DirectConnectionEnrollment }

type DirectConnectionWithdrawInput struct {
	DeviceID string `header:"X-Silo-Device-Id" required:"true" minLength:"1" maxLength:"128" doc:"The client's stable device identifier" example:"iphone-1"`
}

func registerDirectConnectionsCoordination(reg *Registry) {
	registerDirectConnectionsSignaling(reg)
	capability := humaOp(http.MethodGet, Prefix+"/capabilities/direct-connections", "getDirectConnectionsCapability", "direct-connections", "Discover whether this server offers direct connections to the calling client, and the values its engine needs.")
	Register(reg, Operation{Operation: capability, Class: ClassProfileScoped, ServiceBacked: true}, reg.getDirectConnectionsCapability)

	enroll := humaOp(http.MethodPost, Prefix+"/direct-connections/enrollment", "enrollDirectConnectionDevice", "direct-connections", "Enroll the calling device's engine, or renew its enrollment, and return the ticket for the coordination operations. Needs a login session; an API key or a View as user session is refused.")
	enroll.Description = "Enrolling the same device again with the same node key renews the lease. A new node key replaces the entry and ends the old ticket. 409 direct_device_limit means the account holds the most devices it may; show the limit. 409 direct_node_key_conflict means another device holds the node key; make a new key and enroll again. 409 capability_disabled means an administrator turned direct connections off."
	enroll.Errors = append(enroll.Errors, http.StatusConflict)
	Register(reg, Operation{Operation: enroll, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent, Audited: true}, reg.enrollDirectConnectionDevice)

	withdraw := humaOp(http.MethodDelete, Prefix+"/direct-connections/enrollment", "withdrawDirectConnectionDevice", "direct-connections", "Withdraw the calling device's enrollment. Withdrawing a device that is not enrolled succeeds.")
	withdraw.DefaultStatus = http.StatusNoContent
	Register(reg, Operation{Operation: withdraw, Class: ClassProfileScoped, ServiceBacked: true, RetrySafety: RetrySafetyNaturalIdempotent}, reg.withdrawDirectConnectionDevice)
}

func (reg *Registry) getDirectConnectionsCapability(ctx context.Context, _ *CapabilityInput) (*DirectConnectionsCapabilityOutput, error) {
	doc := DirectConnectionsCapability{
		Capability:  Capability{Allowed: new(directEnrollAllowed(ctx))},
		STUNServers: []string{}, Hosts: []DirectConnectionsHost{},
	}
	if reg.deps.DirectCoordination == nil {
		doc.State = StateNotConfigured
		return &DirectConnectionsCapabilityOutput{Body: doc}, nil
	}
	value, err := reg.deps.DirectCoordination.Capability(ctx)
	if err != nil {
		return nil, directCoordinationProblem(err)
	}
	doc.State = enabledCapabilityState(value.Enabled)
	doc.ProtocolRevision, doc.CoordinationURL, doc.TLSPin = value.ProtocolRevision, directCoordinationURL, value.TLSPin
	doc.ACKDecimation, doc.ReceiveWindowMiB = new(value.ACKDecimation), new(value.ReceiveWindowMiB)
	if value.Enabled {
		doc.STUNServers = append(doc.STUNServers, value.STUNServers...)
		state := "unavailable"
		switch {
		case value.Host.Available && value.Host.Draining:
			state = "draining"
		case value.Host.Available:
			state = "available"
		}
		doc.Hosts = append(doc.Hosts, DirectConnectionsHost{HostID: directconn.CapabilityHostID, NodeKey: value.Host.NodeKey.String(), State: state})
	}
	return &DirectConnectionsCapabilityOutput{Body: doc}, nil
}

func (reg *Registry) enrollDirectConnectionDevice(ctx context.Context, in *DirectConnectionEnrollmentInput) (*DirectConnectionEnrollmentOutput, error) {
	svc := reg.deps.DirectCoordination
	if svc == nil {
		return nil, unavailable("direct connections")
	}
	if !capabilityLoginAllowed(ctx) {
		return nil, NewProblem(TypePermissionDenied, "Direct connections need a signed-in session; an API key cannot enroll a device.")
	}
	if !directEnrollAllowed(ctx) {
		return nil, NewProblem(TypePermissionDenied, "A View as user session cannot enroll a device for direct connections.")
	}
	user, profile, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	value, err := svc.Capability(ctx)
	if err != nil {
		return nil, directCoordinationProblem(err)
	}
	if !value.Enabled {
		return nil, CapabilityProblem(StateDisabled, "direct connections")
	}
	var key protocol.NodeKey
	if err := key.UnmarshalText([]byte(in.Body.NodeKey)); err != nil || key.IsZero() {
		return nil, validationProblem("body.node_key", codeInvalid, "expected nodekey: and 64 lowercase hex digits for a nonzero key")
	}
	// capabilityLoginAllowed has checked that the claims carry a login session.
	enrollment, err := svc.Enroll(ctx, directconn.EnrollRequest{UserID: user, ProfileID: profile, SessionID: claimsFrom(ctx).SessionID, DeviceID: in.DeviceID, NodeKey: key, ConnectionPath: in.Body.ConnectionPath})
	if err != nil {
		return nil, directCoordinationProblem(err)
	}
	return &DirectConnectionEnrollmentOutput{Body: DirectConnectionEnrollment{Ticket: enrollment.Ticket, ExpiresAt: NewInstant(enrollment.ExpiresAt), CoordinationURL: directCoordinationURL}}, nil
}

// directEnrollAllowed reports whether the caller may enroll or renew a device:
// a bounded login session that is not a View as user. A View as user session's
// entry would name the viewed account, and several revocations that end such a
// session withdraw none of the viewed account's entries, so no View as user
// session may own one.
func directEnrollAllowed(ctx context.Context) bool {
	return capabilityLoginAllowed(ctx) && claimsFrom(ctx).ImpersonatorUserID == nil
}

func (reg *Registry) withdrawDirectConnectionDevice(ctx context.Context, in *DirectConnectionWithdrawInput) (*struct{}, error) {
	if reg.deps.DirectCoordination == nil {
		return nil, unavailable("direct connections")
	}
	user, _, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := reg.deps.DirectCoordination.Withdraw(ctx, user, in.DeviceID); err != nil {
		return nil, directCoordinationProblem(err)
	}
	return nil, nil
}

// directCoordinationProblem maps the coordination store's refusals to their
// problems. Anything else is an internal error with no detail leaked.
func directCoordinationProblem(err error) *Problem {
	switch {
	case errors.Is(err, server.ErrDeviceLimit):
		return NewProblem(TypeDirectDeviceLimit, "This account has reached the limit of devices that can use direct connections; remove one first.")
	case errors.Is(err, server.ErrNodeKeyConflict):
		return NewProblem(TypeDirectNodeKeyConflict, "Another device holds this node key; make a new one and enroll again.")
	case errors.Is(err, server.ErrUnavailable):
		return NewProblem(TypeDependencyUnavailable, "The direct-connection store is unavailable; try again shortly.").WithRetryAfter(directRetryAfterSeconds)
	case errors.Is(err, directconn.ErrSessionEnded):
		return NewProblem(TypeSessionExpired, "This login session has ended.")
	case errors.Is(err, directconn.ErrDraining):
		return NewProblem(TypeDependencyUnavailable, "This server is shutting down; enroll again shortly.").WithRetryAfter(directRetryAfterSeconds)
	case errors.Is(err, directconn.ErrSettingsNotRead):
		return NewProblem(TypeDependencyUnavailable, "Direct-connection settings are not read yet; try again shortly.").WithRetryAfter(directRetryAfterSeconds)
	}
	return NewProblem(TypeInternalError, "An unexpected error occurred.")
}

func (c DirectConnectionsCapability) capabilityState() string { return c.State }
