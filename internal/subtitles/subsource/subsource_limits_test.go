package subsource

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func serveDownload(t *testing.T, contentType string, body []byte) *Provider {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/subtitles/42/download" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return New(Config{APIKey: "synthetic", BaseURL: server.URL})
}

func TestDownloadExtractsZippedSubtitle(t *testing.T) {
	vtt := "WEBVTT\n\n00:01.000 --> 00:02.000\nHello\n"
	p := serveDownload(t, "application/zip", zipArchive(t, "movie.vtt", strings.NewReader(vtt)))

	data, format, err := p.Download(t.Context(), "42")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != vtt || format != subtitles.FormatVTT {
		t.Fatalf("got %q %q", data, format)
	}
}

func TestDownloadReturnsPlainSubtitle(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nHello\n"
	p := serveDownload(t, "application/x-subrip", []byte(srt))

	data, format, err := p.Download(t.Context(), "42")
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(data) != srt || format != subtitles.FormatSRT {
		t.Fatalf("got %q %q", data, format)
	}
}

func TestDownloadRejectsOversizedResponse(t *testing.T) {
	p := serveDownload(t, "application/x-subrip", make([]byte, subtitles.MaxProviderResponseSize+1))

	if _, _, err := p.Download(t.Context(), "42"); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}

func TestDownloadRejectsHighRatioZipEntry(t *testing.T) {
	archive := zipArchive(t, "movie.srt", io.LimitReader(zeros{}, 64<<20))
	p := serveDownload(t, "application/zip", archive)

	if _, _, err := p.Download(t.Context(), "42"); !errors.Is(err, subtitles.ErrSubtitleTooLarge) {
		t.Fatalf("Download() error = %v, want ErrSubtitleTooLarge", err)
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
