package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOAuthPublicOriginEquivalentIPs(t *testing.T) {
	for _, tc := range []struct {
		name, public, request string
		want                  bool
	}{
		{"compressed IPv6", "https://[0:0:0:0:0:0:0:1]", "https://[::1]", true},
		{"default port", "https://[0:0:0:0:0:0:0:1]:443", "https://[::1]", true},
		{"explicit port", "https://[0:0:0:0:0:0:0:1]:9443", "https://[::1]:9443", true},
		{"different IPv6", "https://[0:0:0:0:0:0:0:1]", "https://[::2]", false},
		{"different port", "https://[::1]:9443", "https://[::1]", false},
		{"different scheme", "https://[::1]", "http://[::1]", false},
		{"IPv4 mapped remains IPv6", "https://[::ffff:127.0.0.1]", "https://127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := url.Parse(tc.public)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", tc.request+"/api/v2/auth/oauth/42/native/start", nil)
			if got := sameOrigin(req.URL.Scheme, req.Host, base); got != tc.want {
				t.Fatalf("sameOrigin(%q, %q) = %v, want %v", tc.request, tc.public, got, tc.want)
			}
			h := NewOAuthHandler(OAuthHandlerDeps{HostBaseURL: tc.public})
			if got := h.OnPublicOrigin(req); got != tc.want {
				t.Fatalf("OnPublicOrigin(%q) = %v, want %v", tc.request, got, tc.want)
			}
		})
	}
}

func TestOAuthLinkResolverFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"missing provider", ErrUnknownAuthInstallation, ErrUnknownAuthInstallation},
		{"unavailable plugin", errors.New("plugin connection unavailable"), ErrProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newOAuthRig(t)
			ticket, err := rig.h.IssueLinkTicket(t.Context(), 7, 42, "right password")
			if err != nil {
				t.Fatal(err)
			}
			rig.h.deps.ResolveClient = func(_ context.Context, _ int) (OAuthClient, string, error) {
				return nil, "", tc.err
			}
			if _, err := rig.h.IssueLinkTicket(t.Context(), 7, 42, "right password"); !errors.Is(err, tc.want) {
				t.Fatalf("IssueLinkTicket error = %v, want %v", err, tc.want)
			}
			if _, err := rig.h.StartLink(t.Context(), 7, "/api/v2", ticket.Ticket, "/"); !errors.Is(err, tc.want) {
				t.Fatalf("StartLink error = %v, want %v", err, tc.want)
			}
		})
	}
}
