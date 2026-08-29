package jellycompat

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkipCompatMediaCompression(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/Videos/i1/stream", true},
		{http.MethodHead, "/Videos/i1/stream.mkv", true},
		{http.MethodGet, "/Videos/i1/hls/p1/000.ts", true},
		{http.MethodGet, "/Videos/i1/audio-v2/stream", true},
		{http.MethodHead, "/Videos/i1/audio-v2/stream.mkv", true},
		{http.MethodGet, "/Videos/i1/audio-v2/hls/p1/000.ts", true},
		{http.MethodGet, "/Items/i1/Download", true},
		{http.MethodGet, "/Videos/i1/master.m3u8", false},
		{http.MethodGet, "/Videos/i1/hls/p1/stream.m3u8", false},
		{http.MethodGet, "/Videos/i1/audio-v2/master.m3u8", false},
		{http.MethodGet, "/Videos/i1/audio-v2/hls/p1/stream.m3u8", false},
		{http.MethodGet, "/Videos/i1/stream/subtitles/1", false},
		{http.MethodGet, "/Videos/i1/stream/", false},
		{http.MethodPost, "/Videos/i1/stream", false},
		{http.MethodGet, "/videos/i1/stream", false},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if got := skipCompatMediaCompression(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
				t.Fatalf("skipCompatMediaCompression = %v, want %v", got, tt.want)
			}
		})
	}
}
