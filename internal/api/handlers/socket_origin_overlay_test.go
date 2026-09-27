package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netaccess"
)

// A browser reaching Silo through a connected network access provider sends
// the overlay origin. Both origin checks accept it while the provider is
// connected and refuse it again once the provider drops, without touching the
// public-origin rule.
func TestSocketOriginAcceptsConnectedOverlayOrigin(t *testing.T) {
	cache := netaccess.NewStatusCache()
	cache.Report(netaccess.Status{
		InstallationID: 3, Provider: "tailscale", State: netaccess.StateConnected,
		Origin:    "https://silo.tail1234.ts.net",
		Listeners: []netaccess.Listener{{Name: "api", Origin: "https://silo.tail1234.ts.net"}},
	})
	overlay := cache.ConnectedOrigins()

	newReq := func(origin string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://internal.test/api/v2/events/ws", nil)
		r.Host = "internal.test"
		r.Header.Set("Origin", origin)
		return r
	}
	if !socketOriginAllowed(newReq("https://silo.tail1234.ts.net"), "https://public.test", overlay) {
		t.Fatal("overlay origin refused while the provider is connected")
	}
	if !socketOriginAllowed(newReq("https://public.test"), "https://public.test", overlay) {
		t.Fatal("public origin refused with an overlay configured")
	}
	if socketOriginAllowed(newReq("http://silo.tail1234.ts.net"), "https://public.test", overlay) {
		t.Fatal("overlay host accepted with the wrong scheme")
	}
	if socketOriginAllowed(newReq("https://evil.test"), "https://public.test", overlay) {
		t.Fatal("foreign origin accepted")
	}
	if socketOriginAllowed(newReq("https://silo.tail1234.ts.net"), "https://public.test", nil) {
		t.Fatal("overlay origin accepted without a connected provider")
	}

	// The handlers read the source per handshake: a dropped provider is
	// refused on the next check without a config reload.
	h := &EventsSocketV2{PublicOrigin: "https://public.test"}
	h.SetOverlayOrigins(cache.ConnectedOrigins)
	if !h.validOrigin(newReq("https://silo.tail1234.ts.net")) {
		t.Fatal("events socket refused the overlay origin")
	}
	cache.Forget(3)
	if h.validOrigin(newReq("https://silo.tail1234.ts.net")) {
		t.Fatal("events socket accepted the overlay origin after the provider dropped")
	}
}

func TestCheckWebSocketOriginAcceptsConnectedOverlayOrigin(t *testing.T) {
	cache := netaccess.NewStatusCache()
	SetWebSocketOverlayOrigins(cache.ConnectedOrigins)
	t.Cleanup(func() { SetWebSocketOverlayOrigins(nil) })

	req := httptest.NewRequest(http.MethodGet, "/playback/sessions/abc/control/ws", nil)
	req.Host = "origin.internal:8097"
	req.Header.Set("Origin", "https://silo.tail1234.ts.net")
	if checkWebSocketOrigin(req) {
		t.Fatal("overlay origin accepted before the provider connected")
	}
	cache.Report(netaccess.Status{InstallationID: 3, Provider: "tailscale", State: netaccess.StateConnected, Origin: "https://silo.tail1234.ts.net"})
	if !checkWebSocketOrigin(req) {
		t.Fatal("overlay origin refused while the provider is connected")
	}
	req.Header.Set("Origin", "https://evil.test")
	if checkWebSocketOrigin(req) {
		t.Fatal("foreign origin accepted")
	}
}
