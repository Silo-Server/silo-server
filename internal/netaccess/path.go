// Package netaccess owns the access path a request arrived on: the default
// path (LAN, public URL, reverse proxy) or an overlay network fronted by a
// network access provider plugin. The plugin stamps every request it proxies
// with a per-process ingress token; the middleware here validates the token,
// strips the header and records the path on the request context so later
// stages (stream URL selection, WebSocket origin checks) can act on it.
package netaccess

import "context"

// Path is the access path of one request. The zero value is the default
// path; Provider names the network access provider (e.g. "tailscale") whose
// listener the request came through.
type Path struct {
	Provider string
}

// IsDefault reports whether the request did not arrive through a provider.
func (p Path) IsDefault() bool { return p.Provider == "" }

type pathKey struct{}

// WithPath returns ctx carrying the access path.
func WithPath(ctx context.Context, path Path) context.Context {
	return context.WithValue(ctx, pathKey{}, path)
}

// PathFromContext returns the access path recorded on ctx, or the default
// path when the request did not pass the ingress middleware or carried no
// token.
func PathFromContext(ctx context.Context) Path {
	if ctx == nil {
		return Path{}
	}
	path, _ := ctx.Value(pathKey{}).(Path)
	return path
}
