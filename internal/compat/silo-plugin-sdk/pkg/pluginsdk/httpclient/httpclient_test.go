package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostJSONSetsApiKeyAndDecodes(t *testing.T) {
	var key, method, ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, method, ct = r.Header.Get("X-Api-Key"), r.Method, r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()
	var out struct {
		ID int `json:"id"`
	}
	if err := New(srv.URL, "secret", nil).PostJSON(context.Background(), "/x", map[string]any{"a": 1}, &out); err != nil {
		t.Fatalf("PostJSON: %v", err)
	}
	if key != "secret" || method != http.MethodPost || ct != "application/json" || out.ID != 7 {
		t.Fatalf("got key=%q method=%q ct=%q id=%d", key, method, ct, out.ID)
	}
}

func TestStatusErrorParsesMessageWithRawFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/json":
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"message":"dup"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`boom`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	err := c.GetJSON(context.Background(), "/json", nil)
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != 409 || se.Message != "dup" {
		t.Fatalf("want StatusError{409,dup}, got %v", err)
	}
	err = c.GetJSON(context.Background(), "/raw", nil)
	if !errors.As(err, &se) || se.StatusCode != 500 || se.Message != "boom" || se.Body != "boom" {
		t.Fatalf("want StatusError{500,boom,boom}, got %v", err)
	}
}

func TestEmpty2xxBodyIsSuccessButTruncatedIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/empty":
			w.WriteHeader(http.StatusCreated) // no body
		case "/truncated":
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":1`)) // cut off mid-JSON
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "k", nil)

	var out struct {
		ID int `json:"id"`
	}
	if err := c.PostJSON(context.Background(), "/empty", nil, &out); err != nil {
		t.Fatalf("empty 2xx body should be success, got %v", err)
	}
	if out.ID != 0 {
		t.Fatalf("empty body should leave zero value, got %d", out.ID)
	}
	out.ID = 5
	if err := c.PostJSON(context.Background(), "/truncated", nil, &out); err == nil {
		t.Fatal("truncated body should be a decode error, got nil")
	}
}

func TestRequiresBaseURLAndKeyAndTrimsBaseURL(t *testing.T) {
	if err := New("", "k", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty base url")
	}
	if err := New("http://x", "", nil).GetJSON(context.Background(), "/x", nil); err == nil {
		t.Fatal("want error for empty api key")
	}
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	// trailing slash on base url must not double up with the leading slash on path
	var out map[string]any
	if err := New(srv.URL+"/", "k", nil).GetJSON(context.Background(), "/api/v1/x", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotPath != "/api/v1/x" || strings.Contains(gotPath, "//") {
		t.Fatalf("bad joined path %q", gotPath)
	}
}

func TestApiKeyNotLeakedInError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"message":"upstream boom"}`))
	}))
	defer srv.Close()
	err := New(srv.URL, "super-secret-key", nil).GetJSON(context.Background(), "/x", nil)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("api key leaked into error: %v", err)
	}
	var se *StatusError
	if errors.As(err, &se) && (strings.Contains(se.Body, "super-secret-key") || strings.Contains(se.Message, "super-secret-key")) {
		t.Fatalf("api key leaked into StatusError: %+v", se)
	}
}

func TestMalformedBodyReturnsDecodeErrorAndConnectionIsReused(t *testing.T) {
	// The server returns malformed JSON on every request. We assert:
	// 1. DoJSON returns a non-nil error containing "decode response".
	// 2. The connection is reused across two sequential calls (same RemoteAddr),
	//    which is only possible when the body was fully drained on the first call.
	connIDs := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connIDs[r.RemoteAddr]++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"bad":`)) // intentionally truncated / malformed
	}))
	defer srv.Close()

	c := New(srv.URL, "k", nil)
	var out struct{ X string }

	err := c.GetJSON(context.Background(), "/x", &out)
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error should mention decode response, got: %v", err)
	}

	// Second call — if the body was not drained the connection cannot be reused.
	err = c.GetJSON(context.Background(), "/x", &out)
	if err == nil {
		t.Fatal("expected decode error on second call, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("second error should mention decode response, got: %v", err)
	}

	// Check that exactly one remote address was used (connection pooled/reused).
	if len(connIDs) != 1 {
		t.Fatalf("expected 1 distinct connection (reuse), got %d: %v", len(connIDs), connIDs)
	}
}

func TestResponseBodyCappedAtMaxResponseBody(t *testing.T) {
	// A 2 MiB body must not be read unbounded; the LimitReader truncates it,
	// so decode fails cleanly (no hang, no OOM) rather than reading it all.
	big := make([]byte, 2*1024*1024)
	for i := range big {
		big[i] = 'a'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"x":"`))
		w.Write(big)
		// intentionally never closes the JSON string -> truncated at the cap
	}))
	defer srv.Close()
	var out map[string]any
	err := New(srv.URL, "k", nil).GetJSON(context.Background(), "/x", &out)
	if err == nil {
		t.Fatal("want a decode error from the capped/truncated body, got nil")
	}
}
