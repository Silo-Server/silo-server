package downloads

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestStreamArtworkDispatchesRootRelativeURLInProcess(t *testing.T) {
	const body = "artwork-bytes"
	var handled bool
	artworkDelivery := chi.NewRouter()
	artworkDelivery.Get("/api/v1/artwork/{capability}/{variant}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handled = true
		if got := chi.URLParam(r, "capability"); got != "capability" {
			t.Errorf("capability = %q", got)
		}
		if got := chi.URLParam(r, "variant"); got != "w500" {
			t.Errorf("variant = %q", got)
		}
		if r.URL.Query().Get("signature") != "signed" {
			t.Errorf("signature = %q", r.URL.Query().Get("signature"))
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	svc := &Service{
		artworkDelivery: artworkDelivery,
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("HTTP client must not be used for root-relative artwork")
		})},
	}

	recorder := httptest.NewRecorder()
	err := svc.streamArtwork(
		t.Context(),
		recorder,
		httptest.NewRequest(http.MethodGet, "/api/v1/downloads/dl/artwork/poster", nil),
		"/api/v1/artwork/capability/w500?signature=signed",
	)
	if err != nil {
		t.Fatalf("streamArtwork() error = %v", err)
	}
	if !handled {
		t.Fatal("artwork delivery handler was not called")
	}
	if got := recorder.Body.String(); got != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	if got := recorder.Header().Get("Content-Type"); got != "image/webp" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "private, max-age=31536000, immutable" {
		t.Fatalf("Cache-Control = %q", got)
	}
}
