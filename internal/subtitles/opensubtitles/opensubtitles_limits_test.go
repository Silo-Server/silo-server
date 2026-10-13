package opensubtitles

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func serveDownload(t *testing.T, file []byte) *Provider {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"synthetic","status":200}`))
		case "/download":
			_, _ = w.Write([]byte(`{"link":"` + server.URL + `/file","file_name":"movie.srt"}`))
		case "/file":
			_, _ = w.Write(file)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return New(Config{Username: "user", Password: "pass", BaseURL: server.URL})
}

func TestDownloadReturnsSubtitle(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	data, format, err := serveDownload(t, []byte(srt)).Download(t.Context(), "7")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != srt || format != subtitles.FormatSRT {
		t.Fatalf("got %q %q", data, format)
	}
}

func TestDownloadRejectsOversizedFile(t *testing.T) {
	p := serveDownload(t, make([]byte, subtitles.MaxProviderResponseSize+1))

	if _, _, err := p.Download(t.Context(), "7"); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}
