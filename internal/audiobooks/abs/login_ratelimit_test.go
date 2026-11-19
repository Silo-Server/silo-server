package abs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

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
