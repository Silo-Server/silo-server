package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A remux that produced no media byte is a dependency failure the client may
// retry, and the byte-delivery adapter maps its 502 to 503 dependency_unavailable.
// Production saw that 503 with no Retry-After at all while the manifest and
// segment routes already sent one, so a client had no delay to back off by.
func TestRemuxStartFailureAnswersRetryableWithRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	writeRemuxStartFailure(rec)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want the v1 502 the delivery adapter maps", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want 5", got)
	}
}
