package streamtelemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenericCaptureIgnoresUnresolvedForwardedFor(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/media", nil)
	request.RemoteAddr = "192.0.2.10:4321"
	request.Header.Set("X-Forwarded-For", "8.8.8.8")

	if got := genericCapture(request).ViewerIP; got != "192.0.2.10" {
		t.Fatalf("viewer IP = %q, want socket peer", got)
	}
}
