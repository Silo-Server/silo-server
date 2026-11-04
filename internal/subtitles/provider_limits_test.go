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

func TestExtractSubtitleFromZipPicksTargetEpisode(t *testing.T) {
	s01e02 := DownloadTarget{Season: 1, Episode: 2}
	for _, tc := range []struct {
		name    string
		entries []string
		target  DownloadTarget
		want    string // entry whose content is returned; "" expects ErrNoEpisodeSubtitle
	}{
		{"movie takes the first subtitle", []string{"readme.txt", "movie.en.ass", "movie.fr.srt"}, DownloadTarget{}, "movie.en.ass"},
		{"season pack", []string{"Show.S01/Show.S01E01.ar.srt", "Show.S01/Show.S01E02.ar.srt", "Show.S01/Show.S01E03.ar.srt"}, s01e02, "Show.S01/Show.S01E02.ar.srt"},
		{"NxNN names", []string{"Show - 1x01.srt", "Show - 1x02.srt"}, s01e02, "Show - 1x02.srt"},
		{"bracketed NxNN names", []string{"Show - [1x01].srt", "Show - [1x02].srt"}, s01e02, "Show - [1x02].srt"},
		{"season must match", []string{"Show.S02E02.srt", "Show.S01E02.srt"}, s01e02, "Show.S01E02.srt"},
		{"macOS resource forks are skipped", []string{"__MACOSX/._Show.S01E02.srt", "Show.S01E02.srt"}, s01e02, "Show.S01E02.srt"},
		{"multi-episode file takes the range", []string{"Show.S01E01.srt", "Show.S01E01E02.srt"}, DownloadTarget{Season: 1, Episode: 1, EndEpisode: 2}, "Show.S01E01E02.srt"},
		{"multi-episode file falls back to its first episode", []string{"Show.S01E02.srt", "Show.S01E01.srt"}, DownloadTarget{Season: 1, Episode: 1, EndEpisode: 2}, "Show.S01E01.srt"},
		{"single episode skips a range", []string{"Show.S01E01E02.srt", "Show.S01E01.srt"}, DownloadTarget{Season: 1, Episode: 1}, "Show.S01E01.srt"},
		{"unlabeled entries keep the first", []string{"Show.en.srt", "Show.en.sdh.srt"}, s01e02, "Show.en.srt"},
		{"another episode alone fails", []string{"Show.S01E01.srt"}, s01e02, ""},
		{"bare numbers are not episodes", []string{"Room.102.srt", "Show.S01E01.srt"}, s01e02, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			for _, name := range tc.entries {
				w, err := zw.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.WriteString(w, name)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}

			content, _, err := ExtractSubtitleFromZip(buf.Bytes(), tc.target)
			if tc.want == "" {
				if !errors.Is(err, ErrNoEpisodeSubtitle) {
					t.Fatalf("got %q, %v; want ErrNoEpisodeSubtitle", content, err)
				}
				return
			}
			if err != nil || string(content) != tc.want {
				t.Fatalf("got %q, %v; want %q", content, err, tc.want)
			}
		})
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

	if _, _, err := ExtractSubtitleFromZip(buf.Bytes(), DownloadTarget{}); !errors.Is(err, ErrSubtitleTooLarge) {
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

	content, _, err := ExtractSubtitleFromZip(buf.Bytes(), DownloadTarget{})
	if err == nil {
		t.Fatalf("ExtractSubtitleFromZip() returned %d bytes, want an error", len(content))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
