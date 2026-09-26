package proxy

import (
	"net/http"

	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/workerprotocol"
	"github.com/danielgtaylor/huma/v2"
)

// ProtocolReads describes the retained reads using the same DTOs their handlers
// encode. The bearer middleware and worker paths remain owned by this listener.
func ProtocolReads(schemas huma.Registry) []workerprotocol.Operation {
	return []workerprotocol.Operation{
		workerprotocol.JSONRead[playback.HWAccelInfo](schemas, "proxy", "/hw-capabilities", "(*internal/proxy.Server).handleHWCapabilities", 401, 503),
		workerprotocol.JSONRead[statusResponse](schemas, "proxy", "/status", "(*internal/proxy.Server).handleStatus", 401),
	}
}

// ProtocolControls describes the existing worker admin commands, not native API aliases.
func ProtocolControls(schemas huma.Registry) []workerprotocol.Operation {
	reprobe := workerprotocol.JSONRead[reprobeCapabilitiesResponse](schemas, "proxy", "/admin/reprobe-capabilities", "(*internal/proxy.Server).handleReprobeCapabilities", 401, 409, 503)
	reprobe.Method = "POST"
	reprobe.RetrySafety = "non_retryable"
	reprobe.Description = "Rebuild the capability snapshot. Busy probes refuse with 409; an incomplete probe retains the prior published hash. No durable replay receipt."
	return []workerprotocol.Operation{
		workerprotocol.EmptyCommand("proxy", "/admin/force-reload", "(*internal/proxy.Server).handleForceReload", "Reload configuration; active remux work is not torn down."),
		workerprotocol.EmptyCommand("proxy", "/admin/reload-config", "(*internal/proxy.Server).handleReloadConfig", "Reload configuration without tearing down active sessions. No durable replay receipt."),
		reprobe,
	}
}

// ProtocolNetworkAccess describes the bearer routes the API server fans its
// network access admin operations out to: one read of every provider instance
// on this proxy, and per-provider status, connect and disconnect. They are worker
// contracts, never native API aliases.
func ProtocolNetworkAccess(schemas huma.Registry) []workerprotocol.Operation {
	const listener = "proxy"
	const providerPathIn = "path"
	status := workerprotocol.JSONRead[netaccess.HostStatusReport](schemas, listener, "/network-access/status", "(*internal/proxy.Server).handleNetworkAccessStatus", 401, 500, 503)
	status.Description = "Live status of every network access provider plugin instance running on this proxy, read from the plugin with a ten-second timeout each. Carries auth_url and error because the caller holds the node bearer. 503 when the proxy hosts no plugins."
	provider := []*huma.Param{{Name: "provider", In: providerPathIn, Required: true, Schema: &huma.Schema{Type: huma.TypeString}, Description: "Provider slug declared by the plugin manifest, e.g. tailscale."}}
	providerStatus := workerprotocol.JSONRead[netaccess.Status](schemas, listener, "/network-access/{provider}/status", "(*internal/proxy.Server).handleNetworkAccessProviderStatus", 401, 404, 500, 503)
	providerStatus.Parameters = provider
	providerStatus.Description = "Live status of the named network access provider on this proxy, with a ten-second timeout. Reads only this provider, so another provider's timeout does not hide its status. 404 for a provider no enabled installation declares; 503 when the proxy hosts no plugins."
	command := func(path, handler, description string) workerprotocol.Operation {
		op := workerprotocol.JSONRead[netaccess.Status](schemas, listener, "/network-access/{provider}"+path, handler, 401, 404, 500, 503)
		op.Method = http.MethodPost
		op.Parameters = provider
		op.RetrySafety = "natural_idempotent"
		op.Description = description
		return op
	}
	return []workerprotocol.Operation{
		status,
		providerStatus,
		command("/connect", "(*internal/proxy.Server).handleNetworkAccessConnect", "Ask the provider on this proxy to bring its overlay identity up; answers the state reached within ten seconds. Repeating converges on one connected instance. 404 for a provider no enabled installation declares."),
		command("/disconnect", "(*internal/proxy.Server).handleNetworkAccessDisconnect", "Ask the provider on this proxy to tear its overlay listener down; answers the state reached within ten seconds. Repeating converges on disconnected. 404 for a provider no enabled installation declares."),
	}
}
