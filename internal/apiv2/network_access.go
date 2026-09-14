package apiv2

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// NetworkAccessService is the slice of *plugins.Service the network access
// operations use: provider discovery from manifests, and per-host status,
// connect and disconnect through the resident provider plugin.
type NetworkAccessService interface {
	ListNetworkAccessProviders(context.Context) ([]plugins.NetworkAccessProvider, error)
	NetworkAccessStatus(ctx context.Context, provider string) (plugins.NetworkAccessReport, error)
	ConnectNetworkAccess(ctx context.Context, provider string, hosts []string) (plugins.NetworkAccessReport, error)
	DisconnectNetworkAccess(ctx context.Context, provider string, hosts []string) (plugins.NetworkAccessReport, error)
}

// NetworkAccessProviderSummary is one installed provider as the capability
// document lists it, read from the plugin manifest without launching it.
type NetworkAccessProviderSummary struct {
	Provider       string `json:"provider" doc:"Stable provider slug used in the admin routes, e.g. tailscale"`
	DisplayName    string `json:"display_name"`
	InstallationID ID     `json:"installation_id" doc:"Plugin installation declaring network_access_provider.v1"`
}

// NetworkAccessCapabilities is the server-wide capability document: available
// when at least one enabled installation declares a provider, not_configured
// otherwise. Provider health is not state; read it from the admin status.
type NetworkAccessCapabilities struct {
	Capability
	Providers []NetworkAccessProviderSummary `json:"providers"`
}
type NetworkAccessCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         NetworkAccessCapabilities
}

func (c NetworkAccessCapabilities) capabilityState() string {
	return configuredCapabilityState(len(c.Providers) > 0)
}

// NetworkAccessHostRef identifies one process running the provider.
type NetworkAccessHostRef struct {
	ID   string `json:"id" doc:"api for the API server; node:<id> for a proxy node"`
	Role string `json:"role" enum:"api,proxy"`
	Name string `json:"name" doc:"Server name for the API host; node name for a proxy"`
}

// NetworkAccessHostStatus is the provider's state on one host.
type NetworkAccessHostStatus struct {
	Host            NetworkAccessHostRef `json:"host"`
	State           string               `json:"state" enum:"disconnected,awaiting_authorization,connecting,connected,error,unavailable" doc:"Published host state; provider states outside this vocabulary map to error"`
	RawState        string               `json:"raw_state,omitempty" doc:"the provider's own state string when it is not one of the published states"`
	Hostname        string               `json:"hostname,omitempty" doc:"Overlay DNS name"`
	Origin          string               `json:"origin,omitempty" doc:"scheme://host[:port] clients reach the API listener at over the overlay"`
	Addresses       []string             `json:"addresses" doc:"Overlay IP addresses"`
	AuthURL         string               `json:"auth_url,omitempty" doc:"Interactive enrollment URL, only while awaiting_authorization; admin-only, never logged"`
	Error           string               `json:"error,omitempty"`
	ProviderVersion string               `json:"provider_version,omitempty"`
	UpdatedAt       *Instant             `json:"updated_at,omitempty" doc:"When the host last heard from the provider; absent while unavailable"`
}

// NetworkAccessStatus is the provider's state across every host.
type NetworkAccessStatus struct {
	Provider string                    `json:"provider"`
	Hosts    []NetworkAccessHostStatus `json:"hosts"`
}
type NetworkAccessStatusOutput struct{ Body NetworkAccessStatus }

type NetworkAccessProviderInput struct {
	Provider string `path:"provider" minLength:"1" maxLength:"64" pattern:"^[a-z0-9]+(?:[._-][a-z0-9]+)*$"`
}

// NetworkAccessCommand selects the hosts a connect or disconnect targets.
type NetworkAccessCommand struct {
	Hosts []string `json:"hosts,omitempty" maxItems:"256" doc:"Host ids to act on (api, node:<id>); omitted means every host"`
	// hostsNull records an explicit {"hosts": null}. The decoder leaves Hosts
	// nil for null and for omission alike, and omission means every host; a
	// null must not widen a malformed request to the whole deployment. The
	// body is optional, so the RawBody route other operations use for this
	// rule would make it required in the document.
	hostsNull bool
}

