package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"strings"
	"testing"
)

// endlessReader yields bytes forever and counts how many were read.
type endlessReader struct{ n int64 }

func (r *endlessReader) Read(p []byte) (int, error) {
	r.n += int64(len(p))
	return len(p), nil
}

func TestReadProviderResponseStopsAtLimit(t *testing.T) {
	body := &endlessReader{}
	if _, err := ReadProviderResponse(body); !errors.Is(err, ErrSubtitleTooLarge) {
		t.Fatalf("ReadProviderResponse() error = %v, want ErrSubtitleTooLarge", err)
	}
	if body.n > 2*MaxProviderResponseSize {
		t.Fatalf("read %d bytes from an endless body, want about %d", body.n, MaxProviderResponseSize)
	}
}

func TestReadProviderResponseAcceptsLimit(t *testing.T) {
	data, err := ReadProviderResponse(bytes.NewReader(make([]byte, MaxProviderResponseSize)))
	if err != nil {
		t.Fatalf("ReadProviderResponse() error = %v", err)
	}
	if len(data) != MaxProviderResponseSize {
		t.Fatalf("len = %d, want %d", len(data), MaxProviderResponseSize)
	}
}

func TestProviderErrorBodyIsBounded(t *testing.T) {
	body := &endlessReader{}
	if got := ProviderErrorBody(body); len(got) != maxProviderErrorBodySize {
		t.Fatalf("len = %d, want %d", len(got), maxProviderErrorBodySize)
	}
	if got := ProviderErrorBody(strings.NewReader("quota exceeded")); got != "quota exceeded" {
		t.Fatalf("ProviderErrorBody() = %q", got)
	}
}

func TestExtractSubtitleFromZipReturnsFirstSubtitle(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{"readme.txt": "ignore", "movie.en.ass": "[Script Info]\n"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	content, format, err := ExtractSubtitleFromZip(buf.Bytes())
	if err != nil {
		t.Fatalf("ExtractSubtitleFromZip() error = %v", err)
	}
	if string(content) != "[Script Info]\n" || format != FormatASS {
		t.Fatalf("got %q %q", content, format)
	}
}

func TestExtractSubtitleFromZipRejectsOversizedEntry(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("bomb.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(w, zeroReader{}, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() > 1<<20 {
		t.Fatalf("archive is %d bytes, want a high-ratio archive under 1 MiB", buf.Len())
	}

	if _, _, err := ExtractSubtitleFromZip(buf.Bytes()); !errors.Is(err, ErrSubtitleTooLarge) {
		t.Fatalf("ExtractSubtitleFromZip() error = %v, want ErrSubtitleTooLarge", err)
	}
}

// A header that understates an entry's size must not let it expand in full.
func TestExtractSubtitleFromZipStopsAtDeclaredSize(t *testing.T) {
	var raw bytes.Buffer
	fw, err := flate.NewWriter(&raw, flate.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(fw, zeroReader{}, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := fw.Close(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "bomb.srt",
		Method:             zip.Deflate,
		CompressedSize64:   uint64(raw.Len()),
		UncompressedSize64: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	content, _, err := ExtractSubtitleFromZip(buf.Bytes())
	if err == nil {
		t.Fatalf("ExtractSubtitleFromZip() returned %d bytes, want an error", len(content))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
