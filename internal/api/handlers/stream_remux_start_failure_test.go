package handlers

import (
	"encoding/json"
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
	// A plain-text body left the delivery adapter nothing to name the failure
	// by, so its log recorded code=internal_error for a provider or relay
	// refusal. The code is additive: same status, same problem type.
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body = %q)", err, rec.Body.String())
	}
	if body.Error != "remux_start_failed" {
		t.Fatalf("error code = %q, want remux_start_failed", body.Error)
	}
}
