package handlers

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

func TestSetupRequestIntent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, contentType, origin, site string
		want                            int
	}{
		{"plain text JSON", "text/plain;charset=UTF-8", "", "", http.StatusUnsupportedMediaType},
		{"form", "application/x-www-form-urlencoded", "", "", http.StatusUnsupportedMediaType},
		{"multipart", "multipart/form-data; boundary=fixture", "", "", http.StatusUnsupportedMediaType},
		{"missing media type", "", "", "", http.StatusUnsupportedMediaType},
		{"malformed media type", "application/json; broken", "", "", http.StatusUnsupportedMediaType},
		{"foreign origin", "application/json", "https://other.example.test", "", http.StatusForbidden},
		{"opaque origin", "application/json", "null", "", http.StatusForbidden},
		{"cross-site", "application/json", "", "cross-site", http.StatusForbidden},
		{"same-site sibling", "application/json", "https://other.example.test", "same-site", http.StatusForbidden},
		{"native JSON", "application/json", "", "", http.StatusBadRequest},
		{"JSON charset", "application/json; charset=utf-8", "", "", http.StatusBadRequest},
		{"same-origin", "application/json", "https://silo.example.test", "same-origin", http.StatusBadRequest},
		{"origin fallback", "application/json", "https://silo.example.test", "", http.StatusBadRequest},
		{"proxy same-origin metadata", "application/json", "https://public.example.test", "same-origin", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://silo.example.test/api/v1/auth/setup", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", tc.contentType)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			w := httptest.NewRecorder()
			// Empty registration fields stop before the unset service. A 400
			// proves accepted intent reached normal registration validation.
			(&AuthHandler{}).HandleSetup(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestSetupRequestIntentProxyHost(t *testing.T) {
	t.Parallel()
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted", true: "trusted"}[trusted], func(t *testing.T) {
			var cidrs []*net.IPNet
			want := http.StatusForbidden
			if trusted {
				_, network, err := net.ParseCIDR("127.0.0.0/8")
				if err != nil {
					t.Fatal(err)
				}
				cidrs = append(cidrs, network)
				want = http.StatusBadRequest
			}
			r := httptest.NewRequest(http.MethodPost, "http://upstream.example.test/api/v1/auth/setup", strings.NewReader(`{}`))
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://public.example.test")
			r.Header.Set("X-Forwarded-Host", "public.example.test")
			w := httptest.NewRecorder()
			clientip.Middleware(clientip.NewResolver(cidrs))(http.HandlerFunc((&AuthHandler{}).HandleSetup)).ServeHTTP(w, r)
			if w.Code != want {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, want, w.Body.String())
			}
		})
	}
}
