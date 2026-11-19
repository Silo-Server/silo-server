package s3client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestS3UploadRejectsMethodChangingRedirects(t *testing.T) {
	for _, status := range []int{301, 302, 303} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var redirected atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/landing" {
					redirected.Add(1)
					w.WriteHeader(http.StatusOK)
					return
				}
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Location", "/landing")
				w.WriteHeader(status)
			}))
			defer srv.Close()
			c := NewClient(BucketConfig{Endpoint: srv.URL, Bucket: "b", AccessKey: "k", SecretKey: "s", PathStyle: true})
			if err := c.PutObject(t.Context(), "b", "object", []byte("payload")); err == nil {
				t.Error("upload reported success after redirect discarded its method and body")
			}
			if got := redirected.Load(); got != 0 {
				t.Errorf("followed %d redirects, want zero", got)
			}
		})
	}
}

func TestS3ClientPreservesReplayableRedirects(t *testing.T) {
	for _, status := range []int{307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var uploaded atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if r.URL.Path != "/target" {
					w.Header().Set("Location", "/target")
					w.WriteHeader(status)
					return
				}
				if err != nil || r.Method != http.MethodPut || string(body) != "payload" {
					t.Errorf("redirected upload: method=%s body=%q err=%v", r.Method, body, err)
				}
				uploaded.Store(true)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/object", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := sharedHTTPClient().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			if !uploaded.Load() {
				t.Fatal("upload did not reach redirect target")
			}
		})
	}
}

func TestS3ClientStopsReplayableRedirectLoops(t *testing.T) {
	var hops atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		w.Header().Set("Location", "/again")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/object", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := sharedHTTPClient().Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errTooManyRedirects) {
		t.Fatalf("err = %v, want redirect limit", err)
	}
	if got := hops.Load(); got != maxRedirects {
		t.Fatalf("server saw %d requests, want %d", got, maxRedirects)
	}
}

func TestSharedClientsRefuseHTTPSToHTTPRedirects(t *testing.T) {
	var leaked atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()
	for _, tc := range []struct {
		name   string
		client *http.Client
		status int
	}{
		{"storage 307", sharedHTTPClient(), http.StatusTemporaryRedirect},
		{"storage 308", sharedHTTPClient(), http.StatusPermanentRedirect},
		{"delivery 302", sharedDeliveryHTTPClient, http.StatusFound},
		{"delivery 307", sharedDeliveryHTTPClient, http.StatusTemporaryRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The redirect keeps the query, as a token-authenticated delivery
			// endpoint or a misbehaving S3 provider might.
			secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", plain.URL+r.URL.RequestURI())
				w.WriteHeader(tc.status)
			}))
			defer secure.Close()
			// The test server's certificate needs its own transport; the redirect
			// policy under test is the shared client's.
			client := &http.Client{Transport: secure.Client().Transport, CheckRedirect: tc.client.CheckRedirect}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, secure.URL+"/object?token=secret", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "AWS4-HMAC-SHA256 signed")
			resp, err := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil {
				t.Error("followed a redirect from https to plain http")
			}
		})
	}
	if got := leaked.Load(); got != 0 {
		t.Fatalf("plain HTTP server received %d requests", got)
	}
}
