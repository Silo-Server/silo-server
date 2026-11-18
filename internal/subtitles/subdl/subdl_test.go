package subdl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func TestSearchTransportErrorOmitsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	_, err := New(Config{BaseURL: server.URL, APIKey: "synthetic-subdl-key"}).Search(t.Context(), subtitles.SearchRequest{Title: "Example"})
	if err == nil {
		t.Fatal("search against a closed server succeeded")
	}
	if strings.Contains(err.Error(), "synthetic-subdl-key") {
		t.Fatalf("search error exposes the API key: %v", err)
	}
}
