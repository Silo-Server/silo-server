package playback

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveStreamCandidateSelectsCandidateWithoutFollowingRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("candidate"); got != "3" {
			t.Fatalf("candidate = %q", got)
		}
		if got := r.URL.Query().Get("refresh"); got != "true" {
			t.Fatalf("refresh = %q", got)
		}
		w.Header().Set("Location", "https://play.test/3")
		w.Header().Set("X-Silo-Stream-Candidates", "10")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	location, count, unavailable, err := resolveStreamCandidate(context.Background(), server.URL+"/resolve/movie/tt123?quality=1080p", 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if location != "https://play.test/3" || count != 10 || unavailable {
		t.Fatalf("result = %q, %d, %v", location, count, unavailable)
	}
}

func TestResolveStreamCandidateReportsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusNotFound)
	}))
	defer server.Close()

	_, _, unavailable, err := resolveStreamCandidate(context.Background(), fmt.Sprintf("%s/resolve/movie/tt123", server.URL), 9, false)
	if err != nil || !unavailable {
		t.Fatalf("unavailable = %v, err = %v", unavailable, err)
	}
}
