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
		status, indexStatus int
		searchStatus        int
		proxyNotFound       bool
		wantCalls           int
		wantRedirected      bool
		wantKeyRejected     bool
		wantSuccess         bool
	}{
		{name: "credential direct", key: "SYNTHETIC_CREDENTIAL", wantCalls: 3, wantSuccess: true},
		{name: "credential same-authority redirect followed", key: "SYNTHETIC_CREDENTIAL", location: "/health-next", wantCalls: 4, wantSuccess: true},
		{name: "credential other-host redirect refused", key: "SYNTHETIC_CREDENTIAL", location: "https://other.example.test/health", wantCalls: 1, wantRedirected: true},
		{name: "credential scheme-change redirect refused", key: "SYNTHETIC_CREDENTIAL", location: "http://search.example.test/health", wantCalls: 1, wantRedirected: true},
		{name: "credential other-port redirect refused", key: "SYNTHETIC_CREDENTIAL", location: "https://search.example.test:8443/health", wantCalls: 1, wantRedirected: true},
		{name: "credential explicit default port redirect followed", key: "SYNTHETIC_CREDENTIAL", location: "https://search.example.test:443/health-next", wantCalls: 4, wantSuccess: true},
		{name: "credential 3xx without location is not a redirect", key: "SYNTHETIC_CREDENTIAL", status: http.StatusFound, wantCalls: 1},
		{name: "credential rejected by authenticated read", key: "SYNTHETIC_CREDENTIAL", indexStatus: http.StatusForbidden, wantCalls: 2, wantKeyRejected: true},
		{name: "credential read unavailable is not a key error", key: "SYNTHETIC_CREDENTIAL", indexStatus: http.StatusServiceUnavailable, wantCalls: 2},
		{name: "credential scoped away from Silo's indexes", key: "SYNTHETIC_CREDENTIAL", searchStatus: http.StatusForbidden, wantCalls: 3, wantKeyRejected: true},
		{name: "credential search answered by a proxy 404", key: "SYNTHETIC_CREDENTIAL", proxyNotFound: true, wantCalls: 3},
		{name: "no credential rejected by secured instance", indexStatus: http.StatusUnauthorized, wantCalls: 2, wantKeyRejected: true},
		{name: "no credential direct", wantCalls: 3, wantSuccess: true},
		{name: "no credential redirect preserved", location: "/health-next", wantCalls: 4, wantSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := newMeilisearchClient("https://search.example.test/base", tc.key, 1234*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client.httpClient.Transport = connectionCheckTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls > 4 {
					t.Fatal("unexpected extra request")
				}
				if req.URL.Scheme != "https" || req.URL.Hostname() != "search.example.test" || (req.URL.Port() != "" && req.URL.Port() != "443") {
					t.Fatal("check left its configured authority")
				}
				wantAuth := ""
				if tc.key != "" {
					wantAuth = "Bearer " + tc.key
				}
				if req.Header.Get("Authorization") != wantAuth {
					t.Fatal("unexpected authorization")
				}
				body := `{"status":"available"}`
				status := http.StatusOK
				switch req.URL.Path {
				case "/base/indexes":
					body = `{"results":[],"total":0}`
				case "/base/indexes/silo_media_items_rebuild_check/search":
					body = `{"code":"index_not_found"}`
					status = http.StatusNotFound
					if tc.searchStatus != 0 {
						body = `{"code":"invalid_api_key"}`
						status = tc.searchStatus
					}
					if tc.proxyNotFound {
						body = "404 page not found"
					}
				}
				response := &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
				if tc.location != "" && calls == 1 {
					response.StatusCode = http.StatusPermanentRedirect
					response.Header.Set("Location", tc.location)
				}
				if tc.status != 0 {
					response.StatusCode = tc.status
				}
				if tc.indexStatus != 0 && req.URL.Path == "/base/indexes" {
					response.StatusCode = tc.indexStatus
				}
				return response, nil
			})
			err = client.checkConnection(t.Context(), DefaultMeilisearchIndex)
			if (err == nil) != tc.wantSuccess || calls != tc.wantCalls {
				t.Fatalf("check success = %v, calls = %d; want %v, %d", err == nil, calls, tc.wantSuccess, tc.wantCalls)
			}
			if errors.Is(err, ErrMeilisearchCheckRedirected) != tc.wantRedirected || errors.Is(err, ErrMeilisearchCheckKeyRejected) != tc.wantKeyRejected {
				t.Fatalf("check error = %v, want redirected = %v, key rejected = %v", err, tc.wantRedirected, tc.wantKeyRejected)
			}
			if client.httpClient.Timeout != 1234*time.Millisecond {
				t.Fatal("connection deadline changed")
			}
		})
	}
}
