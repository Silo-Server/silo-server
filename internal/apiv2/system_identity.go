package apiv2

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/serveridentity"
)

// Server identity and connection discovery. A phone and a TV can reach one
// deployment through different addresses (public URL, LAN, an overlay
// network fronted by a network access provider); the identity document lets a
// client recognize the deployment behind any of them, and the connections
// document tells a signed-in client which addresses the deployment offers so
// it can hand a reachable one to a device that cannot use its own.
//
// The identity alone authorizes nothing: it is public and any host can echo
// one. Clients pair or send credentials to an alternate address only after
// the existing device-login approval, which proves both addresses share one
// backend, has succeeded there.

// ServerIdentityService reads the deployment's stable native server identity.
// *serveridentity.Service implements it.
type ServerIdentityService interface {
	ServerID(context.Context) (string, error)
}

// ServerConnections is where the connections document reads the deployment's
// client-facing addresses from: the configured public URL, read live so a
// settings change applies without restart, and the API host's provider
// status cache for overlay origins. Both are optional.
type ServerConnections struct {
	PublicURL func() string
	Providers *netaccess.StatusCache
}

// ServerIdentity is the public identity document.
type ServerIdentity struct {
	ServerID string `json:"server_id" doc:"Stable identity of this deployment, the same at every address and on every API process; minted once and kept across restarts, hostname changes and database restores. Public and self-asserted: never authorize on it alone." example:"3f2a9d5e-6b1c-4c7e-9a0d-2f4b8c1e7a35"`
}

// ServerIdentityOutput is the getServerIdentity response.
type ServerIdentityOutput struct {
	CacheControl string `header:"Cache-Control"`
	Body         ServerIdentity
}

// Endpoint kinds in the connections document, and the default access path.
const (
	EndpointKindPublic   = "public"
	EndpointKindProvider = "provider"
	AccessPathDefault    = "default"

	// publicDiscoveryCache is the reviewed policy of the public discovery
	// documents: identical for every caller, revalidated rather than stored.
	publicDiscoveryCache = "no-cache"
)

// ServerEndpoint is one address the deployment offers to clients: a tagged
// union of the public and provider variants below, discriminated by kind.
// The wire struct carries the superset; the schema is the union so a
// validator rejects a public endpoint without a url or a provider endpoint
// without its slug and state.
type ServerEndpoint struct {
	Kind        string `json:"kind" enum:"public,provider" doc:"public is server.public_url; provider is a network access provider's overlay origin on the API host"`
	URL         string `json:"url,omitempty" doc:"scheme://host[:port] clients reach the API at; absent for a provider that is not connected on the API host"`
	Provider    string `json:"provider,omitempty" doc:"Provider slug (e.g. tailscale) for kind provider"`
	DisplayName string `json:"display_name,omitempty" doc:"Provider display name from its manifest, for setup help on the receiving device"`
	State       string `json:"state,omitempty" enum:"disconnected,awaiting_authorization,connecting,connected,error,unavailable" doc:"Provider state on the API host for kind provider; only connected carries a url"`
}

// ServerEndpointPublic is the configured public URL.
type ServerEndpointPublic struct {
	Kind string `json:"kind" enum:"public" doc:"server.public_url as configured"`
	URL  string `json:"url" doc:"scheme://host[:port] clients reach the API at"`
}

// ServerEndpointProvider is one installed network access provider on the API
// host. A url is present only while the provider is connected there; a
// connected provider whose origin is not an http(s) origin has none.
type ServerEndpointProvider struct {
	Kind        string `json:"kind" enum:"provider" doc:"A network access provider's overlay origin on the API host"`
	Provider    string `json:"provider" doc:"Provider slug (e.g. tailscale)"`
	DisplayName string `json:"display_name" doc:"Provider display name from its manifest, for setup help on the receiving device"`
	State       string `json:"state" enum:"disconnected,awaiting_authorization,connecting,connected,error,unavailable" doc:"Provider state on the API host; only connected carries a url"`
	URL         string `json:"url,omitempty" doc:"scheme://host[:port] clients reach the API at over the overlay; present only while connected"`
}

func (ServerEndpoint) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		r.Schema(reflect.TypeFor[ServerEndpointPublic](), true, ""),
		r.Schema(reflect.TypeFor[ServerEndpointProvider](), true, ""),
	}}
}

// ServerAccessPath is the access path the current request arrived on: a
// tagged union of the default and provider variants, discriminated by kind.
type ServerAccessPath struct {
	Kind     string `json:"kind" enum:"default,provider" doc:"default for the public URL, LAN or a reverse proxy; provider for an overlay origin"`
	Provider string `json:"provider,omitempty" doc:"Provider slug when kind is provider"`
}

// ServerAccessPathDefault is a request that did not arrive through a provider.
type ServerAccessPathDefault struct {
	Kind string `json:"kind" enum:"default" doc:"The public URL, LAN or a reverse proxy"`
}