func (c *NetworkAccessCommand) UnmarshalJSON(data []byte) error {
	type plain NetworkAccessCommand
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	*c = NetworkAccessCommand(decoded)
	c.hostsNull = bytes.Equal(bytes.TrimSpace(members["hosts"]), jsonNull)
	return nil
}

type NetworkAccessCommandInput struct {
	NetworkAccessProviderInput
	Body *NetworkAccessCommand `required:"false"`
	// bodyPresent records that the request carried a body at all. Huma leaves
	// Body nil both for an omitted body and for the literal document `null`,
	// and only omission means "every host"; a null body is malformed and
	// must not widen to deployment-wide fan-out.
	bodyPresent bool
}

// Resolve notes whether a body was sent. The body itself is still decoded
// by Huma; this only distinguishes an absent body from a null one.
func (in *NetworkAccessCommandInput) Resolve(ctx huma.Context) []error {
	r, _ := humachi.Unwrap(ctx)
	in.bodyPresent = r.ContentLength != 0
	return nil
}

// publishedNetworkAccessStates is the closed enum on NetworkAccessHostStatus.State.
// The plugin vocabulary is open, so anything else is coerced to error with the
// original value kept in raw_state.
var publishedNetworkAccessStates = map[string]bool{
	netaccess.StateDisconnected:          true,
	netaccess.StateAwaitingAuthorization: true,
	netaccess.StateConnecting:            true,
	netaccess.StateConnected:             true,
	netaccess.StateError:                 true,
	netaccess.StateUnavailable:           true,
}

func networkAccessStatusOf(report plugins.NetworkAccessReport) NetworkAccessStatus {
	out := NetworkAccessStatus{Provider: report.Provider.Provider, Hosts: make([]NetworkAccessHostStatus, 0, len(report.Hosts))}
	for _, host := range report.Hosts {
		status := host.Status
		entry := NetworkAccessHostStatus{
			Host:            NetworkAccessHostRef{ID: host.Host.ID, Role: host.Host.Role, Name: host.Host.Name},
			State:           status.State,
			Hostname:        status.Hostname,
			Origin:          status.Origin,
			Addresses:       append([]string{}, status.Addresses...),
			AuthURL:         status.AuthURL,
			Error:           status.Error,
			ProviderVersion: status.ProviderVersion,
		}
		if entry.State == "" {
			entry.State = netaccess.StateUnavailable
		} else if !publishedNetworkAccessStates[entry.State] {
			entry.RawState = entry.State
			entry.State = netaccess.StateError
		}
		if !status.UpdatedAt.IsZero() {
			entry.UpdatedAt = ptr(NewInstant(status.UpdatedAt))
		}
		out.Hosts = append(out.Hosts, entry)
	}
	return out
}

func networkAccessProblem(err error) error {
	switch {
	case errors.Is(err, plugins.ErrNetworkAccessProviderNotFound):
		return NewProblem(TypeNotFound, "Network access provider not found.")
	case errors.Is(err, plugins.ErrNetworkAccessHostUnknown):
		return validationProblem(locationBody+".hosts", codeInvalid, "Unknown network access host.")
	}
	return serviceProblem(err)
}

