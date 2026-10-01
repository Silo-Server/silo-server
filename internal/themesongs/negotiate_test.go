package themesongs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNegotiateChoosesOriginalThenConversion(t *testing.T) {
	ogg := File{Song: Song{Container: "ogg"}, AudioCodec: "vorbis"}
	alac := File{Song: Song{Container: "m4a"}, AudioCodec: "alac"}
	wav := File{Song: Song{Container: "wav"}, AudioCodec: "pcm_s24le"}
	aacMP4 := Format{Container: "mp4", AudioCodec: "aac"}
	for _, tc := range []struct {
		name     string
		file     File
		accepted []Format
		want     Delivery
		ok       bool
	}{
		{"undescribed client keeps original", ogg, nil, DeliveryOriginal, true},
		{"matching codec", ogg, []Format{{Container: "ogg", AudioCodec: "vorbis"}, aacMP4}, DeliveryOriginal, true},
		{"container without codec", ogg, []Format{{Container: "OGG"}}, DeliveryOriginal, true},
		{"wrong codec in container converts", ogg, []Format{{Container: "ogg", AudioCodec: "opus"}, aacMP4}, DeliveryConverted, true},
		{"mp4 family is one container", alac, []Format{{Container: "mp4", AudioCodec: "alac"}}, DeliveryOriginal, true},
		{"alac unsupported converts", alac, []Format{{Container: "m4a", AudioCodec: "aac"}}, DeliveryConverted, true},
		{"pcm sample formats normalize", wav, []Format{{Container: "wav", AudioCodec: "pcm"}}, DeliveryOriginal, true},
		{"nothing playable", ogg, []Format{{Container: "mp3", AudioCodec: "mp3"}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Negotiate(tc.file, tc.accepted)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Negotiate = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestConversionFor(t *testing.T) {
	for _, tc := range []struct {
		channels int
		want     Conversion
	}{
		{1, Conversion{Channels: 1, BitrateKbps: 128}},
		{2, Conversion{Channels: 2, BitrateKbps: 192}},
		{6, Conversion{Channels: 2, BitrateKbps: 192, SourceChannels: 6}},
		{0, Conversion{Channels: 2, BitrateKbps: 192}},
	} {
		if got := ConversionFor(File{AudioChannels: tc.channels}); got != tc.want {
			t.Fatalf("ConversionFor(%d) = %+v, want %+v", tc.channels, got, tc.want)
		}
	}
}

func TestServeFileRefusesReplacedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.mp3")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	modified := info.ModTime().Truncate(time.Microsecond)
	serve := func(size int64, when time.Time, rangeHeader string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/theme", nil)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		rec := httptest.NewRecorder()
		ServeFile(rec, req, "4", path, size, when)
		return rec
	}
	if rec := serve(info.Size(), modified, "bytes=2-4"); rec.Code != http.StatusPartialContent || rec.Body.String() != "234" || rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("served %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec := serve(info.Size()+1, modified, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("replaced size served %d", rec.Code)
	}
	if rec := serve(info.Size(), modified.Add(time.Second), ""); rec.Code != http.StatusNotFound {
		t.Fatalf("replaced mtime served %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	ServeFile(rec, httptest.NewRequest(http.MethodGet, "/theme", nil), "4", "relative/theme.mp3", info.Size(), modified)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("relative path served %d", rec.Code)
	}
}

func TestServeConvertedHeadDoesNotStartFFmpeg(t *testing.T) {
	rec := httptest.NewRecorder()
	// A missing binary proves HEAD never reaches FFmpeg: GET would fail.
	ServeConverted(rec, httptest.NewRequest(http.MethodHead, "/theme", nil), "/nonexistent/theme.ogg", Conversion{Channels: 2, BitrateKbps: 192}, 0, "/nonexistent/ffmpeg")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ConvertedContentType || rec.Body.Len() != 0 {
		t.Fatalf("HEAD = %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}
