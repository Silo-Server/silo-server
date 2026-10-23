package opensubtitles

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/netguard"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// serveDownload fakes the provider API and a file host on loopback. Callers
// that expect the file to arrive pass netguard.WithPrivateAccess, since a
// download link is only trusted with public addresses.
func serveDownload(t *testing.T, status int, file []byte) *Provider {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write([]byte(`{"token":"synthetic","status":200}`))
		case "/download":
			_, _ = w.Write([]byte(`{"link":"` + server.URL + `/file","file_name":"movie.srt"}`))
		case "/file":
			w.WriteHeader(status)
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
	data, format, err := serveDownload(t, http.StatusOK, []byte(srt)).Download(netguard.WithPrivateAccess(t.Context()), "7")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != srt || format != subtitles.FormatSRT {
		t.Fatalf("got %q %q", data, format)
	}
}

func TestDownloadRejectsOversizedFile(t *testing.T) {
	p := serveDownload(t, http.StatusOK, make([]byte, subtitles.MaxProviderResponseSize+1))

	if _, _, err := p.Download(netguard.WithPrivateAccess(t.Context()), "7"); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}

func TestDownloadRefusesLinkOnTheServersNetwork(t *testing.T) {
	p := serveDownload(t, http.StatusOK, []byte("1\n00:00:01,000 --> 00:00:02,000\nprivate\n"))

	// The dialer refuses the address before connecting, so the file host
	// never sees the request.
	if _, _, err := p.Download(t.Context(), "7"); !errors.Is(err, netguard.ErrPrivateDestination) {
		t.Fatalf("Download() error = %v, want ErrPrivateDestination", err)
	}
}

func TestDownloadRejectsFileHostError(t *testing.T) {
	const errorBody = "<html>410 Gone</html>"
	p := serveDownload(t, http.StatusGone, []byte(errorBody))

	data, format, err := p.Download(netguard.WithPrivateAccess(t.Context()), "7")
	if err == nil {
		t.Fatalf("Download() = %q, %q, nil; want an error", data, format)
	}
	if data != nil || format != "" {
		t.Fatalf("Download() = %q, %q, %v; want no subtitle", data, format, err)
	}
	if !strings.Contains(err.Error(), "410") || !strings.Contains(err.Error(), errorBody) {
		t.Fatalf("Download() error = %q; want status and bounded response body", err)
	}
}