func registerNetworkAccess(reg *Registry) {
	capability := Operation{Operation: humaOp(http.MethodGet, Prefix+"/network-access/capabilities", "getNetworkAccessCapabilities", "network-access",
		"List the installed network access providers (overlay networks such as Tailscale that reach this server without port forwarding). available when an enabled plugin declares one; not_configured otherwise. Read from plugin manifests; no plugin is launched and no health is implied."), Class: ClassAuthenticated, ServiceBacked: true}
	Register(reg, capability, func(ctx context.Context, _ *CapabilityInput) (*NetworkAccessCapabilitiesOutput, error) {
		out := NetworkAccessCapabilities{Providers: []NetworkAccessProviderSummary{}}
		if reg.deps.NetworkAccess == nil {
			out.State = StateNotConfigured
			return &NetworkAccessCapabilitiesOutput{Body: out}, nil
		}
		providers, err := reg.deps.NetworkAccess.ListNetworkAccessProviders(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		for _, provider := range providers {
			out.Providers = append(out.Providers, NetworkAccessProviderSummary{Provider: provider.Provider, DisplayName: provider.DisplayName, InstallationID: IDFromInt(int64(provider.InstallationID))})
		}
		return &NetworkAccessCapabilitiesOutput{Body: out}, nil
	})

	admin := func(method, path, id, summary string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+"/admin/network-access/{provider}"+path, id, "network-access", summary), Class: ClassActingAdmin, ServiceBacked: true}
		o.Errors = append(o.Errors, http.StatusNotFound)
		return o
	}
	Register(reg, admin(http.MethodGet, "/status", "getAdminNetworkAccessStatus",
		"Read the provider's live state on every host that runs it, asking each plugin instance directly with a ten-second timeout. A host whose plugin process is not running answers state unavailable with the supervisor's last error. An unknown provider slug is 404."),
		func(ctx context.Context, in *NetworkAccessProviderInput) (*NetworkAccessStatusOutput, error) {
			if reg.deps.NetworkAccess == nil {
				return nil, unavailable("network access")
			}
			report, err := reg.deps.NetworkAccess.NetworkAccessStatus(ctx, in.Provider)
			if err != nil {
				return nil, networkAccessProblem(err)
			}
			return &NetworkAccessStatusOutput{Body: networkAccessStatusOf(report)}, nil
		})
	command := func(path, id, summary string, run func(NetworkAccessService, context.Context, string, []string) (plugins.NetworkAccessReport, error)) {
		op := admin(http.MethodPost, path, id, summary)
		op.DefaultStatus = http.StatusAccepted
		op.DemoRestricted = true
		op.RetrySafety = RetrySafetyNaturalIdempotent
		op.MaxBodyBytes = 64 << 10
		Register(reg, op, func(ctx context.Context, in *NetworkAccessCommandInput) (*NetworkAccessStatusOutput, error) {
			if reg.deps.NetworkAccess == nil {
				return nil, unavailable("network access")
			}
			if in.Body == nil && in.bodyPresent {
				return nil, validationProblem(locationBody, codeInvalidType, "null is not a request body; send an object or omit the body to act on every host.")
			}
			if in.Body != nil && in.Body.hostsNull {
				return nil, validationProblem(locationBody+".hosts", codeInvalidType, "null is not a value for this member; omit it to act on every host.")
			}
			var hosts []string
			if in.Body != nil && in.Body.Hosts != nil {
				hosts = in.Body.Hosts
				if len(hosts) == 0 {
					return nil, validationProblem(locationBody+".hosts", codeInvalid, "Name at least one host or omit hosts to act on every host.")
				}
			}
			report, err := run(reg.deps.NetworkAccess, ctx, in.Provider, hosts)
			if err != nil {
				return nil, networkAccessProblem(err)
			}
			return &NetworkAccessStatusOutput{Body: networkAccessStatusOf(report)}, nil
		})
	}
	command("/connect", "connectNetworkAccess",
		"Ask the provider on the named hosts (every host when hosts is omitted) to bring its overlay identity up and start proxying. Answers 202 with the state each host reached within ten seconds; enrollment may continue in the background (awaiting_authorization carries the auth_url), so poll status for the final state. Repeating the request converges on one connected instance per host. Hosts not named answer their current status; an unknown host id is 422.",
		NetworkAccessService.ConnectNetworkAccess)
	command("/disconnect", "disconnectNetworkAccess",
		"Ask the provider on the named hosts (every host when hosts is omitted) to tear its overlay listener down and clear its desired-connected intent. Answers 202 with the state each host reached within ten seconds. Repeating the request converges on disconnected. Hosts not named answer their current status; an unknown host id is 422.",
		NetworkAccessService.DisconnectNetworkAccess)
}