// ServerAccessPathProvider is a request that arrived through a provider's
// overlay listener.
type ServerAccessPathProvider struct {
	Kind     string `json:"kind" enum:"provider" doc:"An overlay origin"`
	Provider string `json:"provider" doc:"Provider slug the request came through"`
}

func (ServerAccessPath) Schema(r huma.Registry) *huma.Schema {
	return &huma.Schema{OneOf: []*huma.Schema{
		r.Schema(reflect.TypeFor[ServerAccessPathDefault](), true, ""),
		r.Schema(reflect.TypeFor[ServerAccessPathProvider](), true, ""),
	}}
}

// ServerConnectionsDocument is the connections capability document.
type ServerConnectionsDocument struct {
	Capability
	ServerID  string           `json:"server_id" doc:"The same identity getServerIdentity reports"`
	Current   ServerAccessPath `json:"current" doc:"How this request reached the server"`
	Endpoints []ServerEndpoint `json:"endpoints" doc:"Addresses the deployment offers, public first then providers in slug order; empty when no public URL is configured and no provider is installed. Reachability is the client's to test."`
}

// ServerConnectionsOutput is the getServerConnections response.
type ServerConnectionsOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         ServerConnectionsDocument
}

func (ServerConnectionsDocument) capabilityState() string { return StateAvailable }

func registerServerIdentity(reg *Registry) {
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/system/identity", "getServerIdentity", "system",
			"Read the deployment's stable server identity before login, so a client can tell whether two addresses lead to the same server. Self-asserted: pairing and credentials still go through device-login approval."),
		Class: ClassPublic, ServiceBacked: true,
	}, reg.getServerIdentity)
	Register(reg, Operation{
		Operation: humaOp(http.MethodGet, Prefix+"/system/connections", "getServerConnections", "system",
			"List the addresses this deployment offers to clients: the configured public URL and each installed network access provider with its overlay origin when connected on the API host. available whenever the server identity is readable; a missing public URL is an absent endpoint, never a manufactured one. Node backend addresses, enrollment URLs and admin status are not included."),
		Class: ClassAuthenticated, ServiceBacked: true,
	}, reg.getServerConnections)
}

func (reg *Registry) serverID(ctx context.Context) (string, error) {
	if reg.deps.ServerIdentity == nil {
		return "", unavailable("server identity")
	}
	id, err := reg.deps.ServerIdentity.ServerID(ctx)
	if err != nil {
		if errors.Is(err, serveridentity.ErrUnavailable) {
			return "", unavailable("server identity")
		}
		return "", NewProblem(TypeInternalError, "An unexpected error occurred.")
	}
	return id, nil
}

func (reg *Registry) getServerIdentity(ctx context.Context, _ *struct{}) (*ServerIdentityOutput, error) {
	id, err := reg.serverID(ctx)
	if err != nil {
		return nil, err
	}
	return &ServerIdentityOutput{CacheControl: publicDiscoveryCache, Body: ServerIdentity{ServerID: id}}, nil
}

func (reg *Registry) getServerConnections(ctx context.Context, _ *CapabilityInput) (*ServerConnectionsOutput, error) {
	id, err := reg.serverID(ctx)
	if err != nil {
		return nil, err
	}
	doc := ServerConnectionsDocument{ServerID: id, Current: ServerAccessPath{Kind: AccessPathDefault}, Endpoints: []ServerEndpoint{}}
	if path := netaccess.PathFromContext(ctx); !path.IsDefault() {
		doc.Current = ServerAccessPath{Kind: EndpointKindProvider, Provider: path.Provider}
	}
	connections := reg.deps.ServerConnections
	if connections.PublicURL != nil {
		if origin, ok := netaccess.NormalizeOrigin(connections.PublicURL()); ok {
			doc.Endpoints = append(doc.Endpoints, ServerEndpoint{Kind: EndpointKindPublic, URL: origin})
		}
	}
	if reg.deps.NetworkAccess != nil {
		providers, err := reg.deps.NetworkAccess.ListNetworkAccessProviders(ctx)
		if err != nil {
			return nil, serviceProblem(err)
		}
		sort.Slice(providers, func(i, j int) bool { return providers[i].Provider < providers[j].Provider })
		for _, provider := range providers {
			endpoint := ServerEndpoint{Kind: EndpointKindProvider, Provider: provider.Provider, DisplayName: provider.DisplayName, State: netaccess.StateUnavailable}
			if status, ok := connections.Providers.ByProvider(provider.Provider); ok {
				endpoint.State = status.State
				if !publishedNetworkAccessStates[endpoint.State] {
					endpoint.State = netaccess.StateError
				}
				if status.Connected() {
					if origin, ok := netaccess.NormalizeOrigin(strings.TrimSpace(status.Origin)); ok {
						endpoint.URL = origin
					}
				}
			}
			doc.Endpoints = append(doc.Endpoints, endpoint)
		}
	}
	return &ServerConnectionsOutput{Body: doc}, nil
}
