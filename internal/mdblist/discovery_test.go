package mdblist

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSearchAttachesAPIKeyAndQuery(t *testing.T) {
	var capturedPath, capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"user_name":"alice","name":"Horror","slug":"horror","mediatype":"movie","items":10,"likes":5}]`))
	}))
	defer srv.Close()

	c := NewClient("secret-key", srv.Client())
	c.baseURL = srv.URL

	lists, err := c.Search(context.Background(), "horror")
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if capturedPath != "/lists/search" {
		t.Errorf("path = %q, want /lists/search", capturedPath)
	}
	if !strings.Contains(capturedQuery, "apikey=secret-key") {
		t.Errorf("apikey missing from query: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "query=horror") {
		t.Errorf("query missing from query: %q", capturedQuery)
	}
	if len(lists) != 1 {
		t.Fatalf("got %d lists, want 1", len(lists))
	}
	if lists[0].URL != "https://mdblist.com/lists/alice/horror" {
		t.Errorf("URL not synthesized correctly: %q", lists[0].URL)
	}
}

func TestSearchWithoutKeyReturnsErrNotConfigured(t *testing.T) {
	c := NewClient("", nil)
	_, err := c.Search(context.Background(), "horror")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestSearchEmptyQueryRejected(t *testing.T) {
	c := NewClient("k", nil)
	if _, err := c.Search(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty query, got nil")
	}
}

func TestTopHitsTopEndpoint(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c := NewClient("k", srv.Client())
	c.baseURL = srv.URL
	if _, err := c.Top(context.Background()); err != nil {
		t.Fatalf("Top: %v", err)
	}
	if capturedPath != "/lists/top" {
		t.Errorf("path = %q, want /lists/top", capturedPath)
	}
}

func TestSearchSurfacesUpstreamErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := NewClient("bad-key", srv.Client())
	c.baseURL = srv.URL
	if _, err := c.Search(context.Background(), "x"); err == nil {
		t.Fatal("expected unauthorized error, got nil")
	}
}

// assertErrorOmitsAPIKey fails when the key appears anywhere errors.Unwrap
// can reach, not only in the top-level message. It never prints the key.
func assertErrorOmitsAPIKey(t *testing.T, err error, apiKey string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		if msg := e.Error(); strings.Contains(msg, apiKey) {
			t.Fatalf("error chain leaks the API key: %q", strings.ReplaceAll(msg, apiKey, "[KEY]"))
		}
		var urlErr *url.Error
		if errors.As(e, &urlErr) && strings.Contains(urlErr.URL, apiKey) {
			t.Fatalf("url.Error.URL leaks the API key: %q", strings.ReplaceAll(urlErr.URL, apiKey, "[KEY]"))
		}
	}
}

func TestTransportErrorOmitsAPIKey(t *testing.T) {
	secretKey := "super-secret-mdblist-key-12345"
	c := NewClient(secretKey, &http.Client{})
	c.baseURL = "http://127.0.0.1:0"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.Search(ctx, "test")
	assertErrorOmitsAPIKey(t, err, secretKey)
}

// urlQuotingTransport fails every request with an error that quotes the full
// request URL, as some transports and proxies do.
type urlQuotingTransport struct{ cause error }

func (t urlQuotingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("dial %s: %w", req.URL, t.cause)
}

// urlTimeoutError is a net.Error timeout whose message quotes the request
// URL, key included, as a transport's own timeout error can.
type urlTimeoutError struct{ url string }

func (e urlTimeoutError) Error() string { return "read " + e.url + ": i/o timeout" }
func (urlTimeoutError) Timeout() bool   { return true }
func (urlTimeoutError) Temporary() bool { return true }

type timeoutTransport struct{}

func (timeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, urlTimeoutError{url: req.URL.String()}
}

func TestTransportErrorRedactsAPIKeyInWrappedError(t *testing.T) {
	secretKey := "super-secret-mdblist-key-12345"
	cause := errors.New("connection refused")
	c := NewClient(secretKey, &http.Client{Transport: urlQuotingTransport{cause: cause}})

	_, err := c.Search(context.Background(), "test")
	assertErrorOmitsAPIKey(t, err, secretKey)
	// The transport quoted the URL in its own text, so the key is masked there.
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("expected REDACTED in the sanitized error: %q", strings.ReplaceAll(err.Error(), secretKey, "[KEY]"))
	}
	if !errors.Is(err, cause) {
		t.Fatalf("sanitized error no longer matches its cause: %q", strings.ReplaceAll(err.Error(), secretKey, "[KEY]"))
	}
}

// keyChangingTransport replaces the client's key while the request is in
// flight, as a settings reload can, then fails with an error that quotes the
// key the request sent outside any URL.
type keyChangingTransport struct {
	client *Client
	newKey string
}

func (t keyChangingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.client.SetAPIKey(t.newKey)
	return nil, fmt.Errorf("proxy rejected apikey %s", req.URL.Query().Get("apikey"))
}

func TestTransportErrorRedactsTheKeySentAfterASettingsReload(t *testing.T) {
	sentKey := "super-secret-mdblist-key-12345"
	for name, newKey := range map[string]string{"replaced": "another-mdblist-key-67890", "cleared": ""} {
		t.Run(name, func(t *testing.T) {
			c := NewClient(sentKey, nil)
			c.http = &http.Client{Transport: keyChangingTransport{client: c, newKey: newKey}}

			_, err := c.Search(context.Background(), "test")
			assertErrorOmitsAPIKey(t, err, sentKey)
		})
	}
}

func TestTransportErrorKeepsTimeoutClassification(t *testing.T) {
	secretKey := "super-secret-mdblist-key-12345"
	c := NewClient(secretKey, &http.Client{Transport: timeoutTransport{}})

	_, err := c.Search(context.Background(), "test")
	assertErrorOmitsAPIKey(t, err, secretKey)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("sanitized error lost its timeout classification: %q", strings.ReplaceAll(err.Error(), secretKey, "[KEY]"))
	}
}
