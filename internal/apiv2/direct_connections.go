package apiv2

import (
	"context"
	"net/http"

	"github.com/Silo-Server/silo-server/internal/directconn"
)

// DirectConnectionsService reports the API host and optionally refreshes it.
type DirectConnectionsService interface {
	Status(context.Context, bool) (directconn.Status, error)
}

// DirectConnectionsStatus is the administrator-only readiness report.
type DirectConnectionsStatus struct {
	Enabled bool                          `json:"enabled"`
	Hosts   []DirectConnectionsHostStatus `json:"hosts"`
}

// DirectConnectionsHostStatus contains only observations from the named host.
type DirectConnectionsHostStatus struct {
	Host      NetworkAccessHostRef        `json:"host"`
	CheckedAt Instant                     `json:"checked_at"`
	Features  []DirectConnectionsFeature  `json:"features"`
	Listeners []DirectConnectionsListener `json:"listeners,omitempty"`
	Knobs     []DirectConnectionsKnob     `json:"knobs"`
}

// DirectConnectionsFeature uses the shared service and log feature vocabulary.
type DirectConnectionsFeature directconn.Feature

// DirectConnectionsListener reports a bound TCP listener.
type DirectConnectionsListener struct {
	Listener   string                      `json:"listener" enum:"api,jellyfin,abs,proxy"`
	Requested  string                      `json:"requested"`
	Granted    string                      `json:"granted"`
	State      string                      `json:"state"`
	Reason     string                      `json:"reason"`
	Errno      string                      `json:"errno,omitempty"`
	SendBuffer DirectConnectionsSendBuffer `json:"send_buffer"`
}

// DirectConnectionsSendBuffer reports a TCP socket's requested and usable granted bytes.
type DirectConnectionsSendBuffer directconn.SendBuffer

// DirectConnectionsKnob reports a process startup tuning value.
type DirectConnectionsKnob directconn.Knob

type DirectConnectionsStatusInput struct {
	Refresh bool `query:"refresh" default:"false" doc:"Run a new check, or wait for the in-flight check. The settings read and the check have separate fifteen-second bounds, so a request can take about thirty seconds"`
}

type DirectConnectionsStatusOutput struct{ Body DirectConnectionsStatus }

// DirectConnectionsCapabilities describes the readiness operations supported
// by this build. State reports service wiring, not probe health or enablement.
type DirectConnectionsCapabilities struct {
	Capability
	Status  bool `json:"status"`
	Refresh bool `json:"refresh"`
}

type DirectConnectionsCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         DirectConnectionsCapabilities
}

func registerDirectConnections(reg *Registry) {
	Register(reg, Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/direct-connections/capabilities", "getAdminDirectConnectionsCapabilities", "direct-connections", "Discover direct-connection readiness operations and service configuration, independently of probe health and the hidden setting."), Class: ClassActingAdmin, ServiceBacked: true}, func(context.Context, *CapabilityInput) (*DirectConnectionsCapabilitiesOutput, error) {
		return &DirectConnectionsCapabilitiesOutput{Body: DirectConnectionsCapabilities{
			Capability: Capability{State: configuredCapabilityState(reg.deps.DirectConnections != nil)},
			Status:     true, Refresh: true,
		}}, nil
	})
	op := Operation{Operation: humaOp(http.MethodGet, Prefix+"/admin/direct-connections/status", "getAdminDirectConnectionsStatus", "direct-connections", "Read the API host's direct-connection readiness. Network probes run only when the hidden setting is enabled. Refresh joins an in-flight check or starts a new one. The settings read and the check have separate fifteen-second bounds, so a request can take about thirty seconds."), Class: ClassActingAdmin, ServiceBacked: true}
	Register(reg, op, func(ctx context.Context, in *DirectConnectionsStatusInput) (*DirectConnectionsStatusOutput, error) {
		if reg.deps.DirectConnections == nil {
			return nil, unavailable("direct connections")
		}
		status, err := reg.deps.DirectConnections.Status(ctx, in.Refresh)
		if err != nil {
			if ctx.Err() != nil {
				return nil, serviceProblem(err)
			}
			// The service fails only when it cannot read settings in time, or while
			// it shuts down: a temporary dependency failure.
			return nil, unavailable("direct connections")
		}
		features := make([]DirectConnectionsFeature, len(status.Features))
		for i, f := range status.Features {
			features[i] = DirectConnectionsFeature(f)
		}
		listeners := make([]DirectConnectionsListener, len(status.Listeners))
		for i, l := range status.Listeners {
			listeners[i] = DirectConnectionsListener{Listener: l.Listener, Requested: l.Requested, Granted: l.Granted, State: l.State, Reason: l.Reason, Errno: l.Errno, SendBuffer: DirectConnectionsSendBuffer(l.SendBuffer)}
		}
		knobs := make([]DirectConnectionsKnob, len(status.Knobs))
		for i, k := range status.Knobs {
			knobs[i] = DirectConnectionsKnob(k)
		}
		return &DirectConnectionsStatusOutput{Body: DirectConnectionsStatus{Enabled: status.Enabled, Hosts: []DirectConnectionsHostStatus{{Host: NetworkAccessHostRef{ID: "api", Role: "api", Name: "API"}, CheckedAt: NewInstant(status.CheckedAt), Features: features, Listeners: listeners, Knobs: knobs}}}}, nil
	})
}
