package playback

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeDirectPlayRedirectsSTRM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.strm")
	want := "https://media.example.test/play/movie.mkv?token=secret"
	if err := os.WriteFile(path, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/media/movie.strm", nil)
	if err := ServeDirectPlay(rec, req, path); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("location = %q, want %q", got, want)
	}
}

func TestServeDirectPlayRejectsEmptySTRM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movie.strm")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/media/movie.strm", nil)
	err := ServeDirectPlay(rec, req, path)
	if err == nil {
		t.Fatal("expected error")
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "stream shortcut is empty") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}
