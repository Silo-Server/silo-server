package abs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccessResolutionErrorsAreClassifiedAndRedacted(t *testing.T) {
	h := New(Dependencies{MediaStore: noopMediaStore{}})
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "denied", err: ErrAccessDenied, wantStatus: http.StatusForbidden},
		{name: "operational", err: errors.New("postgres password=secret unavailable"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			rec := httptest.NewRecorder()
			h.writeAccessResolutionError(rec, req, tt.err)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if strings.Contains(rec.Body.String(), "password=secret") {
				t.Fatalf("response exposed resolver details: %s", rec.Body.String())
			}
		})
	}
}
