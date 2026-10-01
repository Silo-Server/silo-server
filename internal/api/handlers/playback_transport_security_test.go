package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestFetchRemoteTranscodeCapabilitiesDoesNotForwardJWTOnRedirect(t *testing.T) {
	const jwt = "cluster-jwt-secret"
	var redirectTargetHits atomic.Int32
	var redirectTargetAuthorization atomic.Value
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectTargetHits.Add(1)
		redirectTargetAuthorization.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(playback.HWAccelInfo{Resolved: "qsv"})
	}))
	defer redirectTarget.Close()

	var sourceAuthorization atomic.Value
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceAuthorization.Store(r.Header.Get("Authorization"))
		http.Redirect(w, r, redirectTarget.URL+"/hw-capabilities", http.StatusFound)
	}))
	defer source.Close()

	_, err := fetchRemoteTranscodeCapabilities(context.Background(), source.URL, jwt)
	if got, want := sourceAuthorization.Load(), "Bearer "+jwt; got != want {
		t.Fatalf("source Authorization = %#v, want %q", got, want)
	}
	if got := redirectTargetHits.Load(); got != 0 {
		t.Fatalf("redirect target requests = %d, Authorization = %#v; want no redirect request", got, redirectTargetAuthorization.Load())
	}
	if err == nil || !strings.Contains(err.Error(), "node returned 302") {
		t.Fatalf("redirect response error = %v, want node returned 302", err)
	}
}

func TestFetchRemoteTranscodeCapabilitiesRejectsOversizedResponse(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"resolved":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 2<<20)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer remote.Close()

	_, err := fetchRemoteTranscodeCapabilities(context.Background(), remote.URL, "cluster-jwt-secret")
	if err == nil || !strings.Contains(err.Error(), "capability response exceeds") {
		t.Fatalf("oversized response error = %v, want bounded-response error", err)
	}
}
