// Package nodehttp installs the core's negotiated worker transport on internal
// node clients. Other HTTP clients, including external integrations, do not use
// this hook. Worker processes leave it unconfigured.
package nodehttp

import (
	"context"
	"net/http"
	"sync/atomic"
)

type artifactStatContextKey struct{}

// WithArtifactStat marks the core preparer's metadata-only artifact probe.
// Worker output selection limits this exception to exact artifact HEAD routes.
func WithArtifactStat(ctx context.Context) context.Context {
	return context.WithValue(ctx, artifactStatContextKey{}, true)
}

func IsArtifactStat(ctx context.Context) bool {
	marked, _ := ctx.Value(artifactStatContextKey{}).(bool)
	return marked
}

type configuration struct {
	wrap           func(http.RoundTripper) http.RoundTripper
	requiresRecipe func(context.Context, string) (bool, error)
}

var configured atomic.Pointer[configuration]

// Configure is called by core startup before it admits background or viewer
// work. Nil restores the ordinary legacy node transport.
func Configure(wrap func(http.RoundTripper) http.RoundTripper, requiresRecipe ...func(context.Context, string) (bool, error)) {
	if wrap == nil {
		configured.Store(nil)
		return
	}
	options := &configuration{wrap: wrap}
	if len(requiresRecipe) > 0 {
		options.requiresRecipe = requiresRecipe[0]
	}
	configured.Store(options)
}

// RequiresRecipe asks the core's registered worker authority, independently of
// the client's media-auth mode. Legacy transports have no such requirement.
// Registry failures must remain errors rather than downgrade to a legacy node.
func RequiresRecipe(ctx context.Context, nodeURL string) (bool, error) {
	if options := configured.Load(); options != nil && options.requiresRecipe != nil {
		return options.requiresRecipe(ctx, nodeURL)
	}
	return false, nil
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if options := configured.Load(); options != nil {
		return options.wrap(base).RoundTrip(r)
	}
	return base.RoundTrip(r)
}

func Wrap(base http.RoundTripper) http.RoundTripper {
	if _, wrapped := base.(*transport); wrapped {
		return base
	}
	return &transport{base: base}
}

func Client(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	copy := *client
	copy.Transport = Wrap(copy.Transport)
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}
