package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSkipNativeMediaCompression(t *testing.T) {
	tests := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/v1/stream/s1", true},
		{http.MethodHead, "/api/v1/playback/transcode/s1/segment/000.ts", true},
		{http.MethodGet, "/api/v1/downloads/d1/file", true},
		{http.MethodGet, "/api/v1/downloads/d1/file-proxy", true},
		{http.MethodGet, "/api/v1/direct-download", true},
		{http.MethodHead, "/api/v1/direct-download-proxy", true},
		{http.MethodGet, "/api/v1/ebooks/c1/files/f1/read", true},
		{http.MethodGet, "/api/v1/stream/s1/subtitles/1", false},
		{http.MethodGet, "/api/v1/stream/s1/subtitles/1/fonts", false},
		{http.MethodGet, "/api/v1/stream/s1/", false},
		{http.MethodPost, "/api/v1/stream/s1", false},
		{http.MethodGet, "/API/v1/stream/s1", false},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if got := skipNativeMediaCompression(httptest.NewRequest(tt.method, tt.path, nil)); got != tt.want {
				t.Fatalf("skipNativeMediaCompression = %v, want %v", got, tt.want)
			}
		})
	}
}
