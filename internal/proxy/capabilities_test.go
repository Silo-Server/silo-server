package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyCapabilitiesRequireBearer(t *testing.T) {
	server := newCapabilityProxyServer(t, "capability-secret")

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/hw-capabilities", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an unauthenticated capability probe", recorder.Code)
	}
}
