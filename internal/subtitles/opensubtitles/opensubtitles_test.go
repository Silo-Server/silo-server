package opensubtitles

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func TestLoginRefusesRedirectOffTheAPIHost(t *testing.T) {
	var otherHits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		otherHits.Add(1)
		_, _ = w.Write([]byte(`{"token":"off-host","status":200}`))
	}))
	defer other.Close()
	// A 307 makes Go resend the POST body, which holds the login.
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	}))
	defer api.Close()

	p := New(Config{Username: "user", Password: "synthetic", BaseURL: api.URL})
	if _, err := p.Search(t.Context(), subtitles.SearchRequest{IMDbID: "tt0000001"}); err == nil {
		t.Fatal("login followed an off-host redirect")
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("off-host server received %d requests", n)
	}
}
