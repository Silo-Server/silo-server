package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckWebSocketOrigin(t *testing.T) {
	tests := []struct {
		name          string
		host          string
		origin        string
		forwardedHost string
		want          bool
	}{
		{
			name:   "missing origin is allowed for non-browser clients",
			host:   "origin.internal:8097",
			origin: "",
			want:   true,
		},
		{
			name:   "same origin matches host directly",
			host:   "silo.example.com",
			origin: "https://silo.example.com",
			want:   true,
		},
		{
			name:   "foreign origin without a proxy is rejected",
			host:   "silo.example.com",
			origin: "https://evil.example.com",
			want:   false,
		},
		{
			// The CDN regression: it rewrites Host to the internal origin and
			// carries the public host in X-Forwarded-Host.
			name:          "cdn-rewritten host accepts the forwarded public origin",
			host:          "origin.internal:8097",
			origin:        "https://silo.arkyncdn.net",
			forwardedHost: "silo.arkyncdn.net",
			want:          true,
		},
		{
			name:          "foreign origin is rejected even with a forwarded host",
			host:          "origin.internal:8097",
			origin:        "https://evil.example.com",
			forwardedHost: "silo.arkyncdn.net",
			want:          false,
		},
		{
			name:          "first hop of a multi-proxy forwarded host is honored",
			host:          "origin.internal:8097",
			origin:        "https://silo.arkyncdn.net",
			forwardedHost: "silo.arkyncdn.net, edge.internal",
			want:          true,
		},
		{
			name:   "opaque null origin is rejected",
			host:   "silo.example.com",
			origin: "null",
			want:   false,
		},
		{
			name:   "unparseable origin is rejected",
			host:   "silo.example.com",
			origin: "https://silo.example.com\x7f",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/playback/sessions/abc/control/ws", nil)
			req.Host = tt.host
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.forwardedHost != "" {
				req.Header.Set("X-Forwarded-Host", tt.forwardedHost)
			}

			if got := checkWebSocketOrigin(req); got != tt.want {
				t.Fatalf("checkWebSocketOrigin() = %v, want %v", got, tt.want)
			}
		})
	}
}
