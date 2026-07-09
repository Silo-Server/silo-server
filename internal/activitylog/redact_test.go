package activitylog

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestRedactSecretPathParams(t *testing.T) {
	cases := []struct {
		name   string
		params map[string]string
		path   string
		want   string
	}{
		{
			name:   "autoscan webhook token",
			params: map[string]string{"token": "2riBxLS4UcC8w8WdRoge8F0f8LOLkz4yPsPNA01gWBY"},
			path:   "/api/v1/autoscan/webhooks/2riBxLS4UcC8w8WdRoge8F0f8LOLkz4yPsPNA01gWBY",
			want:   "/api/v1/autoscan/webhooks/[redacted]",
		},
		{
			name:   "webhook-sync secret",
			params: map[string]string{"secret": "c98bffdbe95b5be34141e2a7f0327e1f"},
			path:   "/api/v1/webhook-sync/webhooks/c98bffdbe95b5be34141e2a7f0327e1f",
			want:   "/api/v1/webhook-sync/webhooks/[redacted]",
		},
		{
			name:   "non-secret params untouched",
			params: map[string]string{"id": "abc123"},
			path:   "/api/v1/admin/autoscan/sources/abc123",
			want:   "/api/v1/admin/autoscan/sources/abc123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", tc.path, nil)
			routeCtx := chi.NewRouteContext()
			for k, v := range tc.params {
				routeCtx.URLParams.Add(k, v)
			}
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))

			if got := RedactSecretPathParams(r, tc.path); got != tc.want {
				t.Fatalf("redacted path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRedactSecretPathParamsNoRouteContext(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v1/autoscan/webhooks/tok", nil)
	if got := RedactSecretPathParams(r, r.URL.Path); got != r.URL.Path {
		t.Fatalf("path without route context must pass through, got %q", got)
	}
}
