package pluginhost

import (
	"context"
	"net"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// Host roles reported in GetHostInfo.host_role.
const (
	HostRoleAPI   = "api"
	HostRoleProxy = "proxy"
)

// Listener names reported in GetHostInfo.listeners.
const (
	ListenerAPI      = "api"
	ListenerJellyfin = "jellyfin"
	ListenerABS      = "abs"
)

// Default overlay ports a network access provider exposes each listener on.
const (
	DefaultPortAPI      = 443
	DefaultPortJellyfin = 8096
	DefaultPortABS      = 13378
)

// HostListener is one local listener a network access provider should expose
// on the overlay: its loopback dial address and the port to expose it on.
type HostListener struct {
	Name        string
	Address     string
	DefaultPort int
}

// HostInfo is what GetHostInfo reports about the process hosting the plugin.
// Public URLs and listeners can change with a config reload, so the host
// supplies a HostInfoFunc that builds it per call.
type HostInfo struct {
	// PublicBaseURL is server.public_url without a trailing slash; empty when
	// unset.
	PublicBaseURL string
	// PluginContentPrefix is the API path under which plugin HTTP routes are
	// proxied; GetHostInfo appends /plugins/<installation_id> to it.
	PluginContentPrefix string
	Role                string
	Name                string
	NodeID              int64
	Listeners           []HostListener
}

// HostInfoFunc answers GetHostInfo for the current process.
type HostInfoFunc func(ctx context.Context) (HostInfo, error)

// NetworkAccessBroker is the netaccess side of a resident network access
// provider's lifetime: Host.Start issues the ingress token before the plugin
// can ask for it, stopping the process revokes it, and status pushes land in
// the host's status cache. *netaccess.Broker implements it.
type NetworkAccessBroker interface {
	Issue(installationID int, provider string) (string, error)
	// Revoke drops the token only while it is still the installation's
	// current one, so a late stop cannot revoke a replacement's token.
	Revoke(installationID int, token string)
	IngressToken(installationID int) (string, bool)
	Report(status netaccess.Status) (previous netaccess.Status, changed bool)
}

// NetworkAccessProviderSlug returns the provider slug a manifest declares
// through network_access_provider.v1 (the typed descriptor's provider, or the
// capability id when the descriptor is absent) and whether it declares one.
func NetworkAccessProviderSlug(manifest *pluginv1.PluginManifest) (string, bool) {
	descriptor, slug := NetworkAccessProviderCapability(manifest)
	return slug, descriptor != nil
}

// NetworkAccessProviderCapability returns the manifest's
// network_access_provider.v1 descriptor and the provider slug it declares
// (the typed descriptor's provider, or the capability id when the descriptor
// is absent). The descriptor is nil when the manifest declares none; a
// manifest declares at most one, so the first wins.
func NetworkAccessProviderCapability(manifest *pluginv1.PluginManifest) (*pluginv1.CapabilityDescriptor, string) {
	for _, descriptor := range manifest.GetCapabilities() {
		if descriptor.GetType() != capability.NetworkAccessProvider {
			continue
		}
		if slug := strings.TrimSpace(descriptor.GetNetworkAccessProvider().GetProvider()); slug != "" {
			return descriptor, slug
		}
		if id := strings.TrimSpace(descriptor.GetId()); id != "" {
			return descriptor, id
		}
	}
	return nil, ""
}

// NetworkAccessStatusFromProto converts a provider's reported status into the
// host's SDK-free form. Both the push (ReportNetworkAccessStatus) and the
// on-demand reads the admin API makes go through it so the status cache and
// the API agree on every field.
func NetworkAccessStatusFromProto(installationID int, provider string, reported *pluginv1.NetworkAccessStatus) netaccess.Status {
	entry := netaccess.Status{
		InstallationID:   installationID,
		Provider:         provider,
		State:            strings.TrimSpace(reported.GetState()),
		Hostname:         reported.GetHostname(),
		Origin:           reported.GetOrigin(),
		Addresses:        append([]string(nil), reported.GetAddresses()...),
		AuthURL:          reported.GetAuthUrl(),
		Error:            reported.GetError(),
		ProviderVersion:  reported.GetProviderVersion(),
		DesiredConnected: reported.GetDesiredConnected(),
	}
	for _, listener := range reported.GetListeners() {
		entry.Listeners = append(entry.Listeners, netaccess.Listener{Name: listener.GetName(), Origin: listener.GetOrigin()})
	}
	return entry
}

// LoopbackDialAddress turns a listen address into the host:port a plugin in
// the same process namespace dials: a wildcard or empty host becomes
// 127.0.0.1, a concrete host is kept.
func LoopbackDialAddress(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
