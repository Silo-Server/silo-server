package abs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

// The ABS listener is public and clientip.Middleware overwrites RemoteAddr with
// a header-derived address whenever the TCP peer is a trusted proxy. If the
// limiter keyed on RemoteAddr it would hand every spoofed X-Forwarded-For its
// own bucket, which is the whole point of the deliberate RemoteAddr-only rule.
func TestClientIPUsesTransportPeerNotResolvedAddress(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "203.0.113.7" // as clientip.Middleware rewrites it
	r = r.WithContext(clientip.SetPeerContext(r.Context(), "172.17.0.1:51234"))

	if got := clientIP(r); got != "172.17.0.1" {
		t.Fatalf("clientIP = %q, want the transport peer 172.17.0.1", got)
	}
}

// Without the middleware there is nothing in the context and RemoteAddr is
// still the untouched peer.
func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = "198.51.100.9:44321"

	if got := clientIP(r); got != "198.51.100.9" {
		t.Fatalf("clientIP = %q, want 198.51.100.9", got)
	}
}

// A rotating forged X-Forwarded-For must not buy fresh burst allowance.
func TestLoginLimiterNotBypassedByForwardedForRotation(t *testing.T) {
	limiter := NewLoginLimiter()
	defer limiter.Stop()

	allowed := 0
	for i := 0; i < loginLimitBurst+5; i++ {
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		// Same attacker, same TCP peer, a different spoofed viewer IP each time.
		r.RemoteAddr = "203.0.113." + string(rune('0'+i%10))
		r = r.WithContext(clientip.SetPeerContext(r.Context(), "172.17.0.1:51234"))
		if limiter.allow(clientIP(r)) {
			allowed++
		}
	}
	if allowed > loginLimitBurst {
		t.Fatalf("limiter allowed %d attempts, want at most the %d burst", allowed, loginLimitBurst)
	}
}
