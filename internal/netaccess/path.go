// Package netaccess owns the access path a request arrived on: the default
// path (LAN, public URL, reverse proxy), an N4 direct path, or an overlay fronted by a
// network access provider plugin. The plugin stamps every request it proxies
// with a per-process ingress token; the middleware here validates the token,
// strips the header and records the path on the request context so later
// stages (stream URL selection, WebSocket origin checks) can act on it.
package netaccess

import (
	"context"
	"net/netip"
)

// Path is the access path of one request. The zero value is the default
// path; Provider names the network access provider (e.g. "tailscale") whose
// listener the request came through.
type Path struct {
	// Direct records the N4 path; nil on default and provider requests.
	Direct   *DirectPath
	Provider string
	// InstallationID is the plugin installation whose ingress token the
	// request carried; 0 on the default path.
	InstallationID int
	// Peer is the overlay peer the provider vouched for with
	// X-Silo-Ingress-Peer; the zero Addr when it sent none. It says where the
	// request came from, never who may do what: a network identity sign-in
	// asks the provider's plugin who the peer is.
	Peer netip.Addr
}

// DirectPath identifies an N4 request's terminating host and admitted peer.
// Class is "direct". UserID, DeviceID and Policy remain empty until enrollment
// supplies them; this transport identity alone grants no application authority.
type DirectPath struct {
	HostID   string
	PeerKey  string
	DeviceID string
	UserID   int
	Class    string
	Policy   string
}

// IsDefault reports whether the request did not arrive through a provider.
func (p Path) IsDefault() bool { return p.Provider == "" }

type pathKey struct{}

// WithPath returns ctx carrying the access path.
func WithPath(ctx context.Context, path Path) context.Context {
	return context.WithValue(ctx, pathKey{}, path)
}

// PathFromContext returns the access path recorded on ctx, or the default
// path when neither the ingress middleware nor the direct wrapper recorded one.
func PathFromContext(ctx context.Context) Path {
	if ctx == nil {
		return Path{}
	}
	path, _ := ctx.Value(pathKey{}).(Path)
	return path
}
