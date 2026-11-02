package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSetupRequestMediaType(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contentType string
		want              int
	}{
		{"plain text JSON", "text/plain;charset=UTF-8", http.StatusUnsupportedMediaType},
		{"form", "application/x-www-form-urlencoded", http.StatusUnsupportedMediaType},
		{"multipart", "multipart/form-data; boundary=fixture", http.StatusUnsupportedMediaType},
		{"missing media type", "", http.StatusUnsupportedMediaType},
		{"malformed media type", "application/json; broken", http.StatusUnsupportedMediaType},
		{"ambiguous media type", "application/json, text/plain", http.StatusUnsupportedMediaType},
		{"vendor JSON", "application/vnd.fixture+json", http.StatusUnsupportedMediaType},
		{"native JSON", "application/json", http.StatusBadRequest},
		{"JSON charset", "application/json; charset=utf-8", http.StatusBadRequest},
		{"case insensitive JSON", "Application/JSON", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", tc.contentType)
			w := httptest.NewRecorder()
			// Empty registration fields stop before the unset service. A 400
			// proves accepted media types reach normal registration validation.
			(&AuthHandler{}).HandleSetup(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == http.StatusUnsupportedMediaType && !strings.Contains(w.Body.String(), `"unsupported_media_type"`) {
				t.Fatalf("body = %s, want unsupported_media_type error", w.Body.String())
			}
		})
	}
}
