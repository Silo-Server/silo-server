// Package nodehttp installs the core's negotiated worker transport on internal
// node clients. Other HTTP clients, including external integrations, do not use
// this hook. Worker processes leave it unconfigured.
package nodehttp

import (
	"net/http"
	"sync/atomic"
)

type configuration struct {
	wrap func(http.RoundTripper) http.RoundTripper
}

var configured atomic.Pointer[configuration]

// Configure is called by core startup before it admits background or viewer
// work. Nil restores the ordinary legacy node transport.
func Configure(wrap func(http.RoundTripper) http.RoundTripper) {
	if wrap == nil {
		configured.Store(nil)
		return
	}
	configured.Store(&configuration{wrap: wrap})
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
