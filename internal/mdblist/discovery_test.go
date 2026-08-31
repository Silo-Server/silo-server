package mdblist

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestTransportErrorOmitsAPIKey(t *testing.T) {
	secretKey := "super-secret-mdblist-key-12345"
	c := NewClient(secretKey, &http.Client{})
	c.baseURL = "http://127.0.0.1:0"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.Search(ctx, "test")
	if err == nil {
		t.Fatal("expected transport error, got nil")
	}

	errStr := err.Error()
	if strings.Contains(errStr, secretKey) {
		t.Fatalf("transport error leaked API key! error string: %s", errStr)
	}
	if !strings.Contains(errStr, "REDACTED") {
		t.Fatalf("expected REDACTED in sanitized transport error string: %s", errStr)
	}
}
