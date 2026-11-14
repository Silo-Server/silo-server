package subdl

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func zipArchive(t *testing.T, name string, content io.Reader) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(w, content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func serveDownload(t *testing.T, status int, body []byte) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return New(Config{APIKey: "synthetic", DLBaseURL: server.URL})
}

func TestDownloadRejectsOversizedResponse(t *testing.T) {
	p := serveDownload(t, http.StatusOK, make([]byte, subtitles.MaxProviderResponseSize+1))

	if _, _, err := p.Download(t.Context(), "/subtitle/3197651-3213944.zip", subtitles.DownloadTarget{}); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}

func TestDownloadRejectsHighRatioZipEntry(t *testing.T) {
	archive := zipArchive(t, "movie.srt", io.LimitReader(zeros{}, 64<<20))
	p := serveDownload(t, http.StatusOK, archive)

	if _, _, err := p.Download(t.Context(), "/subtitle/3197651-3213944.zip", subtitles.DownloadTarget{}); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}

func TestDownloadBoundsErrorBody(t *testing.T) {
	p := serveDownload(t, http.StatusBadGateway, bytes.Repeat([]byte("x"), 1<<20))

	_, _, err := p.Download(t.Context(), "/subtitle/3197651-3213944.zip", subtitles.DownloadTarget{})
	if err == nil {
		t.Fatal("Download() error = nil, want an error")
	}
	if len(err.Error()) > 8<<10 {
		t.Fatalf("Download() error length = %d, want a short error", len(err.Error()))
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
