package catalog

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type connectionCheckTransport func(*http.Request) (*http.Response, error)

func (f connectionCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// Every response is produced in memory. There is no listener, dialer, or provider.
func TestMeilisearchConnectionCheckRedirectPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, key, location string
		wantCalls           int
		wantErr             error
		wantSuccess         bool
	}{
		{name: "credential direct", key: "SYNTHETIC_CREDENTIAL", wantCalls: 1, wantSuccess: true},
		{name: "credential same-authority redirect followed", key: "SYNTHETIC_CREDENTIAL", location: "/health-next", wantCalls: 2, wantSuccess: true},
		{name: "credential other-host redirect refused", key: "SYNTHETIC_CREDENTIAL", location: "https://other.example.test/health", wantCalls: 1, wantErr: ErrMeilisearchCheckRedirected},
		{name: "credential scheme-change redirect refused", key: "SYNTHETIC_CREDENTIAL", location: "http://search.example.test/health", wantCalls: 1, wantErr: ErrMeilisearchCheckRedirected},
		{name: "no credential direct", wantCalls: 1, wantSuccess: true},
		{name: "no credential redirect preserved", location: "/health-next", wantCalls: 2, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newMeilisearchClient("https://search.example.test/base", tc.key, 1234*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client.httpClient.Transport = connectionCheckTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 2 {
					t.Fatal("unexpected extra request")
				}
				if req.URL.Scheme != "https" || req.URL.Host != "search.example.test" {
					t.Fatal("check left its configured authority")
				}
				wantAuth := ""
				if tc.key != "" {
					wantAuth = "Bearer " + tc.key
				}
				if req.Header.Get("Authorization") != wantAuth {
					t.Fatal("unexpected authorization")
				}
				response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"status":"available"}`)), Request: req}
				if tc.location != "" && calls == 1 {
					response.StatusCode = http.StatusPermanentRedirect
					response.Header.Set("Location", tc.location)
				}
				return response, nil
			})
			err = client.checkConnection(t.Context())
			if (err == nil) != tc.wantSuccess || calls != tc.wantCalls {
				t.Fatalf("check success = %v, calls = %d; want %v, %d", err == nil, calls, tc.wantSuccess, tc.wantCalls)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("check error = %v, want %v", err, tc.wantErr)
			}
			if client.httpClient.Timeout != 1234*time.Millisecond {
				t.Fatal("connection deadline changed")
			}
		})
	}
}
